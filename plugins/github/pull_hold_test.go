package github

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/plugin"
	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/plugins/github/store"
)

func TestUserPullHoldsReviewsOnlyWithTheWebhookRule(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := plugin.Migrate(ctx, conn, "github", Migrations); err != nil {
		t.Fatal(err)
	}
	gh := store.New(conn)
	tasks := tasksvc.New(conn, nil)
	if err := gh.SaveGitHubReviewPull(ctx, store.SaveGitHubReviewPullParams{RepositoryID: 10, PullNumber: 3, Token: "test-only", ConfigurationID: "config"}); err != nil {
		t.Fatal(err)
	}
	// Each pull returns a new trusted review of a PR that no Task links.
	var review atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/10":
			_, _ = io.WriteString(w, `{"id":10,"full_name":"org/repo"}`)
		case "/repos/org/repo/pulls/3/reviews":
			_, _ = fmt.Fprintf(w, `[{"id":%d,"state":"COMMENTED","commit_id":"abc","submitted_at":"2026-11-21T03:00:00Z","author_association":"MEMBER","user":{"login":"m"}}]`, 90+review.Add(1))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewPuller(gh, tasks)
	p.origin = srv.URL
	pull := func() []sqlc.ListHeldTaskSourceEventsRow {
		t.Helper()
		rows, err := gh.ListGitHubReviewPulls(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("pull configuration: %+v, %v", rows, err)
		}
		if err := p.pull(ctx, rows[0]); err != nil {
			t.Fatal(err)
		}
		held, err := tasks.ListHeld(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}

	if held := pull(); len(held) != 0 {
		t.Fatalf("Pull without a Webhook rule must hold nothing: %+v", held)
	}
	if _, err := gh.SaveGitHubWebhook(ctx, store.SaveGitHubWebhookParams{RepositoryID: 10, Secret: strings.Repeat("s", 32), HoldUnmatched: 1}); err != nil {
		t.Fatal(err)
	}
	if held := pull(); len(held) != 1 || held[0].EventType != ReviewSubmitted {
		t.Fatalf("Pull must use the Webhook rule and hold the review: %+v", held)
	}
}
