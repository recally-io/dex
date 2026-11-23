package delivery

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/speech"
)

// TelegramHooks provides optional Telegram operations used by the durable sender.

type TelegramHooks interface {
	GroupParts([]channel.Part) []channel.Part
	WakeMedia()
	Control(ctx context.Context, peer string, remote channel.RemoteMessage, args engine.TelegramRequest) (channel.Receipt, error)
}

func (m *Manager) telegramCaller(ctx context.Context, caller engine.TaskContext) (sqlc.Submission, chat.ChannelContext, error) {
	var empty sqlc.Submission
	var channelContext chat.ChannelContext
	if caller.AgentID != m.agentID || caller.Key == "" || caller.Scope != "" {
		return empty, channelContext, errors.New("invalid Telegram tool caller")
	}
	conversation, err := m.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: m.agentID, ExecutionScope: caller.Scope, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return empty, channelContext, err
	}
	source, err := m.q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil {
		return empty, channelContext, err
	}
	if source.ConversationID != conversation.ID || conversation.ParentID.Valid || source.Status != "pending" || !strings.HasPrefix(source.Origin, "telegram:") {
		return empty, channelContext, errors.New("telegram tools require their active Telegram Main input")
	}
	if err := json.Unmarshal([]byte(source.ChannelContext), &channelContext); err != nil {
		return empty, channelContext, err
	}
	if channelContext.AccountID == "" || channelContext.SenderID == "" {
		return empty, channelContext, errors.New("telegram source identity is missing")
	}
	allowed, err := m.authorizedReply(ctx, "telegram", source.Origin, source.ChannelContext, source.ID)
	if err != nil {
		return empty, channelContext, err
	}
	if !allowed {
		return empty, channelContext, errors.New("telegram source authorization was revoked")
	}
	return source, channelContext, nil
}

