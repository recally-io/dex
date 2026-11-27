package main

import (
	"strings"
	"testing"
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
