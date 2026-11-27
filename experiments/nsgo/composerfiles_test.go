package main

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nsgo-dev/nsgo/core"
)

func TestUserSendsOnlyReadyAttachmentsAndKeepsUnsentFiles(t *testing.T) {
	t.Parallel()
	cx := core.NewRoot(core.RootOptions{})
	t.Cleanup(cx.Dispose)
	files := newComposerFiles(cx, "example", nil)

	// Given two picked files and a third file over the upload limit.
	files.stage([]core.File{{Name: "one.txt"}, {Name: "two.txt"}, {Name: "large.txt", Size: 20<<20 + 1}})
	if !files.Any() || !files.Blocked() || len(files.IDs()) != 0 {
		t.Fatal("pending files must block sending and expose no upload IDs")
	}

	// When both uploads succeed, the rejected file still blocks sending.
	files.update(1, "upload-one", "")
	files.update(2, "upload-two", "")
	if !files.Blocked() {
		t.Fatal("the rejected file must block sending until the user removes it")
	}
	files.remove(3)
	if files.Blocked() || !slices.Equal(files.IDs(), []string{"upload-one", "upload-two"}) {
		t.Fatalf("ready files must allow an attachment-only message in pick order; IDs = %v", files.IDs())
	}

	// Then confirmation removes only the IDs from that message.
	files.Sent([]string{"upload-one"})
	if !files.Any() || !slices.Equal(files.IDs(), []string{"upload-two"}) {
		t.Fatalf("unsent files must stay in the draft; IDs = %v", files.IDs())
	}
}

func TestUserCannotAttachMoreThanTenFiles(t *testing.T) {
	t.Parallel()
	cx := core.NewRoot(core.RootOptions{})
	t.Cleanup(cx.Dispose)
	files := newComposerFiles(cx, "example", nil)

	// Given more than ten picked files, only the first ten can enter the draft.
	picked := make([]core.File, 11)
	for i := range picked {
		picked[i].Name = "probe.txt"
	}
	files.stage(picked)
	if !files.pickerDisabled() || files.notice.Get() == "" {
		t.Fatal("a full draft must explain the limit and prevent further picks")
	}
	for i := 1; i <= 11; i++ {
		files.update(i, "upload", "")
	}
	if got := len(files.IDs()); got != 10 {
		t.Fatalf("sent attachments = %d, want 10", got)
	}
	files.remove(1)
	if files.pickerDisabled() {
		t.Fatal("removing a file must make room for another pick")
	}
}

func TestUserUploadsFileContentsWithoutSendingTheLocalPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "测试.txt")
	const contents = "attachment data\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	// Given a file chosen from the local disk, encode the upload request.
	body, contentType, err := composerFileMultipart(t.Context(), core.File{Name: "测试.txt", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	_, disposition, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	if part.FormName() != "file" || disposition["filename"] != "测试.txt" || string(got) != contents {
		t.Fatalf("upload must contain only the file name and contents, got filename %q and data %q", disposition["filename"], got)
	}

	// A directory and a name containing a local path are not attachments.
	for _, picked := range []core.File{{Name: "folder", Path: dir}, {Name: path, Path: path}} {
		if _, _, err := composerFileMultipart(t.Context(), picked); err == nil {
			t.Fatal("a directory or a path in the file name must be rejected")
		}
	}

	// A file that grew since selection cannot bypass the size limit.
	if err := os.Truncate(path, 20<<20+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := composerFileMultipart(t.Context(), core.File{Name: "测试.txt", Path: path, Size: int64(len(contents))}); !errors.Is(err, composerFileTooLarge) {
		t.Fatalf("a file over the size limit must be rejected, got %v", err)
	}
}
