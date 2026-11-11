package chat

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
)

const (
	maxTitleBytes   = 240
	maxMessageBytes = 128000
	listLimit       = 20
	// A tool returns each text cut at this many characters. session_read reads the rest.
	textLimit = 4000
	// The last answer in a session list is cut at this many characters.
	listAnswerLimit = 300
	snippetLimit    = 150
	maxSearchWords  = 6
)

// ConfigureSessions sets the check that session_new runs on a model that the caller chose.
func (s *Service) ConfigureSessions(check func(context.Context, Settings) error) {
	s.checkSettings = check
}

// turnID is the ID of the input that one tool call sends. A replay of the call computes the same
// ID, so the stored input is the replay guard.
func turnID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

func sessionRef(c sqlc.Conversation, pending int64) engine.SessionRef {
	state := "idle"
	if pending > 0 {
		state = "running"
	}
	return engine.SessionRef{ID: strconv.FormatInt(c.ID, 10), Title: c.Title, State: state}
}

func (s *Service) ref(ctx context.Context, q *sqlc.Queries, c sqlc.Conversation) (engine.SessionRef, error) {
	pending, err := q.PendingCount(ctx, c.ID)
	return sessionRef(c, pending), err
}

// childOf returns the child of parent that the model named.
func (s *Service) childOf(ctx context.Context, q *sqlc.Queries, parent sqlc.Conversation, id string) (sqlc.Conversation, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return sqlc.Conversation{}, ErrSessionNotFound
	}
	c, err := q.GetConversation(ctx, n)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (c.AgentID != parent.AgentID || c.ParentID.Int64 != parent.ID || !c.ParentID.Valid)) {
		return c, ErrSessionNotFound
	}
	return c, err
}

// repeatedTurn finds the conversation of a turn that a replayed tool call stored already.
func repeatedTurn(ctx context.Context, q *sqlc.Queries, turn string, parent sqlc.Conversation) (sqlc.Conversation, bool, error) {
	prior, err := q.GetTurn(ctx, turn)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.Conversation{}, false, nil
	}
	if err != nil {
		return sqlc.Conversation{}, false, err
	}
	c, err := q.GetConversation(ctx, prior.ConversationID)
	if err != nil {
		return c, false, err
	}
	if c.ParentID.Int64 != parent.ID {
		return c, false, ErrSessionNotFound
	}
	return c, true, nil
}

func checkMessage(message string) error {
	if strings.TrimSpace(message) == "" || len(message) > maxMessageBytes {
		return fmt.Errorf("%w: the message has 1 to %d bytes", ErrSessionInvalid, maxMessageBytes)
	}
	return nil
}

