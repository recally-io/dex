package runner

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"
)

const slowCall = time.Second

type callTraceKey struct{}

type callTrace struct {
	id, environment, method string
}

// TraceConnectionWait correlates the wait for an enrolled Runner to return with
// the RPC that follows. The returned function records the end of that wait.
func TraceConnectionWait(ctx context.Context, environment, method string) (context.Context, func(time.Time, error)) {
	trace := newCallTrace(environment, method)
	return context.WithValue(ctx, callTraceKey{}, trace), func(start time.Time, err error) {
		elapsed := time.Since(start)
		trace.log(ctx, traceLevel(elapsed, err), "connection_wait", "elapsed", elapsed, "result", callOutcome(err))
	}
}

func newCallTrace(environment, method string) callTrace {
	// Use only protocol method names; never log arbitrary client data or error text.
	switch method {
	case MethodExec, MethodRead, MethodWrite, MethodAppend, MethodStat, MethodList, MethodCanonical,
		MethodText, MethodMakeDir, MethodRemove, MethodRename, MethodTruncate, MethodFlush, MethodTempDir, MethodTempFile:
	default:
		method = "other"
	}
	return callTrace{id: rand.Text(), environment: environment, method: method}
}

func (t callTrace) log(ctx context.Context, level slog.Level, phase string, attrs ...any) {
	slog.Log(ctx, level, "runner RPC", append([]any{
		"call_id", t.id, "environment", t.environment, "method", t.method, "phase", phase,
	}, attrs...)...)
}

func traceLevel(elapsed time.Duration, err error) slog.Level {
	// Routine file errors (such as checking for a missing path) are common.
	switch callOutcome(err) {
	case "success", "failed":
		if elapsed < slowCall {
			return slog.LevelDebug
		}
	}
	return slog.LevelInfo
}

func callOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrOffline):
		return "offline"
	case errors.Is(err, ErrUnknown):
		return "unknown"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, errOutputBackpressure):
		return "output_backpressure"
	default:
		return "failed"
	}
}
