package fileagent

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestRunUsesRootedOperationsAndStreamsArchives(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "project"), 0o700); err != nil {
		t.Fatalf("make directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "project", "readme.txt"), []byte("readme"), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	identity := []string{"--uid", strconv.Itoa(os.Getuid()), "--gid", strconv.Itoa(os.Getgid()), "--root", root}
	args := append(identity, "--op", "list", "--path", ".")
	var listing bytes.Buffer
	if err := runWithCurrentDirectory(args, nil, &listing); err != nil {
		t.Fatalf("list: %v", err)
	}
	var entries []entry
	if err := json.Unmarshal(listing.Bytes(), &entries); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("listing entries = %d, want 2", len(entries))
	}

	var archive bytes.Buffer
	if err := runWithCurrentDirectory(append(identity, "--op", "zip", "--path", "project"), nil, &archive); err != nil {
		t.Fatalf("zip: %v", err)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatalf("decode ZIP: %v", err)
	}
	if len(zipReader.File) != 2 {
		t.Fatalf("ZIP entries = %d, want 2", len(zipReader.File))
	}

	var download bytes.Buffer
	if err := runWithCurrentDirectory(append(identity, "--op", "download", "--path", "hello.txt"), nil, &download); err != nil {
		t.Fatalf("download: %v", err)
	}
	if download.String() != "hello" {
		t.Fatalf("download = %q", download.String())
	}
}

func TestUsageCountsLogicalFileBytesAndRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "data"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := range 300 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(index)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var output bytes.Buffer
	if err := usage(root, &output); err != nil || output.String() != "307\n" {
		t.Fatalf("usage=%q error=%v, want 307 logical bytes", output.String(), err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := usage(root, &output); err == nil || output.Len() != 0 {
		t.Fatalf("symlink usage=%q error=%v, want failure without a measurement", output.String(), err)
	}
}

func TestRunRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	base := []string{"--uid", strconv.Itoa(os.Getuid()), "--gid", strconv.Itoa(os.Getgid()), "--root", root, "--op", "stat"}
	if err := runWithCurrentDirectory(append(base, "--path", "../outside.txt"), nil, io.Discard); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if err := runWithCurrentDirectory(append(base, "--path", "escape"), nil, io.Discard); err == nil {
		t.Fatal("symlink was accepted")
	}
}

func TestListingRejectsSymlinkedBaseDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("directory", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var output bytes.Buffer
	if err := list(root, "directory", &output); err != nil {
		t.Fatalf("ordinary directory listing failed: %v", err)
	}
	output.Reset()
	if err := list(root, "alias", &output); err == nil || output.Len() != 0 {
		t.Fatalf("symlinked listing base error=%v output=%q", err, output.String())
	}
}

func runWithCurrentDirectory(args []string, input io.Reader, output io.Writer) error {
	current, err := os.Getwd()
	if err != nil {
		return err
	}
	defer os.Chdir(current)
	return Run(args, inputOrEmpty(input), output, io.Discard)
}

func inputOrEmpty(input io.Reader) io.Reader {
	if input == nil {
		return bytes.NewReader(nil)
	}
	return input
}