func (s *Service) ToolSessionNew(ctx context.Context, r engine.TaskContext, a engine.SessionNewArgs) (engine.SessionRef, error) {
	parent, err := s.sessionParent(ctx, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	if strings.TrimSpace(a.Title) == "" || len(a.Title) > maxTitleBytes || !strings.HasPrefix(r.Key, r.AgentID+"/") {
		return engine.SessionRef{}, fmt.Errorf("%w: the title has 1 to %d bytes and the call needs a durable key", ErrSessionInvalid, maxTitleBytes)
	}
	if err := checkMessage(a.Message); err != nil {
		return engine.SessionRef{}, err
	}
	turn := turnID(r.Key)
	if c, ok, err := repeatedTurn(ctx, s.q, turn, parent); err != nil || ok {
		if err != nil {
			return engine.SessionRef{}, err
		}
		return s.ref(ctx, s.q, c)
	}
	agent, err := s.q.GetAgent(ctx, r.AgentID)
	if err != nil {
		return engine.SessionRef{}, err
	}
	settings := Settings{Provider: cmp.Or(parent.ModelProvider, agent.ModelProvider), ModelID: cmp.Or(parent.ModelID, agent.ModelID), ThinkingLevel: cmp.Or(a.ThinkingLevel, parent.ThinkingLevel, agent.ThinkingLevel)}
	if a.Provider != "" || a.ModelID != "" {
		if a.Provider == "" || a.ModelID == "" {
			return engine.SessionRef{}, fmt.Errorf("%w: give provider and modelId together", ErrSessionInvalid)
		}
		settings.Provider, settings.ModelID = a.Provider, a.ModelID
	}
	if settings.ModelID == "" {
		return engine.SessionRef{}, ErrNoModel
	}
	if (a.Provider != "" || a.ThinkingLevel != "") && s.checkSettings != nil {
		if err := s.checkSettings(ctx, settings); err != nil {
			return engine.SessionRef{}, err
		}
	}
	cwd := parent.Cwd
	if a.Cwd != "" {
		if cwd, err = ProjectPath(a.Cwd); err != nil {
			return engine.SessionRef{}, err
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.SessionRef{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if c, ok, err := repeatedTurn(ctx, q, turn, parent); err != nil || ok {
		if err != nil {
			return engine.SessionRef{}, err
		}
		return s.ref(ctx, q, c)
	}
	if _, err := liveSource(ctx, q, r); err != nil {
		return engine.SessionRef{}, err
	}
	now := s.now().UnixMilli()
	child, err := q.CreateChildConversation(ctx, sqlc.CreateChildConversationParams{
		AgentID: r.AgentID, ChannelKey: ChatPrefix + uuid.NewV7().String(), Title: a.Title,
		ModelProvider: settings.Provider, ModelID: settings.ModelID, ThinkingLevel: settings.ThinkingLevel, Cwd: cwd,
		ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}, CreatedAt: now,
	})
	if err != nil {
		return engine.SessionRef{}, err
	}
	if _, err := q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turn, ConversationID: child.ID, Content: a.Message, WhenBusy: "followUp", ParentRequestID: sql.NullString{String: r.SourceRequestID, Valid: true}, CreatedAt: now}); err != nil {
		return engine.SessionRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.SessionRef{}, err
	}
	s.signal(child.ID)
	s.signal(parent.ID)
	s.Wake()
	return sessionRef(child, 1), nil
}

func (s *Service) ToolSessionSend(ctx context.Context, r engine.TaskContext, a engine.SessionSendArgs) (engine.SessionRef, error) {
	parent, err := s.sessionParent(ctx, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	if err := checkMessage(a.Message); err != nil {
		return engine.SessionRef{}, err
	}
	whenBusy := "followUp"
	switch a.Mode {
	case "", "queue":
	case "steer":
		whenBusy = "steer"
	default:
		return engine.SessionRef{}, fmt.Errorf("%w: mode is queue or steer", ErrSessionInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.SessionRef{}, err
	}
	defer func() { _ = tx.Rollback() }()
	child, err := s.childOf(ctx, q, parent, a.ID)
	if err != nil {
		return engine.SessionRef{}, err
	}
	turn := turnID(r.Key)
	if _, ok, err := repeatedTurn(ctx, q, turn, parent); err != nil || ok {
		if err != nil {
			return engine.SessionRef{}, err
		}
		return s.ref(ctx, q, child)
	}
	if _, err := liveSource(ctx, q, r); err != nil {
		return engine.SessionRef{}, err
	}
	if _, err := q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turn, ConversationID: child.ID, Content: a.Message, WhenBusy: whenBusy, ParentRequestID: sql.NullString{String: r.SourceRequestID, Valid: true}, CreatedAt: s.now().UnixMilli()}); err != nil {
		return engine.SessionRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.SessionRef{}, err
	}
	s.signal(child.ID)
	s.signal(parent.ID)
	s.Wake()
	return sessionRef(child, 1), nil
}

func (s *Service) ToolSessionStop(ctx context.Context, r engine.TaskContext, id string) (engine.SessionRef, error) {
	parent, err := s.sessionParent(ctx, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	child, err := s.childOf(ctx, s.q, parent, id)
	if err != nil {
		return engine.SessionRef{}, err
	}
	stored, err := s.stop(ctx, r.AgentID, child.ID, nil, &stopCall{task: r, parent: parent.ID, child: child})
	if err != nil {
		return engine.SessionRef{}, err
	}
	var ref engine.SessionRef
	if stored != "" {
		return ref, json.Unmarshal([]byte(stored), &ref)
	}
	return s.ref(ctx, s.q, child)
}

func (s *Service) ToolSessionFork(ctx context.Context, r engine.TaskContext, a engine.SessionForkArgs) (engine.SessionRef, error) {
	parent, err := s.sessionParent(ctx, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	if err := checkMessage(a.Message); err != nil {
		return engine.SessionRef{}, err
	}
	child, err := s.childOf(ctx, s.q, parent, a.ID)
	if err != nil {
		return engine.SessionRef{}, err
	}
	turn := turnID(r.Key)
	if c, ok, err := repeatedTurn(ctx, s.q, turn, parent); err != nil || ok {
		if err != nil {
			return engine.SessionRef{}, err
		}
		return s.ref(ctx, s.q, c)
	}
	fork, err := s.fork(ctx, child, a.Entry, false, func(ctx context.Context, q *sqlc.Queries, fork sqlc.Conversation) error {
		if _, err := liveSource(ctx, q, r); err != nil {
			return err
		}
		_, err := q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turn, ConversationID: fork.ID, Content: a.Message, WhenBusy: "followUp", ParentRequestID: sql.NullString{String: r.SourceRequestID, Valid: true}, CreatedAt: s.now().UnixMilli()})
		return err
	})
	if err != nil {
		return engine.SessionRef{}, err
	}
	s.signal(parent.ID)
	s.Wake()
	return sessionRef(fork, 1), nil
}

// readable returns a session that the tools of a bot may read: any session of the bot except a dream.
func (s *Service) readable(ctx context.Context, agentID, id string) (sqlc.Conversation, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return sqlc.Conversation{}, ErrSessionNotFound
	}
	c, err := s.q.GetConversation(ctx, n)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (c.AgentID != agentID || strings.HasPrefix(c.ChannelKey, dream.Channel))) {
		return c, ErrSessionNotFound
	}
	return c, err
}

