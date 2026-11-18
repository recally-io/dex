package engine

import (
	"context"
	"slices"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func (a *Agents) modelsFor(agentID string) durable.Models {
	return &agentModels{
		Models:  a.Catalog.Models,
		catalog: a.Catalog,
		fallback: func(ctx context.Context) ModelRef {
			if a.config.FallbackModel == nil {
				return ModelRef{}
			}
			return a.config.FallbackModel(ctx, agentID)
		},
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
	// An empty start is held back so a failover can replace the whole response.
	var pendingStart *ai.StartEvent
	flush := func() {
		if pendingStart != nil {
			_ = out.Push(*pendingStart)
			pendingStart = nil
		}
	}
	emitted := false
	for event := range primary.Events(ctx) {
		switch e := event.(type) {
		case ai.StartEvent:
			if !emitted && (e.Partial == nil || len(e.Partial.Content) == 0) {
				pendingStart = &e
				continue
			}
		case ai.ErrorEvent:
			if shouldFailover(ctx, emitted, e) {
				forward(ctx, out, m.Models.StreamSimple(ctx, fallbackModel, request, fallbackStreamOptions(fallbackModel, options)...), fallbackModel)
				return
			}
			flush()
			_ = out.Push(event)
			out.End()
			return
		}
		flush()
		emitted = true
		_ = out.Push(event)
		if _, done := event.(ai.DoneEvent); done {
			out.End()
			return
		}
	}
	flush()
	finish(ctx, out, primary, primaryModel)
}

// forward copies src into out until a terminal event.
func forward(ctx context.Context, out, src *ai.AssistantMessageEventStream, model *ai.Model) {
	for event := range src.Events(ctx) {
		_ = out.Push(event)
		switch event.(type) {
		case ai.DoneEvent, ai.ErrorEvent:
			out.End()
			return
		}
	}
	finish(ctx, out, src, model)
}

// finish ends out with the result of src, or as aborted when src ended without one.
func finish(ctx context.Context, out, src *ai.AssistantMessageEventStream, model *ai.Model) {
	if res, err := src.ResultContext(ctx); err == nil && res != nil {
		out.End(res)
		return
	}
	msg := &ai.AssistantMessage{
		API:          model.ProviderMeta.API,
		Provider:     model.ProviderID(),
		Model:        model.ID,
		Content:      []ai.AssistantContentBlock{},
		StopReason:   ai.StopReasonAborted,
		ErrorMessage: "Request was aborted",
		Timestamp:    time.Now().UnixMilli(),
	}
	_ = out.Push(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: msg})
	out.End(msg)
}

func shouldFailover(ctx context.Context, emitted bool, errEv ai.ErrorEvent) bool {
	if emitted || ctx.Err() != nil || errEv.Reason != ai.StopReasonError {
		return false
	}
	return errEv.Error == nil || !ai.IsContextOverflow(*errEv.Error, 0)
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
