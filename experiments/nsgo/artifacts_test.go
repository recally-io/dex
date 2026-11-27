package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/nsgo-dev/nsgo/ui"
)

func TestUserPreviewsMarkdownAsPlainText(t *testing.T) {
	t.Parallel()

	// Given Markdown that contains HTML and a remote image.
	const source = "# 报告\n<script>alert(1)</script>\n![跟踪](https://example.invalid/tracker.png)\n"

	// When the user previews it, the content remains literal text.
	got, err := artifactsReadText(strings.NewReader(source))
	if err != nil || got != source {
		t.Fatalf("preview = %q, %v; want unchanged source", got, err)
	}
}

func TestUserManagesArtifactsWithoutLeavingMain(t *testing.T) {
	sessionsRequireIsolatedGateway(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	botID := sessionsFixtureBot(t, ctx)
	var health struct {
		DataDir string `json:"dataDir"`
	}
	if err := requestJSON(ctx, http.MethodGet, "/api/health", nil, &health); err != nil {
		t.Fatal(err)
	}
	data, err := filepath.EvalSymlinks(health.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(temp, data)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if err != nil || len(parts) != 2 || !strings.HasPrefix(parts[0], "tmp.") || parts[1] != "data" || filepath.Base(botID) != botID {
		t.Fatal("fixture files must stay inside the disposable Dex data directory")
	}
	dir := filepath.Join(data, "agents", botID, "artifacts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Report.md"), []byte("# Synthetic artifact\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Given Main with a real artifact and a button that opens its dialog.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		open := core.NewSignal(cx, false)
		draft := core.NewSignal(cx, "")
		return core.View{Children: []core.Node{
			core.Text{Content: "Main conversation"},
			ui.Button{TestID: "artifacts-test-open", Text: "Artifacts", OnPress: func() { open.Set(true) }},
			core.Text{TestID: "artifacts-test-draft", ContentFunc: draft.Get},
			core.Show{When: open.Get, Then: func(cx *core.Scope) core.Node {
				return artifactsPanel(cx, botID, 0, func() { open.Set(false) }, func(id int, text string) {
					if id == 0 {
						draft.Set(text)
					}
				})
			}},
		}}
	}, nsgotest.Size(390, 820))

	// When the user chooses an artifact, they can preview and rename the real file.
	app.Press("id:artifacts-test-open")
	app.WaitFor("id:artifacts-panel", sessionSurfaceTimeout)
	app.WaitFor("id:artifacts-open-Report.md", sessionSurfaceTimeout)
	app.Press("id:artifacts-open-Report.md")
	app.WaitFor("text:Synthetic artifact", sessionSurfaceTimeout)
	app.Press("id:artifacts-rename")
	app.Clear("id:artifacts-name")
	app.Type("id:artifacts-name", "Final.md")
	app.Press("id:artifacts-save-name")
	app.WaitFor("id:artifacts-open-Final.md", sessionSurfaceTimeout)

	// Then Continue editing closes the dialog and places a draft in Main without sending it.
	app.Press("id:artifacts-continue")
	app.WaitForGone("id:artifacts-panel", sessionSurfaceTimeout)
	app.Find("text:Main conversation")
	if got := app.Find("id:artifacts-test-draft").Text; !strings.Contains(got, "Final.md") {
		t.Fatalf("wrong pending draft: %q", got)
	}

	// Deletion changes the real file only after explicit confirmation.
	app.Press("id:artifacts-test-open")
	app.WaitFor("id:artifacts-open-Final.md", sessionSurfaceTimeout)
	app.Press("id:artifacts-open-Final.md")
	app.Press("id:artifacts-delete")
	app.Press("id:artifacts-delete-confirm")
	app.WaitFor("text:No artifacts yet.", sessionSurfaceTimeout)
	if _, err := os.Stat(filepath.Join(dir, "Final.md")); !os.IsNotExist(err) {
		t.Fatalf("artifact still on disk: %v", err)
	}
}

func TestUserDoesNotPreviewUnsafeOrOversizedFiles(t *testing.T) {
	t.Parallel()

	// Given a suspicious name, a binary file with a text suffix or a file that
	// grows after listing, the preview rejects it instead of loading it as text.
	for _, name := range []string{"../notes.txt", "../report.md", `..\report.md`, "script.html", "image.svg", "archive.zip"} {
		if artifactsCanPreview(artifactsFile{Name: name}) {
			t.Errorf("unexpected preview action for %q", name)
		}
	}
	if !artifactsCanPreview(artifactsFile{Name: "报告 #1.MD", Size: 42}) {
		t.Error("Markdown with a Unicode name must have a preview action")
	}
	if artifactsCanPreview(artifactsFile{Name: "长文.txt", Size: 1 << 20}) {
		t.Error("oversized listed file must not have a preview action")
	}
	for _, content := range []string{"a\x00b", "\xff", strings.Repeat("a", 1<<20)} {
		if _, err := artifactsReadText(strings.NewReader(content)); err == nil {
			t.Errorf("accepted unsafe preview with %d bytes", len(content))
		}
	}
}