func (s *Service) ToolSessionList(ctx context.Context, r engine.TaskContext, scope string) ([]engine.SessionInfo, error) {
	caller, err := s.toolCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	switch scope {
	case "", "children", "all":
	default:
		return nil, fmt.Errorf("%w: scope is children or all", ErrSessionInvalid)
	}
	rows, err := s.q.ListSessions(ctx, sqlc.ListSessionsParams{AgentID: r.AgentID, AllSessions: boolInt(scope == "all"), ParentID: sql.NullInt64{Int64: caller.ID, Valid: true}, MaxRows: listLimit})
	if err != nil {
		return nil, err
	}
	out := make([]engine.SessionInfo, 0, len(rows))
	for _, row := range rows {
		info := engine.SessionInfo{
			SessionRef: sessionRef(sqlc.Conversation{ID: row.ID, Title: row.Title}, row.Pending),
			Provider:   row.ModelProvider, ModelID: row.ModelID,
		}
		if row.ParentID.Valid {
			info.Parent = strconv.FormatInt(row.ParentID.Int64, 10)
		}
		latest, err := s.q.LatestTurn(ctx, row.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			turn := turnView(latest.ID, latest.Origin, latest.Status, latest.Reason.String, "", latest.Answer.String, latest.CreatedAt, latest.SettledAt, listAnswerLimit)
			info.LastTurn = &turn
		}
		out = append(out, info)
	}
	return out, nil
}

func (s *Service) ToolSessionRead(ctx context.Context, r engine.TaskContext, a engine.SessionReadArgs) (engine.SessionPage, error) {
	caller, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.SessionPage{}, err
	}
	conv, err := s.readable(ctx, caller.AgentID, a.ID)
	if err != nil {
		return engine.SessionPage{}, err
	}
	pending, err := s.q.PendingCount(ctx, conv.ID)
	if err != nil {
		return engine.SessionPage{}, err
	}
	page := engine.SessionPage{SessionRef: sessionRef(conv, pending)}
	if a.Turn != "" {
		return s.readText(ctx, conv, page, a)
	}
	limit := cmp.Or(a.Limit, 10)
	if limit < 1 || limit > 50 {
		return engine.SessionPage{}, fmt.Errorf("%w: limit is 1 to 50", ErrSessionInvalid)
	}
	if a.Before != "" {
		before, err := s.q.GetTurn(ctx, a.Before)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && before.ConversationID != conv.ID) {
			return engine.SessionPage{}, fmt.Errorf("%w: before is not a turn of this session", ErrSessionInvalid)
		}
		if err != nil {
			return engine.SessionPage{}, err
		}
	}
	rows, err := s.q.ListTurns(ctx, sqlc.ListTurnsParams{ConversationID: conv.ID, Before: a.Before, MaxRows: int64(limit) + 1})
	if err != nil {
		return engine.SessionPage{}, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		page.Next = rows[limit-1].ID
	}
	for _, row := range rows {
		page.Turns = append(page.Turns, turnView(row.ID, row.Origin, row.Status, row.Reason.String, row.Content, row.Answer.String, row.CreatedAt, row.SettledAt, textLimit))
	}
	return page, nil
}

// readText returns one page of the text of a turn that a result or a read cut.
func (s *Service) readText(ctx context.Context, conv sqlc.Conversation, page engine.SessionPage, a engine.SessionReadArgs) (engine.SessionPage, error) {
	turn, err := s.q.GetTurn(ctx, a.Turn)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && turn.ConversationID != conv.ID) {
		return engine.SessionPage{}, fmt.Errorf("%w: turn is not a turn of this session", ErrSessionInvalid)
	}
	if err != nil {
		return engine.SessionPage{}, err
	}
	var text string
	switch a.Field {
	case "", "answer":
		text = turn.Answer.String
	case "input":
		text = turn.Content
	default:
		return engine.SessionPage{}, fmt.Errorf("%w: field is answer or input", ErrSessionInvalid)
	}
	runes := []rune(text)
	if a.Offset < 0 || a.Offset > len(runes) {
		return engine.SessionPage{}, fmt.Errorf("%w: offset is outside the text", ErrSessionInvalid)
	}
	end := min(a.Offset+textLimit, len(runes))
	page.Text, page.NextOffset, page.HasMore = string(runes[a.Offset:end]), end, end < len(runes)
	return page, nil
}

