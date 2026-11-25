package chat

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserUnprovenAndRevokedFinalReportsCannotWakeMain(t *testing.T) {
	var logs sessionLogBuffer
	priorLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(priorLogger) })
	started, release := make(chan engine.TaskContext, 1), make(chan struct{})
	r := openNativeTest(t, t.TempDir(), "tool", func(next reportFunc) reportFunc {
		return func(ctx context.Context, call engine.TaskContext, args ReportParentArgs) (string, error) {
			started <- call
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return next(ctx, call, args)
		}
	})
	ctx := t.Context()
	parentID, parent := followUpSource(t, r)
	if _, err := r.service.ToolSessionNew(ctx, parent, SessionNewArgs{
		Title: "Revoked before first final", Message: `session_report {"content":"private conclusion after revocation"}`, FollowUp: true,
	}); err != nil {
		t.Fatal(err)
	}
	turn := turnID(parent.Key)
	if err := r.service.admit(ctx, taskInput(t, r, turn)); err != nil {
		t.Fatal(err)
	}
	var owner engine.TaskContext
	select {
	case owner = <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("the native operation never reached the writer")
	}
	// Given the real operation is paused before staging, neither membership in its run
	// nor having the sole open grant can stand in for its exact activation binding.
	for _, claimed := range []int64{0, owner.ActivationID + 1} {
		invalid := owner
		invalid.Key += "-claim-" + strconv.FormatInt(claimed, 10)
		invalid.ActivationID = claimed
		got, err := r.service.ToolReportParent(ctx, invalid, ReportParentArgs{Content: "wrong owner"})
		if got != ReportIgnored || err != nil {
			t.Fatalf("unproven final did not fail closed: %q, %v", got, err)
		}
	}
	// When the original receipt is revoked, the real owner can no longer stage the report.
	if err := r.q.RevokeChannelSubmission(ctx, parent.SourceRequestID); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitReceipt(t, r, turn)
	prior, err := r.q.GetRoutineToolOperation(ctx, owner.Key)
	if err != nil || prior.Result != ReportIgnored {
		t.Fatalf("missing durable no-op for the first report after revocation: %+v, %v", prior, err)
	}
	if got, err := r.service.ToolReportParent(ctx, owner, ReportParentArgs{Content: "replay"}); err != nil || got != prior.Result {
		t.Fatalf("wrong no-op replay: %q, %v", got, err)
	}
	if log := logs.String(); strings.Count(log, "phase=report_ignored reason=revoked") != 1 || strings.Contains(log, "private conclusion after revocation") {
		t.Fatalf("expected one content-free revocation diagnostic, including on replay: %s", log)
	}
	result := receipt(t, r, "result:"+turn)
	if result.ConversationID != parentID || result.ChannelRevoked == 0 || strings.Contains(result.Content, "channel-follow-up") || strings.Contains(result.Content, "private conclusion") {
		t.Fatalf("revoked result kept channel authority: %+v", result)
	}
	var n int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE conversation_id=? AND id LIKE 'report:%'`, parentID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unproven or revoked final woke Main %d times: %v", n, err)
	}
}

func TestUserOptedInFinalReportCommitsOnlyWithItsNativeOutcome(t *testing.T) {
	for _, scenario := range []string{"done", "revoked", "failed", "interrupted", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			dir := t.TempDir()
			type committedReport struct {
				call        engine.TaskContext
				disposition string
				err         error
			}
			staged, release := make(chan committedReport, 1), make(chan struct{})
			model := "tool"
			if scenario == "failed" {
				model = "tool-report-fail"
			}
			r := openNativeTest(t, dir, model, func(next reportFunc) reportFunc {
				return func(ctx context.Context, call engine.TaskContext, args ReportParentArgs) (string, error) {
					disposition, err := next(ctx, call, args)
					staged <- committedReport{call, disposition, err}
					// No test substitute for reporting or settlement: hold the real tool's
					// response after the writer commits, before PiG can mark the input done.
					select {
					case <-release:
					case <-ctx.Done():
					}
					if scenario == "failed" {
						// Lose the response after commit. The real native run will ask the
						// fixture provider to continue, which returns a generation error.
						return "", errors.New("report response lost after commit")
					}
					return disposition, err
				}
			})
			parentID, parent := followUpSource(t, r)
			child, err := r.service.ToolSessionNew(ctx, parent, SessionNewArgs{
				Title: "Staged", Message: `session_report {"content":"first verified findings"}`, FollowUp: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			childID, err := strconv.ParseInt(child.ID, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			turn := turnID(parent.Key)
			if err := r.service.admit(ctx, taskInput(t, r, turn)); err != nil {
				t.Fatal(err)
			}
			var tool committedReport
			select {
			case tool = <-staged:
			case <-time.After(15 * time.Second):
				t.Fatal("native session_report did not commit")
			}
			if tool.err != nil || tool.disposition != ReportStaged || tool.call.SourceRequestID != turn || tool.call.ActivationID == 0 {
				t.Fatalf("wrong report ownership: %+v", tool)
			}
			resultID := "result:" + turn
			unlessResult := func() {
				t.Helper()
				var n int
				if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE conversation_id=? AND kind='task_result'`, parentID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("got %d early or standalone results: %v", n, err)
				}
			}
			unlessResult()
			if sub := receipt(t, r, turn); sub.Status != "pending" {
				t.Fatalf("native settled before receiving its tool result: %+v", sub)
			}
			checkReport := func(service *Service, call engine.TaskContext, want string) {
				t.Helper()
				if got, err := service.ToolReportParent(ctx, call, ReportParentArgs{Content: "must not replace first report"}); err != nil || got != want {
					t.Errorf("tool %s result=%q want=%q err=%v", call.Key, got, want, err)
				}
			}
			replay := tool.call
			later := tool.call
			later.Key += "-later"
			checkReport(r.service, replay, ReportStaged)
			checkReport(r.service, later, ReportIgnored)
			var wg sync.WaitGroup
			racing := tool.call
			racing.Key += "-racing"
			if scenario != "interrupted" {
				wg.Go(func() { checkReport(r.service, racing, ReportIgnored) })
			}
			switch scenario {
			case "done", "failed":
				close(release)
				awaitReceipt(t, r, turn)
			case "revoked":
				if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "99", Options: "{}"}); err != nil {
					t.Fatal(err)
				}
				checkReport(r.service, replay, ReportStaged)
				close(release)
				awaitReceipt(t, r, turn)
			case "stopped":
				// A second opt-in still waits in control. Withdraw it by request ID, not by
				// the foreground activation, then explicitly stop the owner of the stage.
				parent.Key += "-withdraw"
				if _, err := r.service.ToolSessionSend(ctx, parent, SessionSendArgs{ID: child.ID, Message: "must not execute", FollowUp: true}); err != nil {
					t.Fatal(err)
				}
				queued := turnID(parent.Key)
				if err := r.service.admit(ctx, taskInput(t, r, queued)); !errors.Is(err, engine.ErrBusy) {
					t.Fatalf("second opted-in request was not queued independently: %v", err)
				}
				if _, err := r.service.Withdraw(ctx, "bot", childID, queued); err != nil {
					t.Fatal(err)
				}
				if sub := receipt(t, r, queued); sub.Reason.String != "withdrawn" || sub.AdmittedAt.Valid {
					t.Fatalf("withdrew wrong input: %+v", sub)
				}
				if err := r.service.CancelSession(ctx, "bot", childID); err != nil {
					t.Fatal(err)
				}
				close(release)
			case "interrupted":
				r.close()
				r = openNativeTest(t, dir, "tool", nil)
				if err := r.service.RecoverSessions(ctx); err != nil {
					t.Fatal(err)
				}
			}
			wg.Wait()
			checkReport(r.service, replay, ReportStaged)
			checkReport(r.service, later, ReportIgnored)
			later.Key += "-after"
			checkReport(r.service, later, ReportIgnored)
			if scenario == "stopped" {
				if sub := awaitReceipt(t, r, turn); sub.Status != "unanswered" || sub.Reason.String != "stopped" {
					t.Fatalf("child Stop retained success: %+v", sub)
				}
				unlessResult()
				return
			}
			result := receipt(t, r, resultID)
			f, err := r.q.GetChannelFollowUpByDelegation(ctx, turn)
			if err != nil || f.NativeActivationID != (sql.NullInt64{Int64: tool.call.ActivationID, Valid: true}) {
				t.Fatalf("native binding not stable: %+v err=%v", f, err)
			}
			switch scenario {
			case "done":
				if !strings.Contains(result.Content, `"answer":"first verified findings"`) || f.StagedReport.Valid {
					t.Fatalf("result and consumption not committed together: %+v report=%+v", result, f.StagedReport)
				}
			case "revoked":
				if result.ChannelRevoked == 0 || strings.Contains(result.Content, "first verified findings") || strings.Contains(result.Content, "channel-follow-up") || !strings.Contains(result.Content, `"outcome":"done"`) {
					t.Fatalf("revoked grant influenced its result: %+v", result)
				}
			case "failed", "interrupted":
				if !strings.Contains(result.Content, `"outcome":"`+scenario+`"`) || strings.Contains(result.Content, "first verified findings") {
					t.Fatalf("report was used as success after %s: %+v", scenario, result)
				}
			}
			var n int
			if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE conversation_id=? AND kind='task_result'`, parentID).Scan(&n); err != nil || n != 1 {
				t.Fatalf("wakes after settlement = %d (want exactly one canonical): %v", n, err)
			}
		})
	}
}
