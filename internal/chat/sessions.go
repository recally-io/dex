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

func sessionRef(c sqlc.Conversation, bot string, pending int64) engine.SessionRef {
	state := "idle"
	if pending > 0 {
		state = "running"
	}
	return engine.SessionRef{ID: strconv.FormatInt(c.ID, 10), Title: c.Title, State: state, Bot: bot, BotID: c.AgentID}
}

func (s *Service) ref(ctx context.Context, q *sqlc.Queries, c sqlc.Conversation) (engine.SessionRef, error) {
	pending, err := q.PendingCount(ctx, c.ID)
	if err != nil {
		return engine.SessionRef{}, err
	}
	bot, err := q.GetAgent(ctx, c.AgentID)
	return sessionRef(c, bot.Name, pending), err
}

// childOf returns the child of parent that the model named. The ownership rule is the parent
// link for private callers. Group callers also require the same bot and audience.
func (s *Service) childOf(ctx context.Context, q *sqlc.Queries, parent sqlc.Conversation, id string) (sqlc.Conversation, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return sqlc.Conversation{}, ErrSessionNotFound
	}
	c, err := q.GetConversation(ctx, n)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!c.ParentID.Valid || c.ParentID.Int64 != parent.ID || parent.ExecutionScope != "" && (c.AgentID != parent.AgentID || c.ExecutionScope != parent.ExecutionScope || c.ScopeContext != parent.ScopeContext))) {
		return c, ErrSessionNotFound
	}
	return c, err
}

var errRepeatedTurn = errors.New("a call with this key stored its turn already")

// resolveBot finds the bot that the model named by ID or by name. A name is not unique, so an
// ambiguous name is an error that lists the IDs.
func resolveBot(ctx context.Context, q *sqlc.Queries, name string) (sqlc.Agent, error) {
	bot, err := q.GetAgent(ctx, name)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return bot, err
	}
	bots, err := q.ListAgents(ctx)
	if err != nil {
		return bot, err
	}
	var named []sqlc.Agent
	for _, b := range bots {
		if strings.EqualFold(b.Name, name) {
			named = append(named, b)
		}
	}
	switch len(named) {
	case 0:
		return bot, fmt.Errorf("%w: no bot has the ID or the name %q", errSessionInvalid, name)
	case 1:
		return named[0], nil
	}
	ids := make([]string, len(named))
	for i, b := range named {
		ids[i] = b.ID
	}
	return bot, fmt.Errorf("%w: %d bots have the name %q, use one of the IDs %s", errSessionInvalid, len(named), name, strings.Join(ids, ", "))
}

// allBots is the value of the bot argument of session_list and session_search for every bot.
const allBots = "all"

// botFilter returns the bots that a list or a search covers: the caller's bot when the argument is
// empty, every bot for "all", or the one that the argument names.
func botFilter(ctx context.Context, q *sqlc.Queries, current, name string) (agentID string, all bool, err error) {
	switch name = strings.TrimSpace(name); name {
	case "":
		return current, false, nil
	case allBots:
		return "", true, nil
	}
	bot, err := resolveBot(ctx, q, name)
	return bot.ID, false, err
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
	if c.ParentID.Int64 != parent.ID || parent.ExecutionScope != "" && (c.AgentID != parent.AgentID || c.ExecutionScope != parent.ExecutionScope || c.ScopeContext != parent.ScopeContext) {
		return c, false, ErrSessionNotFound
	}
	return c, true, nil
}

func checkMessage(message string) error {
	if strings.TrimSpace(message) == "" || len(message) > maxMessageBytes {
		return fmt.Errorf("%w: the message has 1 to %d bytes", errSessionInvalid, maxMessageBytes)
	}
	return nil
}

func (s *Service) ToolSessionNew(ctx context.Context, r engine.TaskContext, a engine.SessionNewArgs) (engine.SessionRef, error) {
	if r.Scope != "" {
		return engine.SessionRef{}, errSessionInvalid
	}
	return s.newSession(ctx, r, a)
}

