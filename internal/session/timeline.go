package session

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

const MainKey = "main"

// Keep the stored channel key prefix for existing Web sessions.
const sessionPrefix = "chat:"

func Writable(key string) bool {
	return key == MainKey || strings.HasPrefix(key, sessionPrefix)
}

// Create reuses an empty Session without opening an execution context or requiring a model.
func (s *Service) Create(ctx context.Context, botID string) (Session, error) {
	c, err := s.q.EnsureEmptySession(ctx, sqlc.EnsureEmptySessionParams{
		BotID: botID, ChannelKey: sessionPrefix + uuid.NewV7().String(), CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return Session{}, err
	}
	return s.Get(ctx, botID, c.ID)
}

// ErrNotArchivable is an archive request for Main or a read-only kind.
var ErrNotArchivable = errors.New("only separate Web sessions and child sessions can be archived; Main always stays")

// SetArchived archives or restores a session. It changes only the list state: messages,
// records, files and running work stay, and a child session result still returns to the session.
func (s *Service) SetArchived(ctx context.Context, botID string, id int64, archived bool) (Session, error) {
	c, err := s.Get(ctx, botID, id)
	if err != nil {
		return Session{}, err
	}
	if !strings.HasPrefix(c.ChannelKey, sessionPrefix) && c.ParentID == 0 {
		return Session{}, ErrNotArchivable
	}
	if archived {
		err = s.archive(ctx, botID, id)
	} else {
		err = s.q.UnarchiveSession(ctx, sqlc.UnarchiveSessionParams{ID: id, BotID: botID})
	}
	if err != nil {
		return Session{}, err
	}
	return s.Get(ctx, botID, id)
}

// archive also dismisses the session's question: nobody sees it any more. It takes the same writer
// as ask_user, so a question cannot open after the archive commits.
func (s *Service) archive(ctx context.Context, botID string, id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UnixMilli()
	if err := q.ArchiveSession(ctx, sqlc.ArchiveSessionParams{ArchivedAt: sql.NullInt64{Int64: now, Valid: true}, ID: id, BotID: botID}); err != nil {
		return err
	}
	if err := q.DismissQuestions(ctx, sqlc.DismissQuestionsParams{SessionID: id, Now: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrSessionBusy = errors.New("the timeline is busy; wait for its replies before clearing context")
var ErrResetID = errors.New("the reset ID belongs to another input")

// ExistingMain reads the main conversation without creating one.
func (s *Service) ExistingMain(ctx context.Context, botID string) (*Session, error) {
	row, err := s.q.GetSessionByKey(ctx, sqlc.GetSessionByKeyParams{BotID: botID, ChannelKey: MainKey})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	main, err := s.Get(ctx, botID, row.ID)
	if err != nil {
		return nil, err
	}
	return &main, nil
}

// Main creates only the control record. The execution conversation opens on first use.
func (s *Service) Main(ctx context.Context, botID string) (Session, error) {
	bot, err := s.q.GetBot(ctx, botID)
	if err != nil {
		return Session{}, err
	}
	c, err := s.q.EnsureSession(ctx, sqlc.EnsureSessionParams{
		BotID: botID, ChannelKey: MainKey, Title: bot.Name, CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return Session{}, err
	}
	return s.Get(ctx, botID, c.ID)
}

// Clear stores a reset only when both owners are idle. Delivery cannot pass this check. A
// handoff is the first message of the new context; it does not start a run.
func (s *Service) Clear(ctx context.Context, botID string, sessionID int64, requestID, origin, handoff string) error {
	unlock, err := s.lockSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := s.Get(ctx, botID, sessionID)
	if err != nil {
		return err
	}
	if !c.Writable {
		return ErrNotFound
	}
	if sub, err := s.q.GetSubmission(ctx, requestID); err == nil {
		if sub.SessionID != sessionID || sub.Kind != "context_reset" || sub.Content != handoff {
			return ErrResetID
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if c.Busy || s.leaseUntil.Load() != 0 {
		return ErrSessionBusy
	}
	if c.ExecutionID != 0 {
		if err := s.engine.CheckIdle(ctx, engine.Ref{BotID: botID, ID: c.ExecutionID}); err != nil {
			if errors.Is(err, engine.ErrBusy) {
				return ErrSessionBusy
			}
			return err
		}
	}
	err = func() error {
		s.admissionMu.RLock()
		defer s.admissionMu.RUnlock()
		if s.leaseUntil.Load() != 0 {
			return ErrSessionBusy
		}
		// Take the writer before reading. Input and session result inserts use the same SQLite writer.
		// Native calls run after this transaction so settlement can acquire the writer.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, "UPDATE submissions SET id = id WHERE 0"); err != nil {
			return err
		}
		q := s.q.WithTx(tx)
		if err := checkTelegramControl(ctx, q, strings.TrimPrefix(requestID, "channel:"), botID, sessionID); err != nil {
			return err
		}
		if archived, err := q.SessionBotArchived(ctx, sessionID); err != nil || archived != 0 {
			return cmp.Or(err, db.ErrBotArchived)
		}
		pending, err := q.CountPendingSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if pending != 0 {
			return ErrSessionBusy
		}
		producer, replyChannel, replyPeer := inboxIdentity(origin)
		created, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
			ID: requestID, SessionID: sessionID, Content: handoff, Attachments: "null", Origin: origin, Producer: producer, ReplyChannel: replyChannel, ReplyPeer: replyPeer, Kind: "context_reset", CreatedAt: s.now().UnixMilli(),
		})
		if err != nil {
			return err
		}
		if created == 0 {
			return ErrResetID
		}
		if err := q.DismissQuestions(ctx, sqlc.DismissQuestionsParams{SessionID: sessionID, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}}); err != nil {
			return err
		}
		if _, err := q.CancelMonitorsBySession(ctx, sqlc.CancelMonitorsBySessionParams{
			SessionID: sessionID,
			UpdatedAt: s.now().UnixMilli(),
		}); err != nil {
			return err
		}
		if c.ChannelKey == MainKey {
			if err := q.DiscardTelegramInputEffects(ctx, botID); err != nil {
				return err
			}
			if err := q.DiscardPrivateTelegramAlbums(ctx, botID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		return err
	}

	s.signal(sessionID)
	if c.ChannelKey == MainKey && s.notify != nil {
		s.notify(MainKey)
	}
	s.deliverSession(ctx, sessionID, requestID)
	return nil
}

func inputOrigin(key string) string {
	switch {
	case Writable(key):
		return "web"
	case strings.HasPrefix(key, "automation:"):
		return "automation"
	case strings.HasPrefix(key, "dream:"):
		return "dream"
	default:
		peer, _, _ := strings.Cut(key, "/")
		return peer
	}
}

// ErrSettingsForbidden is a settings change for a session that takes no input and is not an automation.
var ErrSettingsForbidden = errors.New("this session is read-only; its model cannot change")

// SetSession replaces the settings that the session overrides. The next message of the session uses them.
func (s *Service) SetSession(ctx context.Context, botID string, sessionID int64, c Settings) error {
	// One rule with SetProject: a session that accepts input accepts settings. Automation sessions
	// take no input but keep their own model for the next run.
	session, err := s.Get(ctx, botID, sessionID)
	if err != nil {
		return err
	}
	if !session.Writable && !strings.HasPrefix(session.ChannelKey, "automation:") {
		return ErrSettingsForbidden
	}
	if s.checkSettings != nil {
		if err := s.checkSettings(ctx, c); err != nil {
			return err
		}
	}
	return s.q.SetSessionSettings(ctx, sqlc.SetSessionSettingsParams{
		ModelProvider: c.Provider, ModelID: c.ModelID, ThinkingLevel: c.ThinkingLevel, ID: sessionID, BotID: botID,
	})
}
