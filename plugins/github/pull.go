package github

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/plugins/github/store"
)

const maxReviewPages = 20

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

// Puller polls configured PRs and saves the last accepted single-page ETag.
type Puller struct {
	q      *store.Queries
	tasks  *tasksvc.Service
	client *http.Client
	origin string
}

// NewPuller configures a client that sends credentials only to the GitHub API.
func NewPuller(q *store.Queries, tasks *tasksvc.Service) *Puller {
	return &Puller{q: q, tasks: tasks, origin: "https://api.github.com", client: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Check processes each saved source once. It continues when one source fails.
func (p *Puller) Check(ctx context.Context) {
	rows, err := p.q.ListGitHubReviewPulls(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("list GitHub review pulls", "err", err)
		}
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := p.pull(ctx, row); err != nil && ctx.Err() == nil {
			slog.Warn("pull GitHub reviews", "repository", row.RepositoryID, "pull", row.PullNumber, "err", err)
		}
	}
}

func (p *Puller) pull(ctx context.Context, row store.PluginGithubReviewPull) error {
	body, _, _, err := p.get(ctx, fmt.Sprintf("/repositories/%d", row.RepositoryID), row.Token, "")
	if err != nil {
		return err
	}
	var repo struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(body, &repo); err != nil || repo.ID != row.RepositoryID || !repositoryName.MatchString(repo.FullName) || strings.HasSuffix(repo.FullName, "/.") || strings.HasSuffix(repo.FullName, "/..") {
		return errors.New("GitHub returned the wrong repository")
	}
	rule, err := p.rule(ctx, row.RepositoryID)
	if err != nil {
		return err
	}
	for page := 1; page <= maxReviewPages; page++ {
		previous := ""
		if page == 1 {
			previous = row.Etag
		}
		path := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100&page=%d", repo.FullName, row.PullNumber, page)
		body, headers, unchanged, err := p.get(ctx, path, row.Token, previous)
		if err != nil {
			return err
		}
		if unchanged {
			return nil
		}
		_, count, err := acceptReviewPage(ctx, p.tasks, row.RepositoryID, row.PullNumber, body, rule)
		if err != nil {
			return err
		}
		if strings.Contains(headers.Get("Link"), `rel="next"`) {
			continue
		}
		next := ""
		// A full first page can gain a second page without changing its ETag.
		if page == 1 && count < 100 {
			next = headers.Get("ETag")
		}
		return p.q.AdvanceGitHubReviewPull(ctx, store.AdvanceGitHubReviewPullParams{RepositoryID: row.RepositoryID, PullNumber: row.PullNumber, ConfigurationID: row.ConfigurationID, PreviousEtag: row.Etag, NextEtag: next})
	}
	return errors.New("GitHub review page limit exceeded")
}

// rule reads the Webhook rule of the repository, so Pull and the Webhook hold the same reviews.
// Without a Webhook configuration Pull holds nothing.
func (p *Puller) rule(ctx context.Context, repositoryID int64) (Rule, error) {
	config, err := p.q.GetGitHubWebhook(ctx, repositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, nil
	}
	return Rule{HoldUnmatched: config.HoldUnmatched == 1}, err
}

func (p *Puller) get(ctx context.Context, path, token, etag string) ([]byte, http.Header, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.origin+path, nil)
	if err != nil {
		return nil, nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && etag != "" {
		return nil, nil, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, false, fmt.Errorf("GitHub API status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxPayload+1))
	if err != nil {
		return nil, nil, false, err
	}
	if len(body) > MaxPayload {
		return nil, nil, false, ErrPayload
	}
	return body, resp.Header, false, nil
}