// TelegramAction binds every operation to the caller's persisted source and current peer.
func (m *Manager) TelegramAction(ctx context.Context, caller engine.TaskContext, args engine.TelegramRequest, resolve speech.Resolve) (engine.TelegramResult, error) {
	source, identity, err := m.telegramCaller(ctx, caller)
	if err != nil {
		return engine.TelegramResult{}, err
	}
	if !slices.Contains([]string{"list", "edit", "edit_caption", "delete", "react", "copy", "forward", "speak"}, args.Action) {
		return engine.TelegramResult{}, errors.New("unsupported Telegram action")
	}
	if args.Action == "list" {
		rows, err := m.q.ListTelegramPeerReplies(ctx, sqlc.ListTelegramPeerRepliesParams{AgentID: m.agentID, Origin: source.Origin})
		if err != nil {
			return engine.TelegramResult{}, err
		}
		result := engine.TelegramResult{Status: "confirmed"}
		for _, row := range rows {
			var target chat.ChannelContext
			if err := json.Unmarshal([]byte(row.ChannelContext), &target); err != nil {
				return result, err
			}
			if target.AccountID != identity.AccountID || target.SenderID != identity.SenderID {
				continue
			}
			var ids []channel.RemoteMessage
			if err := json.Unmarshal([]byte(row.ChannelRemoteIds), &ids); err != nil {
				return result, err
			}
			text := []rune(row.Answer.String)
			if len(text) > 240 {
				text = text[:240]
			}
			result.Deliveries = append(result.Deliveries, engine.TelegramDelivery{ID: row.ID, Text: string(text), Messages: len(ids)})
		}
		effects, err := m.q.ListTelegramPeerEffects(ctx, sqlc.ListTelegramPeerEffectsParams{AgentID: m.agentID, Peer: source.Origin, AccountID: identity.AccountID, SenderID: identity.SenderID})
		if err != nil {
			return result, err
		}
		for _, effect := range effects {
			var sent engine.TelegramResult
			if err := json.Unmarshal([]byte(effect.ResultJson), &sent); err != nil {
				return result, err
			}
			result.Deliveries = append(result.Deliveries, engine.TelegramDelivery{ID: "operation:" + effect.OperationKey, Text: "Copied or forwarded bot message", Messages: len(sent.MessageIDs)})
		}
		return result, nil
	}
	m.mu.Lock()
	runtime := m.bots["telegram"]
	m.mu.Unlock()
	if runtime == nil {
		return engine.TelegramResult{}, errors.New("telegram is off")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(runtime.ctx, cancel) //nolint:contextcheck // Both the request and channel runtime must cancel this operation.
	defer stop()
	if args.Action == "speak" {
		result, err := m.speak(ctx, caller, source, identity, args.Text, resolve)
		if err != nil {
			return result, fmt.Errorf("TTS failed; keep this readable text in the final reply: %s (%w)", args.Text, err)
		}
		return result, nil
	}
	ids, err := m.telegramIDs(ctx, source, identity, args.DeliveryID)
	if err != nil {
		return engine.TelegramResult{}, err
	}
	if args.Index < 0 || args.Index >= len(ids) {
		return engine.TelegramResult{}, errors.New("telegram message index is outside the confirmed delivery")
	}
	switch args.Action {
	case "edit":
		if ids[args.Index].Kind != "rich-v1" {
			return engine.TelegramResult{}, errors.New("telegram text edits require a rich message")
		}
		if strings.TrimSpace(args.Text) == "" {
			return engine.TelegramResult{}, errors.New("telegram text edits need nonempty text")
		}
	case "edit_caption":
		if ids[args.Index].Kind != "media" {
			return engine.TelegramResult{}, errors.New("rich messages have no media caption")
		}
		if len(utf16.Encode([]rune(args.Text))) > 1024 {
			return engine.TelegramResult{}, errors.New("telegram edit caption exceeds 1024 UTF-16 units")
		}
	}
	existing, err := m.effectForTool(ctx, caller, source, identity, args.Action)
	if err != nil {
		return engine.TelegramResult{}, err
	}
	if existing.State == "done" {
		var result engine.TelegramResult
		err := json.Unmarshal([]byte(existing.ResultJson), &result)
		return result, err
	}
	transport, ok := runtime.bot.(TelegramHooks)
	if !ok {
		return engine.TelegramResult{}, errors.New("telegram control transport is unavailable")
	}
	receipt, callErr := transport.Control(ctx, source.Origin, ids[args.Index], args)
	result := engine.TelegramResult{Status: "done", MessageIDs: receipt.MessageIDs, MessageKind: receipt.Kind}
	if len(receipt.MessageIDs) > 0 {
		result.DeliveryID = "operation:" + caller.Key
	}
	encoded, _ := json.Marshal(result)
	state, reason := "done", ""
	if callErr != nil {
		state = "unknown"
		reason = callErr.Error()
		if errors.Is(callErr, channel.ErrPermanent) {
			state = "failed"
		}
	}
	if err := m.completeEffect(ctx, sqlc.CompleteTelegramEffectParams{OperationKey: caller.Key, State: state, ResultJson: string(encoded), Error: reason}, "telegram operation is no longer active"); err != nil {
		return result, err
	}
	return result, callErr
}

func (m *Manager) effectForTool(ctx context.Context, caller engine.TaskContext, source sqlc.Submission, identity chat.ChannelContext, kind string) (sqlc.TelegramEffect, error) {
	row, err := m.q.GetTelegramEffect(ctx, caller.Key)
	if errors.Is(err, sql.ErrNoRows) {
		n, createErr := m.q.CreateTelegramEffect(ctx, sqlc.CreateTelegramEffectParams{OperationKey: caller.Key, AgentID: m.agentID, AccountID: identity.AccountID, SenderID: identity.SenderID, Peer: source.Origin, SourceKey: source.SourceKey.String, Kind: kind, InputJson: "{}", CreatedAt: time.Now().UnixMilli()})
		if createErr != nil {
			return row, createErr
		}
		if n != 1 {
			return row, errors.New("telegram effect authorization changed")
		}
		row, err = m.q.GetTelegramEffect(ctx, caller.Key)
	}
	if err != nil {
		return row, err
	}
	if row.AgentID != m.agentID || row.Peer != source.Origin || row.Kind != kind || row.SourceKey != source.SourceKey.String || row.AccountID != identity.AccountID || row.SenderID != identity.SenderID {
		return row, errors.New("telegram operation belongs to another scope")
	}
	switch row.State {
	case "done":
		return row, nil
	case "processing":
		if err := m.q.MarkTelegramEffectUnknown(ctx, caller.Key); err != nil {
			return row, err
		}
		return row, errors.New("telegram operation outcome unknown; automatic replay is disabled")
	case "unknown", "failed", "discarded":
		return row, fmt.Errorf("telegram operation %s: %s", row.State, row.Error)
	}
	n, err := m.q.StartTelegramEffect(ctx, caller.Key)
	if err != nil {
		return row, err
	}
	if n != 1 {
		return row, errors.New("telegram operation already claimed")
	}
	return row, nil
}

func (m *Manager) speak(ctx context.Context, caller engine.TaskContext, source sqlc.Submission, identity chat.ChannelContext, text string, resolve speech.Resolve) (engine.TelegramResult, error) {
	settings, err := m.q.GetTelegramSettings(ctx, m.agentID)
	if err != nil {
		return engine.TelegramResult{}, err
	}
	var options struct {
		TTS speech.Options `json:"tts"`
	}
	if err := json.Unmarshal([]byte(settings.Options), &options); err != nil {
		return engine.TelegramResult{}, err
	}
	config := options.TTS.Budgets()
	if !config.Enabled || !config.AllowSending {
		return engine.TelegramResult{}, errors.New("TTS is off; explicit provider/model/voice and data-sending consent are required")
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > config.MaxTextCharacters {
		return engine.TelegramResult{}, errors.New("TTS text exceeds the configured character budget")
	}
	row, err := m.effectForTool(ctx, caller, source, identity, "tts")
	if err != nil {
		return engine.TelegramResult{}, err
	}
	if row.State != "done" {
		callCtx, callCancel := context.WithTimeout(ctx, time.Duration(config.TimeoutSeconds)*time.Second)
		endpoint, callErr := resolve(callCtx, config.Provider)
		var body []byte
		if callErr == nil {
			_, body, callErr = speech.Call(callCtx, endpoint, config, "tts", "", text, nil)
		}
		callCancel()
		state, reason := "done", ""
		if callErr != nil {
			state = speechFailure(ctx, callErr)
			reason = callErr.Error()
		}
		name := "speech." + config.Format
		if config.Format == "opus" {
			name = "speech.ogg"
		}
		if err := m.completeEffect(ctx, sqlc.CompleteTelegramEffectParams{OperationKey: caller.Key, State: state, Body: body, Name: name, ResultJson: "{}", Error: reason}, "TTS operation is no longer active"); err != nil {
			return engine.TelegramResult{}, err
		}
		if callErr != nil {
			return engine.TelegramResult{}, callErr
		}
		row.Body, row.Name = body, name
	}
	queued, err := m.QueueFile(ctx, caller, row.Name, bytes.NewReader(row.Body))
	if err != nil {
		return engine.TelegramResult{}, err
	}
	if !queued {
		return engine.TelegramResult{}, errors.New("TTS destination cannot queue native audio")
	}
	if err := m.q.ClearTelegramEffectInput(ctx, caller.Key); err != nil {
		return engine.TelegramResult{}, err
	}
	return engine.TelegramResult{Status: "native audio queued", Text: text}, nil
}

func (m *Manager) telegramIDs(ctx context.Context, source sqlc.Submission, identity chat.ChannelContext, id string) ([]channel.RemoteMessage, error) {
	if key, ok := strings.CutPrefix(id, "operation:"); ok {
		effect, err := m.q.GetTelegramEffect(ctx, key)
		if err != nil {
			return nil, errors.New("telegram delivery was not found")
		}
		if effect.AgentID != m.agentID || effect.Peer != source.Origin || effect.AccountID != identity.AccountID || effect.SenderID != identity.SenderID || effect.State != "done" || !slices.Contains([]string{"copy", "forward"}, effect.Kind) {
			return nil, errors.New("telegram delivery is outside the current sender and peer")
		}
		original, err := m.q.GetSubmissionBySource(ctx, sql.NullString{String: effect.SourceKey, Valid: true})
		if err != nil {
			return nil, err
		}
		parent, err := m.q.GetSubmission(ctx, original)
		if err != nil {
			return nil, err
		}
		if parent.ConversationID != source.ConversationID || parent.ChannelRevoked != 0 {
			return nil, errors.New("telegram delivery source is no longer authorized")
		}
		var result engine.TelegramResult
		err = json.Unmarshal([]byte(effect.ResultJson), &result)
		messages := make([]channel.RemoteMessage, len(result.MessageIDs))
		for i, id := range result.MessageIDs {
			messages[i] = channel.RemoteMessage{ID: id, Kind: result.MessageKind}
		}
		return messages, err
	}
	target, err := m.q.GetSubmission(ctx, id)
	if err != nil {
		return nil, errors.New("telegram delivery was not found")
	}
	var targetIdentity chat.ChannelContext
	if err := json.Unmarshal([]byte(target.ChannelContext), &targetIdentity); err != nil {
		return nil, err
	}
	if target.ConversationID != source.ConversationID || target.Origin != source.Origin || target.ChannelRevoked != 0 || !target.DeliveredAt.Valid || targetIdentity.AccountID != identity.AccountID || targetIdentity.SenderID != identity.SenderID {
		return nil, errors.New("telegram delivery is outside the current sender and peer")
	}
	var ids []channel.RemoteMessage
	err = json.Unmarshal([]byte(target.ChannelRemoteIds), &ids)
	return ids, err
}
