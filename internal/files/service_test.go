package files

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cows-project/cows/internal/workspace"
)

type fakeMountResolver struct {
	mounts []workspace.FileMount
}

func (f fakeMountResolver) ListFileMounts(context.Context, string, string) ([]workspace.FileMount, error) {
	return f.mounts, nil
}

func newFileService(t *testing.T, readOnly bool) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	service := New(fakeMountResolver{mounts: []workspace.FileMount{{Name: "data", ContainerPath: "/data", Root: root, ReadOnly: readOnly}}})
	return service, root
}

func TestListAndMutateFilesInsideApprovedRoot(t *testing.T) {
	service, root := newFileService(t, false)
	ctx := context.Background()
	listing, err := service.List(ctx, "user-1", "workspace-1", "data", ".")
	if err != nil || len(listing.Items) != 1 || listing.Items[0].Name != "hello.txt" {
		t.Fatalf("list files: %+v, %v", listing, err)
	}
	if err := service.CreateDirectory(ctx, "user-1", "workspace-1", "data", ".", "folder"); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	if err := service.Upload(ctx, "user-1", "workspace-1", "data", "folder", "upload.txt", strings.NewReader("uploaded")); err != nil {
		t.Fatalf("upload file: %v", err)
	}
	if err := service.Rename(ctx, "user-1", "workspace-1", "data", "folder", "upload.txt", "renamed.txt"); err != nil {
		t.Fatalf("rename file: %v", err)
	}
	if err := service.Delete(ctx, "user-1", "workspace-1", "data", "folder/renamed.txt"); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "folder", "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists: %v", err)
	}
}

func TestRejectsTraversalAndReadOnlyMutation(t *testing.T) {
	service, _ := newFileService(t, true)
	ctx := context.Background()
	if _, err := service.List(ctx, "user-1", "workspace-1", "data", "../outside"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("traversal error = %v", err)
	}
	if err := service.CreateDirectory(ctx, "user-1", "workspace-1", "data", ".", "new"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only mkdir error = %v", err)
	}
	if err := service.Upload(ctx, "user-1", "workspace-1", "data", ".", "new.txt", strings.NewReader("new")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only upload error = %v", err)
	}
}

func TestRootedDownloadRejectsSymlinkEscape(t *testing.T) {
	service, root := newFileService(t, false)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, _, err := service.OpenDownload(context.Background(), "user-1", "workspace-1", "data", "escape"); err == nil {
		t.Fatal("symlink escape was allowed")
	}
}

func TestOpenZipStreamsDirectoryContents(t *testing.T) {
	service, root := newFileService(t, true)
	if err := os.Mkdir(filepath.Join(root, "project"), 0o700); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "project", "readme.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write project file: %v", err)
	}
	reader, filename, err := service.OpenZip(context.Background(), "user-1", "workspace-1", "data", "project")
	if err != nil {
		t.Fatalf("open ZIP: %v", err)
	}
	data, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read ZIP: read=%v close=%v", err, closeErr)
	}
	if filename != "project.zip" {
		t.Fatalf("ZIP filename = %q", filename)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read ZIP archive: %v", err)
	}
	if len(archive.File) != 2 {
		t.Fatalf("ZIP entries = %d, want directory and file", len(archive.File))
	}
	var fileEntry *zip.File
	for _, entry := range archive.File {
		if entry.Name == "project/readme.txt" {
			fileEntry = entry
			break
		}
	}
	if fileEntry == nil {
		t.Fatal("ZIP did not contain project/readme.txt")
	}
	file, err := fileEntry.Open()
	if err != nil {
		t.Fatalf("open ZIP file: %v", err)
	}
	contents, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(contents) != "hello" {
		t.Fatalf("ZIP file contents = %q, error = %v", contents, err)
	}
}

func TestOpenZipRejectsFiles(t *testing.T) {
	service, _ := newFileService(t, true)
	if _, _, err := service.OpenZip(context.Background(), "user-1", "workspace-1", "data", "hello.txt"); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file ZIP error = %v", err)
	}
}

func TestUploadLimitIsEnforced(t *testing.T) {
	service, _ := newFileService(t, false)
	source := io.LimitReader(repeatingReader{}, MaxUploadBytes+1)
	if err := service.Upload(context.Background(), "user-1", "workspace-1", "data", ".", "large.txt", source); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("large upload error = %v", err)
	}
}

type repeatingReader struct{}

func (repeatingReader) Read(value []byte) (int, error) {
	for index := range value {
		value[index] = 'x'
	}
	return len(value), nil
}
