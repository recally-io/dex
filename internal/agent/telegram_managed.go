package agent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/vault"
)

// Keep the account ID before the first colon for existing channel identity queries.
const telegramVaultMarker = "::vault:"

var telegramTokenPattern = regexp.MustCompile(`^[1-9][0-9]*:[A-Za-z0-9_-]+$`)

// TelegramManagedSetup contains only the state and link for the current step.
type TelegramManagedSetup struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	ExpiresAt int64  `json:"expiresAt"`
	URL       string `json:"url,omitempty"`
	Username  string `json:"username,omitempty"`
}

type telegramManager struct {
	client    *telegram.Manager
	id        int64
	username  string
	checkedAt time.Time
	stopPoll  context.CancelFunc
}

// ConfigureTelegramVault opens the service-only credential scope before channels start.
func (s *Service) ConfigureTelegramVault(ctx context.Context, store *vault.Store) error {
	s.telegramVault = store
	ref, err := s.q.TelegramManagerCredential(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	token, err := store.Credential(ref)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("the saved Telegram manager credential is missing")
	}
	s.manager = s.newTelegramManager(token)
	return nil
}

func (s *Service) newTelegramManager(token string) *telegramManager {
	account, _, _ := strings.Cut(token, ":")
	id, _ := strconv.ParseInt(account, 10, 64)
	return &telegramManager{id: id, client: telegram.NewManager(s.apiURL, token)}
}