func (s *Service) newSession(ctx context.Context, r engine.TaskContext, a engine.SessionNewArgs) (engine.SessionRef, error) {
	if r.Scope != "" && (a.Bot != "" || a.Cwd != "" || a.Environment != "" || a.Provider != "" || a.ModelID != "" || a.ThinkingLevel != "" || !strings.HasPrefix(r.Key, r.AgentID+"/"+r.Scope+"/")) {
		return engine.SessionRef{}, errSessionInvalid
	}
	parent, err := s.sessionParent(ctx, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	if strings.TrimSpace(a.Title) == "" || len(a.Title) > maxTitleBytes || !strings.HasPrefix(r.Key, r.AgentID+"/") {
		return engine.SessionRef{}, fmt.Errorf("%w: the title has 1 to %d bytes and the call needs a durable key", errSessionInvalid, maxTitleBytes)
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
	target, err := s.q.GetAgent(ctx, r.AgentID)
	if err != nil {
		return engine.SessionRef{}, err
	}
	if name := strings.TrimSpace(a.Bot); name != "" {
		if target, err = resolveBot(ctx, s.q, name); err != nil {
			return engine.SessionRef{}, err
		}
	}
	// A child in the same bot starts from the settings and the project of its parent. A child in
	// another bot starts from the defaults and the workspace of that bot.
	inherited := parent
	if target.ID != r.AgentID {
		inherited = sqlc.Conversation{}
	}
	settings := Settings{Provider: cmp.Or(inherited.ModelProvider, target.ModelProvider), ModelID: cmp.Or(inherited.ModelID, target.ModelID), ThinkingLevel: cmp.Or(a.ThinkingLevel, inherited.ThinkingLevel, target.ThinkingLevel)}
	if a.Provider != "" || a.ModelID != "" {
		if a.Provider == "" || a.ModelID == "" {
			return engine.SessionRef{}, fmt.Errorf("%w: give provider and modelId together", errSessionInvalid)
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
	// A child in the same bot runs in the environment of its parent unless it selects another one;
	// a child in another bot runs in the environment of that bot. The cwd of the parent stays in
	// the environment of the parent.
	environment := inherited.EnvironmentID
	if a.Environment != "" {
		if err := checkEnvironment(ctx, s.q, a.Environment); err != nil {
			return engine.SessionRef{}, err
		}
		environment = a.Environment
	}
	cwd := inherited.Cwd
	if source, err := s.q.GetAgent(ctx, r.AgentID); err != nil {
		return engine.SessionRef{}, err
	} else if effectiveEnvironment(target, environment) != effectiveEnvironment(source, parent.EnvironmentID) {
		cwd = ""
	}
	if a.Cwd != "" {
		if cwd, err = s.resolveProjectPath(ctx, target.ID, environment, a.Cwd); err != nil {
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
	source, err := sessionSource(ctx, q, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	now := s.now().UnixMilli()
	child, err := q.CreateChildConversation(ctx, sqlc.CreateChildConversationParams{
		AgentID: target.ID, ChannelKey: chatPrefix + uuid.NewV7().String(), Title: a.Title,
		ModelProvider: settings.Provider, ModelID: settings.ModelID, ThinkingLevel: settings.ThinkingLevel, Cwd: cwd, EnvironmentID: environment,
		ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}, CreatedAt: now, ExecutionScope: parent.ExecutionScope, ScopeContext: parent.ScopeContext,
	})
	if err != nil {
		return engine.SessionRef{}, err
	}
	if _, err := q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turn, ConversationID: child.ID, Content: a.Message, WhenBusy: "followUp", ParentRequestID: sql.NullString{String: r.SourceRequestID, Valid: true}, CreatedAt: now}); err != nil {
		return engine.SessionRef{}, err
	}
	if a.FollowUp {
		if err := createChannelFollowUp(ctx, q, parent, source, turn); err != nil {
			return engine.SessionRef{}, err
		}
	}
	// Work for a Task starts here, so the first child of a Task input becomes its primary Session.
	if taskID, err := workTaskForInput(ctx, q, source.ID); err != nil {
		return engine.SessionRef{}, err
	} else if taskID != "" {
		if err := q.LinkFirstWorkTaskSession(ctx, sqlc.LinkFirstWorkTaskSessionParams{TaskID: taskID, ConversationID: child.ID}); err != nil {
			return engine.SessionRef{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return engine.SessionRef{}, err
	}
	s.signal(child.ID)
	s.signal(parent.ID)
	s.Wake()
	return sessionRef(child, target.Name, 1), nil
}

func (s *Service) ToolSessionSend(ctx context.Context, r engine.TaskContext, a engine.SessionSendArgs) (engine.SessionRef, error) {
	if r.Scope != "" {
		return engine.SessionRef{}, errSessionInvalid
	}
	return s.sendSession(ctx, r, a)
}

func (s *Service) sendSession(ctx context.Context, r engine.TaskContext, a engine.SessionSendArgs) (engine.SessionRef, error) {
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
		return engine.SessionRef{}, fmt.Errorf("%w: mode is queue or steer", errSessionInvalid)
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
	source, err := sessionSource(ctx, q, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	if _, err := q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turn, ConversationID: child.ID, Content: a.Message, WhenBusy: whenBusy, ParentRequestID: sql.NullString{String: r.SourceRequestID, Valid: true}, CreatedAt: s.now().UnixMilli()}); err != nil {
		return engine.SessionRef{}, err
	}
	if a.FollowUp {
		if err := createChannelFollowUp(ctx, q, parent, source, turn); err != nil {
			return engine.SessionRef{}, err
		}
	}
	bot, err := q.GetAgent(ctx, child.AgentID)
	if err != nil {
		return engine.SessionRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.SessionRef{}, err
	}
	s.signal(child.ID)
	s.signal(parent.ID)
	s.Wake()
	return sessionRef(child, bot.Name, 1), nil
}

func (s *Service) ToolSessionStop(ctx context.Context, r engine.TaskContext, id string) (engine.SessionRef, error) {
	if r.Scope != "" {
		return engine.SessionRef{}, errSessionInvalid
	}
	return s.stopSession(ctx, r, id)
}

func (s *Service) stopSession(ctx context.Context, r engine.TaskContext, id string) (engine.SessionRef, error) {
	parent, err := s.sessionParent(ctx, r)
	if err != nil {
		return engine.SessionRef{}, err
	}
	child, err := s.childOf(ctx, s.q, parent, id)
	if err != nil {
		return engine.SessionRef{}, err
	}
	stored, err := s.stop(ctx, child.AgentID, child.ID, nil, &stopCall{task: r, parent: parent.ID, child: child}, false)
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
	if r.Scope != "" {
		return engine.SessionRef{}, errSessionInvalid
	}
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
	if a.Environment != "" {
		if err := checkEnvironment(ctx, s.q, a.Environment); err != nil {
			return engine.SessionRef{}, err
		}
	}
	fork, err := s.fork(ctx, child, a.Entry, false, cmp.Or(a.Environment, child.EnvironmentID), func(ctx context.Context, q *sqlc.Queries, fork sqlc.Conversation) error {
		if _, err := sessionSource(ctx, q, r); err != nil {
			return err
		}
		created, err := q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turn, ConversationID: fork.ID, Content: a.Message, WhenBusy: "followUp", ParentRequestID: sql.NullString{String: r.SourceRequestID, Valid: true}, CreatedAt: s.now().UnixMilli()})
		if err == nil && created == 0 {
			// A concurrent call with the same key stored its fork first. Roll this fork back.
			return errRepeatedTurn
		}
		return err
	})
	if errors.Is(err, errRepeatedTurn) {
		c, ok, err := repeatedTurn(ctx, s.q, turn, parent)
		if err != nil || !ok {
			return engine.SessionRef{}, cmp.Or(err, ErrSessionNotFound)
		}
		return s.ref(ctx, s.q, c)
	}
	if err != nil {
		return engine.SessionRef{}, err
	}
	s.signal(parent.ID)
	s.Wake()
	return s.ref(ctx, s.q, fork)
}

// readable returns a session that a tool may read: any session of any bot except a dream.
func (s *Service) readable(ctx context.Context, id string) (sqlc.Conversation, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return sqlc.Conversation{}, ErrSessionNotFound
	}
	c, err := s.q.GetConversation(ctx, n)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.HasPrefix(c.ChannelKey, dream.Channel)) {
		return c, ErrSessionNotFound
	}
	return c, err
}

func (s *Service) ToolSessionList(ctx context.Context, r engine.TaskContext, a engine.SessionListArgs) ([]engine.SessionInfo, error) {
	caller, err := s.toolCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	switch a.Scope {
	case "", "children", "all":
	default:
		return nil, fmt.Errorf("%w: scope is children or all", errSessionInvalid)
	}
	agentID, all, err := botFilter(ctx, s.q, caller.AgentID, a.Bot)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListSessions(ctx, sqlc.ListSessionsParams{AgentID: agentID, AllBots: boolInt(all), AllSessions: boolInt(a.Scope == "all"), ParentID: sql.NullInt64{Int64: caller.ID, Valid: true}, MaxRows: listLimit})
	if err != nil {
		return nil, err
	}
	out := make([]engine.SessionInfo, 0, len(rows))
	for _, row := range rows {
		info := engine.SessionInfo{
			SessionRef: sessionRef(sqlc.Conversation{ID: row.ID, Title: row.Title, AgentID: row.AgentID}, row.AgentName, row.Pending),
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
	if _, err := s.toolCaller(ctx, r); err != nil {
		return engine.SessionPage{}, err
	}
	conv, err := s.readable(ctx, a.ID)
	if err != nil {
		return engine.SessionPage{}, err
	}
	return s.readSession(ctx, conv, a)
}

func (s *Service) readSession(ctx context.Context, conv sqlc.Conversation, a engine.SessionReadArgs) (engine.SessionPage, error) {
	ref, err := s.ref(ctx, s.q, conv)
	if err != nil {
		return engine.SessionPage{}, err
	}
	page := engine.SessionPage{SessionRef: ref}
	if ref.State == "running" {
		page.Activity = &engine.SessionActivity{}
		if conv.ExecutionID.Valid {
			key := engine.Ref{AgentID: conv.AgentID, ID: conv.ExecutionID.Int64, Scope: conv.ExecutionScope}
			if activity, ok := s.engine.Activity([]engine.Ref{key})[key]; ok {
				page.Activity.Available = true
				page.Activity.LastObservedAt = activity.LastActivityAt
				page.Activity.RunningTools = activity.RunningTools
			}
		}
	}
	if a.Turn != "" {
		return s.readText(ctx, conv, page, a)
	}
	limit := cmp.Or(a.Limit, 10)
	if limit < 1 || limit > 50 {
		return engine.SessionPage{}, fmt.Errorf("%w: limit is 1 to 50", errSessionInvalid)
	}
	if a.Before != "" {
		before, err := s.q.GetTurn(ctx, a.Before)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && before.ConversationID != conv.ID) {
			return engine.SessionPage{}, fmt.Errorf("%w: before is not a turn of this session", errSessionInvalid)
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
		return engine.SessionPage{}, fmt.Errorf("%w: turn is not a turn of this session", errSessionInvalid)
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
		return engine.SessionPage{}, fmt.Errorf("%w: field is answer or input", errSessionInvalid)
	}
	runes := []rune(text)
	if a.Offset < 0 || a.Offset > len(runes) {
		return engine.SessionPage{}, fmt.Errorf("%w: offset is outside the text", errSessionInvalid)
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
		return nil, fmt.Errorf("%w: the query has 1 to %d words", errSessionInvalid, maxSearchWords)
	}
	if a.Limit < 0 || a.Limit > 50 {
		return nil, fmt.Errorf("%w: limit is 1 to 50", errSessionInvalid)
	}
	agentID, all, err := botFilter(ctx, s.q, caller.AgentID, a.Bot)
	if err != nil {
		return nil, err
	}
	params := sqlc.SearchTurnsParams{AgentID: agentID, AllBots: boolInt(all), ExcludedTurnID: r.SourceRequestID, MaxRows: int64(cmp.Or(a.Limit, 20))}
	for i, slot := range []*string{&params.W1, &params.W2, &params.W3, &params.W4, &params.W5, &params.W6} {
		if i < len(words) {
			*slot = words[i]
		}
	}
	switch a.Session {
	case "":
		params.SessionID = caller.ID
	case "all":
		// The bot filter still applies when there is no session filter.
	default:
		if params.SessionID, err = strconv.ParseInt(a.Session, 10, 64); err != nil || params.SessionID <= 0 {
			return nil, fmt.Errorf("%w: session must be a session ID or all", errSessionInvalid)
		}
	}
	if a.Since != "" {
		since, err := time.Parse(time.RFC3339, a.Since)
		if err != nil {
			if since, err = time.Parse(time.DateOnly, a.Since); err != nil {
				return nil, fmt.Errorf("%w: since is an RFC 3339 time or a date", errSessionInvalid)
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
		match := engine.SessionMatch{Session: strconv.FormatInt(row.ConversationID, 10), Title: row.Title, Bot: row.AgentName, BotID: row.AgentID, Turn: row.ID, At: row.CreatedAt}
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

// ChatOfTurn returns the session that holds input id, or ErrNotFound. The ID is the input ID of a
// turn: a result names its child session turn, and a task from before child sessions used the ID
// of its first input as its own ID. The session belongs to agentID, or is a child of a session of
// agentID in another bot. The child names its bot in AgentID.
func (s *Service) ChatOfTurn(ctx context.Context, agentID, id string) (Chat, error) {
	turn, err := s.q.GetSubmission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	conv, err := s.q.GetConversation(ctx, turn.ConversationID)
	if err != nil {
		return Chat{}, err
	}
	if conv.AgentID == agentID || !conv.ParentID.Valid {
		return s.Get(ctx, agentID, turn.ConversationID)
	}
	parent, err := s.q.GetConversation(ctx, conv.ParentID.Int64)
	if err != nil || parent.AgentID != agentID {
		return Chat{}, ErrNotFound
	}
	c, err := s.Get(ctx, conv.AgentID, conv.ID)
	if err != nil {
		return Chat{}, err
	}
	agent, err := s.q.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return Chat{}, err
	}
	c.AgentID, c.AgentName = agent.ID, agent.Name
	return c, nil
}
