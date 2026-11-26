package app_test

import (
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type reactionCall struct{ message, status string }

type reactionGate struct {
	calls           chan reactionCall
	release         <-chan struct{}
	failStatus      string
	limitNextQueued atomic.Bool
	retryAfter      int
	queuedTimes     chan time.Time
	holdQueuedRetry <-chan struct{}
	unknownOutcome  bool
}

func (g *reactionGate) handle(r *http.Request, message, status string) bool {
	select {
	case g.calls <- reactionCall{message, status}:
	case <-r.Context().Done():
		return false
	}
	if status != g.failStatus {
		return true
	}
	select {
	case <-g.release:
	case <-r.Context().Done():
	}
	return false
}

func (g *reactionGate) expect(t *testing.T, message, status string) {
	t.Helper()
	select {
	case got := <-g.calls:
		if got != (reactionCall{message, status}) {
			t.Fatalf("reaction = %+v, want %s / %s", got, message, status)
		}
	case <-time.After(12 * time.Second):
		t.Fatalf("no reaction for %s / %s", message, status)
	}
}

func TestUserTelegramSlowRejectedReactionDoesNotBlockReply(t *testing.T) {
	t.Parallel()
	// Given an authorized private chat with no explicit reaction option.
	data := t.TempDir()
	release := make(chan struct{})
	gate := &reactionGate{calls: make(chan reactionCall, 10), release: release, failStatus: "👀"}
	f := newFakeTelegram(t)
	f.reactGate = gate
	_ = startTelegram(t, f, data, 42)
	// When the first status call stalls and then hits a rate limit.
	f.message(1, 42, 42, "telegram status receipt")
	gate.expect(t, "1", "👀")
	// Then the reply arrives while the reaction is stalled, and success never overtakes it.
	if got := f.next(t).Text; got != "echo: telegram status receipt" {
		t.Fatalf("reply = %q", got)
	}
	// A later update also commits and gets a reply while the first call waits.
	f.message(2, 42, 42, "later input")
	gate.expect(t, "2", "👀")
	if got := f.next(t).Text; got != "echo: later input" {
		t.Fatalf("later reply = %q", got)
	}
	conn := controlWriteDB(t, data)
	waitTelegramQueueAck(t, conn, 1, "unknown")
	waitTelegramQueueAck(t, conn, 2, "unknown")
	close(release)
	seen := make(map[string]bool)
	for range 2 {
		select {
		case got := <-gate.calls:
			if got.status != "👍" || seen[got.message] {
				t.Fatalf("unexpected reaction after releasing requests: %+v", got)
			}
			seen[got.message] = true
		case <-time.After(12 * time.Second):
			t.Fatal("missing terminal reaction")
		}
	}
	waitTelegramQueueAck(t, conn, 1, "failed")
	waitTelegramQueueAck(t, conn, 2, "failed")
}

func TestUserFeishuSlowRejectedReactionDoesNotBlockReply(t *testing.T) {
	t.Parallel()
	// Given a paired private chat with no explicit reaction option.
	release := make(chan struct{})
	gate := &reactionGate{calls: make(chan reactionCall, 10), release: release, failStatus: "OneSecond"}
	f := newFakeFeishu(t)
	f.reactGate = gate
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	// When the first status call stalls and then hits a rate limit.
	f.text(t, "om_slow", "owner", "p2p", "feishu status receipt")
	gate.expect(t, "om_slow", "OneSecond")
	// Then the final reply does not wait, and the terminal state follows the failed attempt.
	f.waitReply(t, "echo: feishu status receipt")
	close(release)
	gate.expect(t, "om_slow", "DONE")
}

func TestUserTelegramStatusWaitsForNativeRunAndConfirmation(t *testing.T) {
	t.Parallel()
	// Given two queued inputs and a local boundary holding the first run.
	data := t.TempDir()
	gate := &reactionGate{calls: make(chan reactionCall, 20)}
	f := newFakeTelegram(t)
	f.reactGate = gate
	d := startTelegram(t, f, data, 42, 43)
	d.setModel(t, "bash")
	d.send(t, d.mainSession(t), taskGate(t, data, "earlier"))
	waitTaskFile(t, filepath.Join(data, "earlier.pid"))
	f.message(1, 42, 42, taskGate(t, data, "incoming"))
	gate.expect(t, "1", "👀")
	conn := controlWriteDB(t, data)
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1'"); n != 1 {
		t.Fatalf("queued status before durable receipt: %d", n)
	}
	f.message(2, 43, 43, "printf second-answer")
	gate.expect(t, "2", "👀") // Neither input can show processing yet.
	// When the turn runs, and the first reply remains unconfirmed, processing stays visible.
	f.failSend.Store(true)
	releaseResultGate(t, data, "earlier")
	waitTaskFile(t, filepath.Join(data, "incoming.pid"))
	gate.expect(t, "1", "🔥")
	releaseResultGate(t, data, "incoming")
	eventually(t, "first reply awaiting retry", func() (bool, any) {
		return count(t, conn, "SELECT count(*) FROM outbox_messages o JOIN submissions s ON s.id=o.source_submission_id WHERE s.source_key='telegram:123:1' AND o.status='pending' AND o.retry_at>0") == 1, nil
	})
	select {
	case got := <-gate.calls:
		if got.message == "1" {
			t.Fatalf("unexpected state before delivery: %+v", got)
		}
	default:
	}
	// Then when delivery retries and the platform confirms, the first source shows success.
	f.failSend.Store(false)
	_ = f.next(t)
	for {
		select {
		case got := <-gate.calls:
			if got.message == "1" {
				if got.status != "👍" {
					t.Fatalf("terminal reaction = %+v", got)
				}
				return
			}
		case <-time.After(12 * time.Second):
			t.Fatal("missing confirmed reply state")
		}
	}
}

