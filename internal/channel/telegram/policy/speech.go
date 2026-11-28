package policy

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/speech"
	"github.com/recally-io/dex/internal/upload"
)

// SpeechInput restores an unacknowledged upload. A credential move cannot transfer its ownership.
func (p *Policy) SpeechInput(ctx context.Context, source string) (*session.Message, bool, error) {
	row, err := p.q.GetTelegramEffect(ctx, "stt:"+source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if row.BotID != p.botID || row.State == "discarded" {
		return nil, true, nil
	}
	if row.InputJson == "{}" {
		return nil, false, nil
	}
	var msg session.Message
	if err := json.Unmarshal([]byte(row.InputJson), &msg); err != nil {
		return nil, false, err
	}
	return &msg, false, nil
}

func (p *Policy) Transcribe(ctx context.Context, peer, source string, msg session.Message, duration int, options speech.Options, resolve speech.Resolve, store *upload.Store) (session.Message, error) {
	key := "stt:" + source
	msg.SpeechSource = key
	row, err := p.q.GetTelegramEffect(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		input, marshalErr := json.Marshal(msg)
		if marshalErr != nil {
			return msg, marshalErr
		}
		n, createErr := p.q.CreateTelegramEffect(ctx, sqlc.CreateTelegramEffectParams{OperationKey: key, BotID: p.botID, AccountID: msg.ChannelContext.AccountID, SenderID: msg.ChannelContext.SenderID, Peer: peer, SourceKey: source, Kind: "stt", InputJson: string(input), CreatedAt: time.Now().UnixMilli()})
		if createErr != nil {
			return msg, createErr
		}
		if n != 1 {
			return msg, errors.New("speech source authorization changed")
		}
		row, err = p.q.GetTelegramEffect(ctx, key)
	}
	if err != nil {
		return msg, err
	}
	if row.BotID != p.botID || row.State == "discarded" {
		return msg, errors.New("speech input is no longer authorized")
	}
	if row.State == "processing" {
		if err := p.q.MarkTelegramEffectUnknown(ctx, key); err != nil {
			return msg, err
		}
		row.State = "unknown"
		row.Error = "transcription outcome unknown; not sent again"
	}
	if row.State == "ready" {
		n, err := p.q.StartTelegramEffect(ctx, key)
		if err != nil {
			return msg, err
		}
		if n != 1 {
			return msg, errors.New("speech input already claimed")
		}
		options = options.Budgets()
		callCtx, callCancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
		var transcript string
		switch {
		case !options.Enabled || !options.AllowSending:
			err = errors.New("transcription is off; original audio kept")
		case len(msg.Attachments) != 1 || msg.Attachments[0].Size > options.MaxBytes || duration > options.MaxDurationSeconds:
			err = errors.New("audio exceeds the configured speech byte or duration budget")
		default:
			var endpoint speech.Endpoint
			if resolve == nil {
				err = errors.New("speech provider resolver is not configured")
			} else {
				endpoint, err = resolve(callCtx, options.Provider)
			}
			if err == nil {
				var fileName string
				file, attachment, openErr := store.Open(msg.Attachments[0].ID)
				if openErr != nil {
					err = openErr
				} else {
					fileName = attachment.Name
					transcript, err = speech.Transcribe(callCtx, endpoint, options, fileName, file)
					_ = file.Close()
				}
			}
		}
		callCancel()
		row.State = "done"
		row.Text = transcript
		row.Error = ""
		if err != nil {
			row.State = speechFailure(ctx, err)
			row.Error = err.Error()
		}
		if err := p.completeEffect(ctx, sqlc.CompleteTelegramEffectParams{OperationKey: key, State: row.State, Text: row.Text, Error: row.Error, ResultJson: "{}"}, "speech input is no longer active"); err != nil {
			return msg, err
		}
		if ctx.Err() != nil {
			return msg, ctx.Err()
		}
	}
	msg.ChannelNote = strings.ReplaceAll(msg.ChannelNote, "Automatic transcription is not configured. ", "")
	if row.State == "done" {
		if strings.TrimSpace(msg.Content) == "" {
			msg.Content = row.Text
		} else {
			msg.ChannelNote += "\nTranscription: " + row.Text
		}
		msg.ChannelNote += "\nAudio was transcribed by the explicitly configured provider."
	} else {
		msg.ChannelNote += "\nTranscription unavailable: " + row.Error + ". The original audio is attached; no automatic provider retry or fallback is used."
	}
	return msg, nil
}

// speechFailure classifies a failed provider call. Only an explicit unknown outcome or the
// caller's cancellation stays unknown; every other error is a definite failure.
func speechFailure(ctx context.Context, err error) string {
	if errors.Is(err, speech.ErrOutcomeUnknown) || ctx.Err() != nil {
		return "unknown"
	}
	return "failed"
}

// completeEffect records a started effect's outcome even after ctx ends. A missed row means
// the effect was revoked or claimed elsewhere, so the caller must not use the outcome.
func (p *Policy) completeEffect(ctx context.Context, params sqlc.CompleteTelegramEffectParams, inactive string) error {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	saved, err := p.q.CompleteTelegramEffect(saveCtx, params)
	if err != nil {
		return err
	}
	if saved != 1 {
		return errors.New(inactive)
	}
	return nil
}

func (p *Policy) FinishSpeechInput(ctx context.Context, source string, album bool) error {
	effect, err := p.q.GetTelegramEffect(ctx, "stt:"+source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if effect.BotID != p.botID || effect.InputJson == "{}" {
		return nil
	}
	receipt, err := p.sessions.LookupSource(ctx, p.botID, source)
	if err != nil {
		return err
	}
	if receipt.ID == "" && (!album || !receipt.Staged) {
		return nil
	}
	return p.q.ClearTelegramEffectInput(ctx, "stt:"+source)
}

func (p *Policy) cleanupSpeechInputs(ctx context.Context, store *upload.Store) error {
	rows, err := p.q.ListDiscardedTelegramEffectInputs(ctx, p.botID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		receipt, err := p.sessions.LookupSource(ctx, p.botID, row.SourceKey)
		if err != nil {
			return err
		}
		if receipt.ID == "" {
			var msg session.Message
			if err := json.Unmarshal([]byte(row.InputJson), &msg); err != nil {
				return err
			}
			for _, file := range msg.Attachments {
				if err := store.Remove(file.ID); err != nil && !errors.Is(err, upload.ErrNotFound) {
					return fmt.Errorf("release speech upload: %w", err)
				}
			}
		}
		if err := p.q.ClearTelegramEffectInput(ctx, row.OperationKey); err != nil {
			return err
		}
	}
	return nil
}