func cutRunes(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	return string([]rune(text)[:limit]), true
}

// turnOutcome names how a turn ended from its receipt.
func turnOutcome(status, reason string) string {
	switch status {
	case "pending":
		return "running"
	case "done":
		return "done"
	}
	switch reason {
	case "stopped", "aborted":
		return "stopped"
	case "withdrawn", "interrupted":
		return reason
	}
	return "failed"
}

func turnView(id, origin, status, reason, input, answer string, createdAt int64, settledAt sql.NullInt64, limit int) engine.SessionTurn {
	turn := engine.SessionTurn{Turn: id, At: createdAt, Origin: origin, Outcome: turnOutcome(status, reason)}
	if settledAt.Valid {
		turn.EndedAt = &settledAt.Int64
	}
	if status != "done" {
		turn.Reason = reason
	}
	var cut bool
	if turn.Input, cut = cutRunes(input, limit); cut {
		turn.Cut = append(turn.Cut, "input")
	}
	if turn.Answer, cut = cutRunes(answer, limit); cut {
		turn.Cut = append(turn.Cut, "answer")
	}
	return turn
}

// asciiLower lowercases ASCII letters only, as SQLite lower() does, so a match in SQL is a match here.
func asciiLower(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, text)
}

func (s *Service) ToolSessionSearch(ctx context.Context, r engine.TaskContext, a engine.SessionSearchArgs) ([]engine.SessionMatch, error) {
	caller, err := s.toolCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(asciiLower(a.Query))
	if len(words) == 0 || len(words) > maxSearchWords {
		return nil, fmt.Errorf("%w: the query has 1 to %d words", ErrSessionInvalid, maxSearchWords)
	}
	params := sqlc.SearchTurnsParams{AgentID: caller.AgentID, MaxRows: int64(cmp.Or(a.Limit, 20))}
	if a.Limit < 0 || a.Limit > 50 {
		return nil, fmt.Errorf("%w: limit is 1 to 50", ErrSessionInvalid)
	}
	for i, slot := range []*string{&params.W1, &params.W2, &params.W3, &params.W4, &params.W5, &params.W6} {
		if i < len(words) {
			*slot = words[i]
		}
	}
	if a.Session != "" {
		if params.SessionID, err = strconv.ParseInt(a.Session, 10, 64); err != nil || params.SessionID <= 0 {
			return nil, fmt.Errorf("%w: session is a session ID", ErrSessionInvalid)
		}
	}
	if a.Since != "" {
		since, err := time.Parse(time.RFC3339, a.Since)
		if err != nil {
			if since, err = time.Parse(time.DateOnly, a.Since); err != nil {
				return nil, fmt.Errorf("%w: since is an RFC 3339 time or a date", ErrSessionInvalid)
			}
		}
		params.Since = since.UnixMilli()
	}
	rows, err := s.q.SearchTurns(ctx, params)
	if err != nil {
		return nil, err
	}
	matches := make([]engine.SessionMatch, 0, len(rows))
	for _, row := range rows {
		match := engine.SessionMatch{Session: strconv.FormatInt(row.ConversationID, 10), Title: row.Title, Turn: row.ID, At: row.CreatedAt}
		match.Field, match.Snippet = firstMatch(words, row.Content, row.Answer.String)
		matches = append(matches, match)
	}
	return matches, nil
}

// firstMatch finds the first word in the input, then in the answer, and cuts a snippet around it.
func firstMatch(words []string, input, answer string) (field, snippet string) {
	for _, candidate := range []struct{ field, text string }{{"input", input}, {"answer", answer}} {
		lower := asciiLower(candidate.text)
		for _, word := range words {
			at := strings.Index(lower, word)
			if at < 0 {
				continue
			}
			runes := []rune(candidate.text)
			start := max(0, utf8.RuneCountInString(candidate.text[:at])-snippetLimit/3)
			end := min(len(runes), start+snippetLimit)
			snippet = string(runes[start:end])
			if start > 0 {
				snippet = "…" + snippet
			}
			if end < len(runes) {
				snippet += "…"
			}
			return candidate.field, snippet
		}
	}
	return "", ""
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
