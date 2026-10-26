//go:build agenteval

// Command agent-eval runs fixture tasks through the dex HTTP API.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

type task struct {
	ID          string            `json:"id"`
	Prompt      string            `json:"prompt"`
	Rubric      string            `json:"rubric"`
	Files       map[string]string `json:"files"`
	Project     bool              `json:"project"`
	Attachment  string            `json:"attachment"`
	Contains    []string          `json:"contains"`
	Tools       []string          `json:"tools"`
	Skill       string            `json:"skill"`
	MinSpawn    int               `json:"minSpawn"`
	MaxSpawn    *int              `json:"maxSpawn"`
	OutputFiles map[string]string `json:"outputFiles"`
	Test        []string          `json:"test"`
}
type check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}
type result struct {
	Task         string                  `json:"task"`
	Repetition   int                     `json:"repetition"`
	Rubric       string                  `json:"rubric"`
	Checks       []check                 `json:"checks"`
	Seconds      float64                 `json:"seconds"`
	Turns        int                     `json:"turns"`
	ToolCalls    int                     `json:"toolCalls"`
	Spawns       int                     `json:"spawns"`
	InputTokens  int                     `json:"inputTokens"`
	OutputTokens int                     `json:"outputTokens"`
	Error        string                  `json:"error,omitempty"`
	Transcript   map[int64][]engine.Item `json:"transcript,omitempty"`
}
type report struct {
	Model       string   `json:"model"`
	Revision    string   `json:"revision"`
	At          string   `json:"at"`
	Repetitions int      `json:"repetitions"`
	Cost        string   `json:"cost"`
	Results     []result `json:"results"`
}
type chat struct {
	ID   int64  `json:"id"`
	Busy bool   `json:"busy"`
	Cwd  string `json:"cwd"`
	Task *struct {
		Parent int64  `json:"parentConversationId"`
		Status string `json:"status"`
	} `json:"task"`
}

type httpError struct {
	status       int
	method, path string
}

func (e *httpError) Error() string { return fmt.Sprintf("%s %s: HTTP %d", e.method, e.path, e.status) }

type client struct {
	base string
	ctx  context.Context
}