// SetTelegramManager validates the dedicated manager before changing its credential.
func (s *Service) SetTelegramManager(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	var manager *telegramManager
	if token != "" {
		if !telegramTokenPattern.MatchString(token) {
			return fmt.Errorf("%w: enter a manager bot token from BotFather", ErrInvalid)
		}
		manager = s.newTelegramManager(token)
		username, err := manager.client.ManagementIdentity(ctx, manager.id)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		manager.username, manager.checkedAt = username, time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldRef, err := s.q.TelegramManagerCredential(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var ref string
	if manager != nil {
		if err := s.checkTelegramAccount(ctx, "", strconv.FormatInt(manager.id, 10)); err != nil {
			return err
		}
		ref = "telegram-manager-" + uuid.NewV7().String()
		if err := s.telegramVault.EditCredential(ref, &token); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if manager == nil {
		err = q.DeleteTelegramManagerCredential(ctx)
	} else {
		err = q.SetTelegramManagerCredential(ctx, ref)
	}
	if err != nil {
		return err
	}
	if err := q.CancelAllTelegramManagedSetups(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.manager != nil && s.manager.stopPoll != nil {
		s.manager.stopPoll()
	}
	s.manager = manager
	s.forgetTelegramCredential(oldRef)
	return nil
}

func (s *Service) managerStatus() (configured, available bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manager != nil, s.manager != nil && s.manager.username != ""
}

// TelegramManagerPoll returns the scheduler job for the dedicated account.
// The scheduler calls the job serially and joins it before channel shutdown.
func (s *Service) TelegramManagerPoll() func(context.Context) {
	var last *telegramManager
	var offset int64
	return func(ctx context.Context) {
		s.mu.Lock()
		manager := s.manager
		if manager == nil {
			s.mu.Unlock()
			last, offset = nil, 0
			return
		}
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		manager.stopPoll = cancel
		refresh := time.Since(manager.checkedAt) >= 30*time.Second
		s.mu.Unlock()
		if manager != last {
			last, offset = manager, 0
		}
		if refresh {
			_ = s.refreshTelegramManager(ctx, manager)
		}
		s.mu.Lock()
		ready := s.manager == manager && manager.username != ""
		s.mu.Unlock()
		if ready {
			var err error
			offset, err = manager.client.Poll(ctx, offset, func(id string, user int64) error {
				return s.pairManagedOwner(ctx, manager, id, user)
			}, func(update telegram.ManagedUpdate) error {
				return s.receiveManagedBot(ctx, manager, update)
			})
			if err != nil && ctx.Err() == nil {
				slog.Warn("telegram manager: poll failed", "err", err)
			}
		}
	}
}

func (s *Service) refreshTelegramManager(ctx context.Context, manager *telegramManager) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	username, err := manager.client.ManagementIdentity(ctx, manager.id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if manager != s.manager {
		return fmt.Errorf("%w: the Telegram manager changed", ErrInvalid)
	}
	manager.username, manager.checkedAt = username, time.Now()
	return err
}

func (s *Service) BeginTelegramManagedSetup(ctx context.Context, agentID string) (TelegramManagedSetup, error) {
	s.mu.Lock()
	manager := s.manager
	s.mu.Unlock()
	if manager == nil {
		return TelegramManagedSetup{}, fmt.Errorf("%w: configure a Telegram manager first", ErrInvalid)
	}
	if err := s.refreshTelegramManager(ctx, manager); err != nil {
		return TelegramManagedSetup{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.manager != manager || manager.username == "" {
		return TelegramManagedSetup{}, fmt.Errorf("%w: the Telegram manager is not available", ErrInvalid)
	}
	// The username is the only per-request value the newbot link can return in an update.
	// Keep its entropy separate from the private owner verification code.
	var secret [24]byte
	var suffix [12]byte
	_, _ = rand.Read(secret[:])
	_, _ = rand.Read(suffix[:])
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TelegramManagedSetup{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err = q.PruneTelegramManagedSetups(ctx, now.Add(-24*time.Hour).Unix()); err != nil {
		return TelegramManagedSetup{}, err
	}
	if err = db.RequireActive(ctx, q, agentID); err != nil {
		return TelegramManagedSetup{}, err
	}
	if err = q.CancelAgentTelegramManagedSetups(ctx, agentID); err != nil {
		return TelegramManagedSetup{}, err
	}
	row, err := q.CreateTelegramManagedSetup(ctx, sqlc.CreateTelegramManagedSetupParams{
		ID: hex.EncodeToString(secret[:]), AgentID: agentID, ManagerID: manager.id,
		Username: "dex_" + hex.EncodeToString(suffix[:]) + "_bot", ExpiresAt: now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return TelegramManagedSetup{}, err
	}
	if err = tx.Commit(); err != nil {
		return TelegramManagedSetup{}, err
	}
	return s.managedSetupView(row), nil
}

func (s *Service) GetTelegramManagedSetup(ctx context.Context, agentID, id string) (TelegramManagedSetup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.q.GetTelegramManagedSetup(ctx, sqlc.GetTelegramManagedSetupParams{AgentID: agentID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return TelegramManagedSetup{}, ErrNotFound
	}
	if err != nil {
		return TelegramManagedSetup{}, err
	}
	return s.managedSetupView(row), nil
}

func (s *Service) CancelTelegramManagedSetup(ctx context.Context, agentID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.q.CancelTelegramManagedSetup(ctx, sqlc.CancelTelegramManagedSetupParams{AgentID: agentID, ID: id})
}

// managedSetupView requires mu.
func (s *Service) managedSetupView(row sqlc.TelegramManagedSetup) TelegramManagedSetup {
	view := TelegramManagedSetup{ID: row.ID, State: row.State, ExpiresAt: row.ExpiresAt}
	if view.State != "pending" {
		return view
	}
	if time.Now().Unix() >= row.ExpiresAt {
		view.State = "expired"
		return view
	}
	view.State = "verify_owner"
	if row.OwnerID != 0 {
		view.State, view.Username = "create_bot", row.Username
	}
	if s.manager == nil || s.manager.id != row.ManagerID || s.manager.username == "" {
		return view
	}
	managerName := s.manager.username
	if row.OwnerID == 0 {
		view.URL = "https://t.me/" + managerName + "?start=" + row.ID
	} else {
		view.URL = "https://t.me/newbot/" + managerName + "/" + row.Username + "?name=" + url.QueryEscape("Dex Bot")
	}
	return view
}

func (s *Service) pairManagedOwner(ctx context.Context, manager *telegramManager, id string, owner int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if manager != s.manager || manager.username == "" {
		return nil
	}
	return s.q.PairTelegramManagedOwner(ctx, sqlc.PairTelegramManagedOwnerParams{
		ID: id, ManagerID: manager.id, OwnerID: owner, Now: time.Now().Unix(),
	})
}

func (s *Service) receiveManagedBot(ctx context.Context, manager *telegramManager, u telegram.ManagedUpdate) error {
	if u.User.ID <= 0 || u.User.IsBot || u.Bot.ID <= 0 || !u.Bot.IsBot || u.Bot.ID == manager.id {
		return nil
	}
	s.mu.Lock()
	if manager != s.manager || manager.username == "" {
		s.mu.Unlock()
		return nil
	}
	row, err := s.q.ClaimTelegramManagedBot(ctx, sqlc.ClaimTelegramManagedBotParams{
		Username: strings.ToLower(u.Bot.Username), ManagerID: manager.id, OwnerID: u.User.ID,
		BotID: sql.NullInt64{Int64: u.Bot.ID, Valid: true}, Now: time.Now().Unix(),
	})
	s.mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	token, err := manager.client.ManagedToken(ctx, u.Bot.ID)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	account := strconv.FormatInt(u.Bot.ID, 10)
	if err != nil || !telegramTokenPattern.MatchString(token) || !strings.HasPrefix(token, account+":") {
		return s.failManagedSetup(ctx, row.ID)
	}
	if err := telegram.ValidateManagedToken(ctx, s.apiURL, token, u.Bot.ID); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return s.failManagedSetup(ctx, row.ID)
	}
	return s.connectManagedBot(ctx, manager, row, account, token)
}

func (s *Service) failManagedSetup(ctx context.Context, id string) error {
	_, err := s.q.FinishTelegramManagedSetup(ctx, sqlc.FinishTelegramManagedSetupParams{ID: id, State: "failed", Now: time.Now().Unix()})
	return err
}

// connectManagedBot does not stop the existing bot until the credential and settings are saved.
func (s *Service) connectManagedBot(ctx context.Context, manager *telegramManager, row sqlc.TelegramManagedSetup, account, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if manager != s.manager || manager.username == "" {
		return s.failManagedSetup(ctx, row.ID)
	}
	current, err := s.q.GetTelegramManagedSetup(ctx, sqlc.GetTelegramManagedSetupParams{ID: row.ID, AgentID: row.AgentID})
	if err != nil || current.State != "pending" || current.ExpiresAt <= time.Now().Unix() {
		return err
	}
	if err := s.checkTelegramAccount(ctx, row.AgentID, account); err != nil {
		if errors.Is(err, ErrCredentialInUse) {
			return s.failManagedSetup(ctx, row.ID)
		}
		return err
	}
	saved, err := s.q.GetTelegramSettings(ctx, row.AgentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	options := saved.Options
	if options == "" {
		options = "{}"
	}
	ref := account + telegramVaultMarker + row.ID
	cfg := telegram.Config{Token: token, CredentialRef: ref, APIURL: s.apiURL, AllowedUsers: []int64{row.OwnerID}, Pair: s.pairTelegram(row.AgentID, ref)}
	if err := json.Unmarshal([]byte(options), &cfg.Options); err != nil {
		return err
	}
	if err := s.telegramVault.EditCredential(ref, &token); err != nil {
		return s.failManagedSetup(ctx, row.ID)
	}
	// Vault writes are atomic but cannot join SQLite transactions. A crash before commit can
	// leave an unused vault entry; a committed channel always has its credential.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	changed, err := q.FinishTelegramManagedSetup(ctx, sqlc.FinishTelegramManagedSetupParams{ID: row.ID, State: "connected", Now: time.Now().Unix()})
	if err != nil || changed == 0 {
		return err
	}
	if err := db.RequireActive(ctx, q, row.AgentID); err != nil {
		if errors.Is(err, db.ErrAgentArchived) {
			_ = tx.Rollback()
			return s.failManagedSetup(ctx, row.ID)
		}
		return err
	}
	cfg.RetryAt, err = q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: row.AgentID, Token: ref, AllowedUsers: strconv.FormatInt(row.OwnerID, 10), Options: options})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.onTelegram(row.AgentID, nil)
	s.onTelegram(row.AgentID, &cfg)
	if strings.Contains(saved.Token, telegramVaultMarker) {
		s.forgetTelegramCredential(saved.Token)
	}
	return nil
}

func (s *Service) checkTelegramAccount(ctx context.Context, agentID, account string) error {
	owner, err := s.q.TelegramAccountOwner(ctx, sqlc.TelegramAccountOwnerParams{AccountID: account, ExceptAgent: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: telegram is used by bot %q (%s)", ErrCredentialInUse, owner.Name, owner.ID)
}

func (s *Service) forgetTelegramCredential(ref string) {
	if ref != "" {
		if err := s.telegramVault.EditCredential(ref, nil); err != nil {
			slog.Warn("telegram: could not remove unused vault credential")
		}
	}
}
