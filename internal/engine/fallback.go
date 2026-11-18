package engine

import (
	"context"
	"slices"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func (a *Agents) setFallback(agentID string, ref ModelRef) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fallbacks[agentID] = ref
}

func (a *Agents) fallbackFor(ctx context.Context, agentID string) ModelRef {
	if a.config.FallbackModel != nil {
		return a.config.FallbackModel(ctx, agentID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fallbacks[agentID]
}

func (a *Agents) modelsFor(agentID string) durable.Models {
	return &agentModels{
		Models:   a.Catalog.Models,
		catalog:  a.Catalog,
		fallback: func(ctx context.Context) ModelRef { return a.fallbackFor(ctx, agentID) },
	}
}

// agentModels wraps *ai.Models so a missing or failing primary model falls back to the bot's
// configured fallback model before any response content has streamed. Embedding *ai.Models
// preserves optional PiG interfaces (ModelsLister, ModelsCatalog).
type agentModels struct {
	*ai.Models
	catalog  *Catalog
	fallback func(context.Context) ModelRef
}

func (m *agentModels) GetModel(provider, id string) *ai.Model {
	if resolved := m.Models.GetModel(provider, id); resolved != nil {
		return resolved
	}
	fb := m.fallback(context.Background())
	if fb.ModelID == "" || (fb.Provider == provider && fb.ModelID == id) {
		return nil
	}
	return m.Models.GetModel(fb.Provider, fb.ModelID)
}

func (m *agentModels) CompleteSimple(ctx context.Context, model *ai.Model, request ai.Context, options ...ai.StreamOptions) *ai.AssistantMessage {
	return m.StreamSimple(ctx, model, request, options...).Result()
}

func (m *agentModels) StreamSimple(ctx context.Context, model *ai.Model, request ai.Context, options ...ai.StreamOptions) *ai.AssistantMessageEventStream {
	fbRef := m.fallback(ctx)
	if model == nil || fbRef.ModelID == "" || (model.ProviderID() == fbRef.Provider && model.ID == fbRef.ModelID) {
		return m.Models.StreamSimple(ctx, model, request, options...)
	}
	fbModel := m.Models.GetModel(fbRef.Provider, fbRef.ModelID)
	if fbModel == nil {
		return m.Models.StreamSimple(ctx, model, request, options...)
	}

	m.catalog.mu.Lock()
	if m.catalog.closed {
		m.catalog.mu.Unlock()
		return m.Models.StreamSimple(ctx, model, request, options...)
	}
	out := ai.NewAssistantMessageEventStream()
	m.catalog.work.Go(func() {
		m.streamWithFallback(ctx, out, model, fbModel, request, options)
	})
	m.catalog.mu.Unlock()
	return out
}

func (m *agentModels) streamWithFallback(
	ctx context.Context,
	out *ai.AssistantMessageEventStream,
	primaryModel, fallbackModel *ai.Model,
	request ai.Context,
	options []ai.StreamOptions,
) {
	primary := m.Models.StreamSimple(ctx, primaryModel, request, options...)
	var pendingStart *ai.StartEvent
	emitted := false

	for event := range primary.Events(ctx) {
		if start, ok := startEvent(event); ok && !emitted && (start.Partial == nil || len(start.Partial.Content) == 0) {
			pendingStart = &start
			continue
		}
		if errEv, ok := errorEvent(event); ok {
			if shouldFailover(ctx, emitted, errEv) {
				fbOpts := fallbackStreamOptions(fallbackModel, options)
				m.forwardFallback(ctx, out, fallbackModel, request, fbOpts)
				return
			}
			if pendingStart != nil {
				_ = out.Push(*pendingStart)
			}
			_ = out.Push(event)
			out.End()
			return
		}
		if pendingStart != nil {
			_ = out.Push(*pendingStart)
			pendingStart = nil
		}
		emitted = true
		_ = out.Push(event)
		if isDoneEvent(event) {
			out.End()
			return
		}
	}

	if pendingStart != nil {
		_ = out.Push(*pendingStart)
	}
	if res, err := primary.ResultContext(ctx); err == nil && res != nil {
		out.End(res)
		return
	}
	msg := abortedAssistantMessage(primaryModel)
	_ = out.Push(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: msg})
	out.End(msg)
}

func (m *agentModels) forwardFallback(
	ctx context.Context,
	out *ai.AssistantMessageEventStream,
	fallbackModel *ai.Model,
	request ai.Context,
	options []ai.StreamOptions,
) {
	fbStream := m.Models.StreamSimple(ctx, fallbackModel, request, options...)
	for event := range fbStream.Events(ctx) {
		_ = out.Push(event)
		if isTerminalEvent(event) {
			out.End()
			return
		}
	}
	if res, err := fbStream.ResultContext(ctx); err == nil && res != nil {
		out.End(res)
		return
	}
	msg := abortedAssistantMessage(fallbackModel)
	_ = out.Push(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: msg})
	out.End(msg)
}

func shouldFailover(ctx context.Context, emitted bool, errEv ai.ErrorEvent) bool {
	if emitted || ctx.Err() != nil || errEv.Reason != ai.StopReasonError {
		return false
	}
	if errEv.Error != nil && ai.IsContextOverflow(*errEv.Error, 0) {
		return false
	}
	return true
}

func fallbackStreamOptions(fbModel *ai.Model, options []ai.StreamOptions) []ai.StreamOptions {
	fbOpts := slices.Clone(options)
	if len(fbOpts) > 0 {
		fbOpts[0].APIKey = ""
		if !fbModel.ProviderMeta.Reasoning {
			fbOpts[0].Thinking = ""
		}
	}
	return fbOpts
}

func startEvent(event ai.AssistantMessageEvent) (ai.StartEvent, bool) {
	switch e := event.(type) {
	case ai.StartEvent:
		return e, true
	case *ai.StartEvent:
		if e != nil {
			return *e, true
		}
	}
	return ai.StartEvent{}, false
}

func errorEvent(event ai.AssistantMessageEvent) (ai.ErrorEvent, bool) {
	switch e := event.(type) {
	case ai.ErrorEvent:
		return e, true
	case *ai.ErrorEvent:
		if e != nil {
			return *e, true
		}
	}
	return ai.ErrorEvent{}, false
}

func isDoneEvent(event ai.AssistantMessageEvent) bool {
	switch event.(type) {
	case ai.DoneEvent, *ai.DoneEvent:
		return true
	}
	return false
}

func isTerminalEvent(event ai.AssistantMessageEvent) bool {
	if isDoneEvent(event) {
		return true
	}
	_, err := errorEvent(event)
	return err
}

func abortedAssistantMessage(model *ai.Model) *ai.AssistantMessage {
	var api ai.API
	var provider, id string
	if model != nil {
		api = model.ProviderMeta.API
		provider = model.ProviderID()
		id = model.ID
	}
	return &ai.AssistantMessage{
		API:          api,
		Provider:     provider,
		Model:        id,
		Content:      []ai.AssistantContentBlock{},
		StopReason:   ai.StopReasonAborted,
		ErrorMessage: "Request was aborted",
		Timestamp:    time.Now().UnixMilli(),
	}
}