func TestUserFeishuStatusReplacesReactionByReturnedIDOnStop(t *testing.T) {
	t.Parallel()
	// Given a native run held in its shell tool.
	data := t.TempDir()
	gate := &reactionGate{calls: make(chan reactionCall, 10)}
	f := newFakeFeishu(t)
	f.reactGate = gate
	d := startFeishu(t, f, data)
	pairFeishu(t, d, f)
	d.setModel(t, "bash")
	f.text(t, "om_running", "owner", "p2p", taskGate(t, data, "cancel"))
	gate.expect(t, "om_running", "OneSecond")
	gate.expect(t, "om_running", "delete:remote-OneSecond")
	gate.expect(t, "om_running", "OnIt")
	waitTaskFile(t, filepath.Join(data, "cancel.pid"))
	// When the owner stops the run, Feishu removes the running reaction by ID.
	f.text(t, "om_stop", "owner", "p2p", "/stop")
	gate.expect(t, "om_running", "delete:remote-OnIt")
	gate.expect(t, "om_running", "MinusOne")
}

func TestUserTelegramStatusRespectsPrivateOff(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": map[string]any{"reactions": false}}, nil)
	f.message(1, 42, 42, "private off")
	_ = f.next(t)
	waitTelegramQueueAck(t, controlWriteDB(t, data), 1, "not_attempted")
}

func TestUserFeishuStatusPreservesExplicitOffAcrossSettingsReload(t *testing.T) {
	t.Parallel()
	// Given an already paired Feishu app with private reactions explicitly off.
	data := t.TempDir()
	gate := &reactionGate{calls: make(chan reactionCall, 10)}
	f := newFakeFeishu(t)
	f.reactGate = gate
	d := startFeishu(t, f, data)
	pairFeishu(t, d, f)
	path := "/api/agents/default/channels/feishu"
	d.must(t, http.MethodPut, path, map[string]any{"appId": "app", "appSecret": "", "brand": "feishu", "reactions": false}, nil)
	// When the same credentials are saved again without the option, then reloaded.
	d.must(t, http.MethodPut, path, map[string]any{"appId": "app", "appSecret": "", "brand": "feishu"}, nil)
	d.stop()
	d = startFeishu(t, f, data)
	var saved struct {
		Reactions *bool `json:"reactions"`
	}
	d.must(t, http.MethodGet, path, nil, &saved)
	if saved.Reactions == nil || *saved.Reactions {
		t.Fatalf("lost explicit off: %+v", saved)
	}
	// Then a reply still arrives without a reaction, and groups/others still cannot trigger them.
	f.text(t, "om_off", "owner", "p2p", "silent receipt")
	f.waitReply(t, "echo: silent receipt")
	d.must(t, http.MethodPut, path, map[string]any{"appId": "app", "appSecret": "", "brand": "feishu", "reactions": true}, nil)
	f.text(t, "om_group", "owner", "group", "ignored")
	f.text(t, "om_other", "other", "p2p", "ignored")
	f.text(t, "om_on", "owner", "p2p", "visible receipt")
	gate.expect(t, "om_on", "OneSecond")
}

func TestUserTelegramStatusShowsFailureWhenTheReplyIsRejected(t *testing.T) {
	t.Parallel()
	// Given a permanent rejection of the final reply.
	gate := &reactionGate{calls: make(chan reactionCall, 10)}
	f := newFakeTelegram(t)
	f.reactGate = gate
	f.failures["sendRichMessage"] = []platformFailure{{Code: 400}}
	startTelegram(t, f, t.TempDir(), 42)
	f.message(1, 42, 42, "rejected reply")
	gate.expect(t, "1", "👀")
	// Then rejection appears as a delivery failure.
	for {
		select {
		case got := <-gate.calls:
			if got.status == "🔥" {
				continue
			}
			if got != (reactionCall{"1", "👎"}) {
				t.Fatalf("terminal reaction = %+v; want delivery failure", got)
			}
			return
		case <-time.After(12 * time.Second):
			t.Fatal("missing failure reaction")
		}
	}
}
