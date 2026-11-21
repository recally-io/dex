package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worktask"
	"github.com/recally-io/dex/plugins/github/store"
)

func TestUserPullsReviewsBeforeSavingCheckpoint(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	gh := store.New(conn)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	tasks := worktask.New(conn, nil)
	task, err := tasks.Create(ctx, "default", "Review")
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.LinkObject(ctx, "default", task.ID, Namespace(10), PullRequest(2)); err != nil {
		t.Fatal(err)
	}
	if err := gh.SaveGitHubReviewPull(ctx, store.SaveGitHubReviewPullParams{RepositoryID: 10, PullNumber: 2, Token: "test-only", ConfigurationID: "config"}); err != nil {
		t.Fatal(err)
	}

	// Given a response whose first review is valid but whose last review cannot be saved.
	const valid = `{"id":71,"state":"APPROVED","body":"Looks good.","commit_id":"abc","submitted_at":"2026-11-21T03:00:00Z"}`
	const invalid = `{"state":"APPROVED"}`
	var stage atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repositories/10":
			_, _ = io.WriteString(w, `{"id":10,"full_name":"org/repo"}`)
		case "/repos/org/repo/pulls/2/reviews":
			w.Header().Set("ETag", `"reviews"`)
			if stage.Load() == 2 {
				if r.Header.Get("If-None-Match") != `"reviews"` {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if r.Header.Get("If-None-Match") != "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if stage.Load() == 0 {
				_, _ = io.WriteString(w, "["+valid+","+invalid+"]")
				return
			}
			_, _ = io.WriteString(w, "["+valid+"]")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewPuller(gh, tasks)
	p.origin = srv.URL
	current := func() store.GithubReviewPull {
		t.Helper()
		rows, err := gh.ListGitHubReviewPulls(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("list pull sources: %d, %v", len(rows), err)
		}
		return rows[0]
	}

	// When part of the response fails, the cursor must not advance.
	if err := p.pull(ctx, current()); err == nil || current().Etag != "" {
		t.Fatalf("failed page advanced checkpoint: %v", err)
	}

	// Then a successful retry saves the cursor and does not repeat the first review.
	stage.Store(1)
	if err := p.pull(ctx, current()); err != nil || current().Etag != `"reviews"` {
		t.Fatalf("successful pull did not save its checkpoint: %v", err)
	}
	stage.Store(2)
	if err := p.pull(ctx, current()); err != nil {
		t.Fatal(err)
	}
	detail, err := tasks.Detail(ctx, "default", task.ID)
	if err != nil || len(detail.Entries) != 1 {
		t.Fatalf("expected one Task entry: %d, %v", len(detail.Entries), err)
	}
}
