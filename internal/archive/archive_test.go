package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, name string, content []byte) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(full, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func readArchive(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("decode ZIP: %v", err)
	}
	names := make([]string, 0, len(reader.File))
	for _, f := range reader.File {
		names = append(names, f.Name)
	}
	return names
}

func TestZipDirectoryStreamsFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "project/readme.txt", []byte("readme"))
	writeFile(t, dir, "project/nested/data.txt", []byte("data"))
	root := openRoot(t, dir)
	var buf bytes.Buffer
	if err := ZipDirectory(context.Background(), root, "project", &buf); err != nil {
		t.Fatalf("ZipDirectory: %v", err)
	}
	names := readArchive(t, &buf)
	want := map[string]bool{"project/": true, "project/readme.txt": true, "project/nested/": true, "project/nested/data.txt": true}
	for _, name := range names {
		if !want[name] {
			t.Errorf("unexpected entry %q", name)
		}
	}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %d entries", names, len(want))
	}
}

func TestWriteZipSharesLimitsAndPolicyWithZipDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "top.txt", []byte("top"))
	writeFile(t, dir, "dir/inner.txt", []byte("inner"))
	root := openRoot(t, dir)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	state := State{}
	if err := WriteZip(context.Background(), root, ".", w, &state); err != nil {
		t.Fatalf("WriteZip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if state.Entries != 3 {
		t.Fatalf("state.Entries = %d, want 3", state.Entries)
	}
	if state.Bytes != 8 {
		t.Fatalf("state.Bytes = %d, want 8", state.Bytes)
	}
}

func TestZipDirectoryRejectsSymlinkAndNonDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	writeFile(t, dir, "plain.txt", []byte("plain"))
	root := openRoot(t, dir)
	var buf bytes.Buffer
	if err := ZipDirectory(context.Background(), root, "escape", &buf); err != nil {
		if !errors.Is(err, ErrSymlinkUnsupported) {
			t.Fatalf("symlink error = %v, want ErrSymlinkUnsupported", err)
		}
	} else {
		t.Fatal("symlink was accepted")
	}
	if err := ZipDirectory(context.Background(), root, "plain.txt", &buf); err != nil {
		if !errors.Is(err, ErrNotDirectory) {
			t.Fatalf("non-dir error = %v, want ErrNotDirectory", err)
		}
	} else {
		t.Fatal("non-directory was accepted")
	}
}

func TestWriteZipRejectsSymlinkInsideTree(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	root := openRoot(t, dir)
	w := zip.NewWriter(io.Discard)
	defer w.Close()
	if err := WriteZip(context.Background(), root, ".", w, &State{}); err != nil {
		if !errors.Is(err, ErrSymlinkUnsupported) {
			t.Fatalf("error = %v, want ErrSymlinkUnsupported", err)
		}
	} else {
		t.Fatal("symlink inside tree was accepted")
	}
}

func TestWriteZipAbortsOnByteLimit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tree/data.txt", []byte("data"))
	root := openRoot(t, dir)
	// Pre-seed the byte count so the first file read crosses MaxBytes without
	// allocating a multi-gigabyte file.
	state := State{Bytes: MaxBytes - 1}
	err := WriteZip(context.Background(), root, "tree", zip.NewWriter(io.Discard), &state)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
}

func TestWriteZipAbortsOnEntryLimit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tree/a.txt", []byte("a"))
	writeFile(t, dir, "tree/b.txt", []byte("b"))
	root := openRoot(t, dir)
	// Pre-seed the state one short of the limit so the small tree above pushes
	// it over without creating MaxEntries real files.
	state := State{Entries: MaxEntries - 1}
	err := WriteZip(context.Background(), root, "tree", zip.NewWriter(io.Discard), &state)
	if !errors.Is(err, ErrTooManyEntries) {
		t.Fatalf("error = %v, want ErrTooManyEntries", err)
	}
}