func (c client) request(method, path string, body any, out any) error {
	var b io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		b = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(c.ctx, method, c.base+path, b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &httpError{status: resp.StatusCode, method: method, path: path}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.UnmarshalRead(resp.Body, out)
}
func (c client) upload(text string) (string, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, err := w.CreateFormFile("file", "attachment.txt")
	if err != nil {
		return "", err
	}
	if _, err = io.WriteString(p, text); err != nil {
		return "", err
	}
	if err = w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.base+"/api/agents/default/uploads", &b)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("upload: HTTP %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	err = json.UnmarshalRead(resp.Body, &out)
	return out.ID, err
}

// The source is never migrated or opened through db.Open.
func providers(ctx context.Context, path string) ([]sqlc.Provider, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	source, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return sqlc.New(source).ListProviders(ctx)
}
func seed(ctx context.Context, path string, rows []sqlc.Provider) error {
	dest, err := db.Open(ctx, path)
	if err != nil {
		return err
	}
	defer dest.Close()
	q := sqlc.New(dest)
	for _, p := range rows {
		if err = q.UpsertProvider(ctx, sqlc.UpsertProviderParams(p)); err != nil {
			return err
		}
	}
	return nil
}
func command(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // Commands come from versioned eval tasks.
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	b, err := cmd.CombinedOutput()
	return string(b), err
}
func run(ctx context.Context, t task, repetition int, model string, rows []sqlc.Provider, timeout time.Duration) (r result) {
	r = result{Task: t.ID, Repetition: repetition, Rubric: t.Rubric, Transcript: map[int64][]engine.Item{}}
	started := time.Now()
	defer func() {
		r.Seconds = time.Since(started).Seconds()
		r.Checks = append(r.Checks, check{Name: "completed", Pass: r.Error == "", Detail: r.Error})
	}()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fail := func(err error) { r.Error = err.Error() }
	root, err := os.MkdirTemp("", "dex-eval-")
	if err != nil {
		fail(err)
		return
	}
	defer os.RemoveAll(root)
	home, data, project := filepath.Join(root, "home"), filepath.Join(root, "data"), filepath.Join(root, "project")
	for _, p := range []string{home, data, project} {
		if err = os.MkdirAll(p, 0o700); err != nil {
			fail(err)
			return
		}
	}
	for path, content := range t.Files {
		target := filepath.Join(project, path)
		if !filepath.IsLocal(path) {
			fail(fmt.Errorf("nonlocal fixture path %q", path))
			return
		}
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			fail(err)
			return
		}
		if err = os.WriteFile(target, []byte(content), 0o600); err != nil {
			fail(err)
			return
		}
	}
	if _, err = command(ctx, project, "git", "init", "-q"); err != nil {
		fail(err)
		return
	}
	if err = seed(ctx, filepath.Join(data, "control.sqlite"), rows); err != nil {
		fail(err)
		return
	}
	a, err := app.Start(ctx, app.Config{DataDir: data, Home: home, ToolEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home, "TMPDIR": root}, DreamEvery: 24 * time.Hour})
	if err != nil {
		fail(err)
		return
	}
	defer func() { //nolint:contextcheck // Close joins the app lifetime.
		if err := a.Close(); err != nil {
			fail(err)
		}
	}()
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	c := client{base: server.URL, ctx: ctx}
	provider, id, _ := strings.Cut(model, "/")
	// Model discovery is asynchronous. Retry only its documented pending status.
	for {
		err = c.request("PUT", "/api/agents/default", map[string]any{"name": "Eval", "provider": provider, "modelId": id, "thinkingLevel": "off", "instructions": ""}, nil)
		if err == nil {
			break
		}
		if status, ok := errors.AsType[*httpError](err); !ok || status.status != http.StatusServiceUnavailable {
			fail(err)
			return
		}
		select {
		case <-ctx.Done():
			fail(err)
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	var ch chat
	if err = c.request("POST", "/api/agents/default/chats", nil, &ch); err != nil {
		fail(err)
		return
	}
	path := "/api/agents/default/chats/" + strconv.FormatInt(ch.ID, 10)
	if t.Project {
		if err = c.request("PUT", path+"/project", map[string]string{"path": project}, nil); err != nil {
			fail(err)
			return
		}
	}
	attachments := []string{}
	if t.Attachment != "" {
		var id string
		id, err = c.upload(t.Attachment)
		if err != nil {
			fail(err)
			return
		}
		attachments = append(attachments, id)
	}
	prompt := strings.ReplaceAll(t.Prompt, "{{project}}", project)
	if err = c.request("POST", path+"/messages", map[string]any{"id": uuid.New().String(), "text": prompt, "attachments": attachments}, nil); err != nil {
		fail(err)
		return
	}
	ids := []int64{ch.ID}
	for {
		var all struct {
			Chats []chat `json:"chats"`
		}
		if err = c.request("GET", "/api/agents/default/chats", nil, &all); err != nil {
			fail(err)
			break
		}
		busy := false
		for _, v := range all.Chats {
			if v.ID == ch.ID || (v.Task != nil && v.Task.Parent == ch.ID) {
				if !slices.Contains(ids, v.ID) {
					ids = append(ids, v.ID)
				}
				busy = busy || v.Busy || (v.Task != nil && (v.Task.Status == "queued" || v.Task.Status == "running"))
			}
		}
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
			fail(ctx.Err())
		case <-time.After(250 * time.Millisecond):
			continue
		}
		break
	}
	if ctx.Err() != nil {
		recovery, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer stop()
		c.ctx = recovery
		_ = c.request("POST", path+"/abort", nil, nil)
	}
	for _, id := range ids {
		before := int64(9223372036854775807)
		for {
			var page engine.History
			p := "/api/agents/default/chats/" + strconv.FormatInt(id, 10) + "/history?limit=200&before=" + strconv.FormatInt(before, 10)
			if err = c.request("GET", p, nil, &page); err != nil {
				fail(err)
				break
			}
			r.Transcript[id] = append(page.Items, r.Transcript[id]...)
			if page.NextBefore == 0 {
				break
			}
			before = page.NextBefore
		}
	}
	var answer strings.Builder
	tools := map[string]int{}
	skillRead := false
	turnIDs := map[string]bool{}
	for id, items := range r.Transcript {
		for _, item := range items {
			if item.Kind == "assistant" || item.Kind == "tool" || item.Kind == "thinking" {
				turnIDs[fmt.Sprintf("%d/%d", id, item.EntryID)] = true
			}
			switch item.Kind {
			case "assistant":
				if id == ch.ID {
					answer.WriteString("\n" + item.Text)
				}
				if item.Usage != nil {
					r.InputTokens += item.Usage.Input
					r.OutputTokens += item.Usage.Output
				}
			case "tool":
				r.ToolCalls++
				tools[item.Name]++
				if item.Name == "spawn_task" {
					r.Spawns++
				}
				if item.Name == "read" && t.Skill != "" {
					args, _ := json.Marshal(item.Args)
					skillRead = skillRead || strings.Contains(string(args), t.Skill)
				}
				if item.Name == "bash" && t.Skill != "" {
					for path, content := range t.Files {
						if strings.HasSuffix(path, t.Skill) && strings.Contains(item.Output, content) {
							skillRead = true
						}
					}
				}
			case "error":
				r.Checks = append(r.Checks, check{Name: "no execution error", Pass: false, Detail: item.Reason})
			}
		}
	}
	r.Turns = len(turnIDs)
	for _, s := range t.Contains {
		r.Checks = append(r.Checks, check{Name: "answer contains " + s, Pass: strings.Contains(strings.ToLower(answer.String()), strings.ToLower(s))})
	}
	for _, name := range t.Tools {
		r.Checks = append(r.Checks, check{Name: "tool " + name, Pass: tools[name] > 0})
	}
	if t.Skill != "" {
		r.Checks = append(r.Checks, check{Name: "project skill read", Pass: skillRead})
	}
	if t.MinSpawn > 0 {
		r.Checks = append(r.Checks, check{Name: "minimum spawns", Pass: r.Spawns >= t.MinSpawn})
	}
	if t.MaxSpawn != nil {
		r.Checks = append(r.Checks, check{Name: "maximum spawns", Pass: r.Spawns <= *t.MaxSpawn})
	}
	for path, want := range t.OutputFiles {
		b, err := os.ReadFile(filepath.Join(project, path)) //nolint:gosec // Paths come from versioned eval tasks.
		r.Checks = append(r.Checks, check{Name: "file " + path, Pass: err == nil && strings.Contains(string(b), want)})
	}
	for path, original := range t.Files {
		if strings.HasSuffix(path, "_test.go") || path == "go.mod" {
			b, err := os.ReadFile(filepath.Join(project, path)) //nolint:gosec // Paths come from versioned eval tasks.
			r.Checks = append(r.Checks, check{Name: "fixture unchanged " + path, Pass: err == nil && string(b) == original})
		}
	}
	if len(t.Test) > 0 {
		output, err := command(ctx, project, t.Test...)
		r.Checks = append(r.Checks, check{Name: "fixture test", Pass: err == nil, Detail: output})
	}
	if slices.Contains(t.Tools, "set_project") {
		var saved chat
		err = c.request("GET", path, nil, &saved)
		r.Checks = append(r.Checks, check{Name: "saved project", Pass: err == nil && saved.Cwd == project})
	}
	return
}
func passed(r result) bool {
	for _, c := range r.Checks {
		if !c.Pass {
			return false
		}
	}
	return len(r.Checks) > 0
}
func save(path string, v any, secrets []string) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	for _, key := range secrets {
		if key != "" {
			encoded, _ := json.Marshal(key)
			b = bytes.ReplaceAll(b, encoded[1:len(encoded)-1], []byte("[REDACTED]"))
		}
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
func main() {
	if err := mainRun(); err != nil {
		fmt.Fprintln(os.Stderr, "agent eval failed:", err)
		os.Exit(1)
	}
}
func mainRun() error {
	source := flag.String("providers-from", "", "Read provider rows from this control.sqlite (read-only)")
	model := flag.String("model", "magpie/tokenflux/deepseek-flash", "Provider/model ID")
	n := flag.Int("n", 2, "Repetitions per task")
	baseline := flag.String("baseline", "evals/baseline.json", "Summary to compare")
	summary := flag.String("save-summary", "", "Write a summary without transcripts")
	filter := flag.String("task", "", "Run one task ID")
	timeout := flag.Duration("timeout", 10*time.Minute, "Deadline per task")
	flag.Parse()
	provider, id, ok := strings.Cut(*model, "/")
	if *source == "" || *n < 1 || !ok || provider == "" || id == "" {
		return errors.New("--providers-from and a provider/model are required; n must be positive")
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	rows, err := providers(ctx, *source)
	if err != nil {
		return errors.New("cannot read provider rows from source database")
	}
	found := false
	var secrets []string
	for _, p := range rows {
		found = found || p.ID == provider
		if p.ApiKey != "" {
			secrets = append(secrets, p.ApiKey)
		}
	}
	if !found {
		return errors.New("selected provider is absent from source database")
	}
	files, err := filepath.Glob("evals/tasks/*.json")
	if err != nil {
		return err
	}
	var tasks []task
	for _, path := range files {
		b, err := os.ReadFile(path) //nolint:gosec // Reads versioned tasks from evals/tasks.
		if err != nil {
			return err
		}
		var t task
		if err = json.Unmarshal(b, &t); err != nil {
			return err
		}
		if *filter == "" || *filter == t.ID {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) == 0 {
		return errors.New("no eval tasks selected")
	}
	revision, _ := command(ctx, ".", "git", "rev-parse", "HEAD")
	rep := report{Model: *model, Revision: strings.TrimSpace(revision), At: time.Now().UTC().Format(time.RFC3339Nano), Repetitions: *n, Cost: "unavailable; tokens cover only turns with text in the HTTP transcript"}
	dir := filepath.Join(".local/evals", time.Now().UTC().Format("20060102T150405.000000000"))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, t := range tasks {
		for i := range *n {
			r := run(ctx, t, i+1, *model, rows, *timeout)
			rep.Results = append(rep.Results, r)
			fmt.Printf("%s %d/%d pass=%t time=%.1fs turns=%d tools=%d spawn=%d tokens=%d/%d\n", t.ID, i+1, *n, passed(r), r.Seconds, r.Turns, r.ToolCalls, r.Spawns, r.InputTokens, r.OutputTokens)
			if err = save(filepath.Join(dir, "results.json"), rep, secrets); err != nil {
				return err
			}
		}
	}
	var old report
	if b, e := os.ReadFile(*baseline); e == nil {
		if err = json.Unmarshal(b, &old); err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Agent eval\n\nModel: `%s`  Revision: `%s`\n\nCost: %s.\n\n| Task/run | Pass | Baseline | Flipped checks | Seconds | Turns | Tools | Spawns | Input/output tokens |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n", rep.Model, rep.Revision, rep.Cost)
	for _, r := range rep.Results {
		base := "absent"
		var flips []string
		for _, b := range old.Results {
			if b.Task == r.Task && b.Repetition == r.Repetition {
				base = strconv.FormatBool(passed(b))
				for _, c := range r.Checks {
					for _, bc := range b.Checks {
						if c.Name == bc.Name && c.Pass != bc.Pass {
							flips = append(flips, fmt.Sprintf("**%s: %t → %t**", c.Name, bc.Pass, c.Pass))
						}
					}
				}
			}
		}
		fmt.Fprintf(&md, "| %s/%d | %t | %s | %s | %.1f | %d | %d | %d | %d/%d |\n", r.Task, r.Repetition, passed(r), base, strings.Join(flips, "; "), r.Seconds, r.Turns, r.ToolCalls, r.Spawns, r.InputTokens, r.OutputTokens)
	}
	if err = os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0o600); err != nil {
		return err
	}
	fmt.Print(md.String())
	fmt.Println("Artifacts:", dir)
	if *summary != "" {
		for i := range rep.Results {
			rep.Results[i].Transcript = nil
		}
		if err = save(*summary, rep, secrets); err != nil {
			return err
		}
	}
	return nil
}
