package artifact_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/artifact"
)

const diskID = "01900000-0000-7000-8000-000000000001"

func diskFixture(t *testing.T) (string, string, *artifact.Service) {
	t.Helper()
	data := t.TempDir()
	file := filepath.Join(data, "artifacts", "1_2", diskID, "report.md")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	return data, file, artifact.New(nil, data)
}

func TestUserReadsTheActualSnapshotFile(t *testing.T) {
	// Given a disk-only delivery without a database or a sidecar index.
	_, file, service := diskFixture(t)
	// When the physical file is renamed and changed, both conversation views read it.
	renamed := filepath.Join(filepath.Dir(file), "actual.md")
	if err := os.Rename(file, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renamed, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []int64{1, 2} {
		rows, err := service.List(t.Context(), chat)
		if err != nil || len(rows) != 1 || rows[0].Name != "actual.md" || rows[0].Size != 7 {
			t.Fatalf("files=%+v err=%v", rows, err)
		}
	}
	// Then an unrelated chat cannot list or mutate it.
	if rows, err := service.List(t.Context(), 3); err != nil || len(rows) != 0 {
		t.Fatalf("unrelated files=%+v err=%v", rows, err)
	}
	if err := service.Delete(t.Context(), 3, diskID); err == nil {
		t.Fatal("unrelated chat deleted a snapshot")
	}
}

func TestUserCannotDeleteSourcesThroughSnapshotPaths(t *testing.T) {
	data, file, service := diskFixture(t)
	outside := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(outside, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Fatal(err)
	}
	if rows, err := service.List(t.Context(), 1); err != nil || len(rows) != 0 {
		t.Fatalf("symlink files=%+v err=%v", rows, err)
	}
	if _, f, err := service.Open(t.Context(), 1, diskID); err == nil {
		_ = f.Close()
		t.Fatal("opened a snapshot symlink")
	}
	if err := service.Delete(t.Context(), 1, diskID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(t.Context(), 1, "../../source.md"); err == nil {
		t.Fatal("accepted a path as an ID")
	}
	if _, err := service.Rename(t.Context(), 1, diskID, "../source.md"); err == nil {
		t.Fatal("accepted a path as a name")
	}
	// Replacing the complete artifacts directory with a link cannot widen the boundary.
	if err := os.Rename(filepath.Join(data, "artifacts"), filepath.Join(data, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(data, "artifacts")); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(t.Context(), 1, diskID); err == nil {
		t.Fatal("accepted a replaced artifacts root")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "source" {
		t.Fatalf("source=%q err=%v", body, err)
	}
}

func TestUserDoesNotLoseFilesWhenADirectoryContainsTwoFiles(t *testing.T) {
	_, file, service := diskFixture(t)
	second := filepath.Join(filepath.Dir(file), "second.md")
	if err := os.WriteFile(second, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	// An ambiguous snapshot is reported, never resolved by deleting or overwriting one file.
	if err := service.Delete(t.Context(), 1, diskID); err == nil {
		t.Fatal("deleted an ambiguous snapshot")
	}
	if _, err := service.Rename(t.Context(), 1, diskID, "second.md"); err == nil {
		t.Fatal("overwrote an existing file")
	}
	for _, path := range []string{file, second} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}
