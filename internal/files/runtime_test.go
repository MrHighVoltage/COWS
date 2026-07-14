package files

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/runtime"
	"github.com/cows-project/cows/internal/workspace"
)

func TestServiceUsesRuntimeFileAccessForMountedRoots(t *testing.T) {
	fake := &fakeFileAccess{
		entries: []runtime.FileEntry{{Name: "hello.txt", Size: 5}},
		content: "hello",
	}
	service := New(fakeMountResolver{mounts: []workspace.FileMount{{
		Name: "data", RuntimeID: "runtime-1", MountType: "volume", Source: "volume-1", ContainerUID: 1000, ContainerGID: 1000,
	}}}, fakeFileAccessRuntime{access: fake})

	listing, err := service.List(context.Background(), "user-1", "workspace-1", "data", ".")
	if err != nil || len(listing.Items) != 1 || listing.Items[0].Name != "hello.txt" {
		t.Fatalf("runtime listing = %+v, error = %v", listing, err)
	}
	file, info, err := service.OpenDownload(context.Background(), "user-1", "workspace-1", "data", "hello.txt")
	if err != nil {
		t.Fatalf("runtime download: %v", err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || string(data) != "hello" || info.Size() != 5 {
		t.Fatalf("runtime download = %q, info=%v, read=%v, close=%v", data, info.Size(), readErr, closeErr)
	}
	if err := service.Upload(context.Background(), "user-1", "workspace-1", "data", ".", "new.txt", strings.NewReader("new")); err != nil {
		t.Fatalf("runtime upload: %v", err)
	}
	if fake.uploaded != "new.txt:new" {
		t.Fatalf("runtime upload = %q", fake.uploaded)
	}
}

type fakeFileAccessRuntime struct{ access runtime.FileAccess }

func (f fakeFileAccessRuntime) OpenFileAccess(context.Context, runtime.FileAccessSpec) (runtime.FileAccess, error) {
	return f.access, nil
}

type fakeFileAccess struct {
	entries  []runtime.FileEntry
	content  string
	uploaded string
}

func (f *fakeFileAccess) Close() error { return nil }

func (f *fakeFileAccess) List(context.Context, string) ([]runtime.FileEntry, error) {
	return f.entries, nil
}

func (f *fakeFileAccess) Stat(context.Context, string) (fs.FileInfo, error) {
	return fakeRuntimeInfo{name: "hello.txt", size: int64(len(f.content))}, nil
}

func (f *fakeFileAccess) OpenRead(context.Context, string) (io.ReadCloser, fs.FileInfo, error) {
	return io.NopCloser(strings.NewReader(f.content)), fakeRuntimeInfo{name: "hello.txt", size: int64(len(f.content))}, nil
}

func (*fakeFileAccess) OpenZip(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (*fakeFileAccess) CreateDirectory(context.Context, string, string) error { return nil }
func (*fakeFileAccess) Delete(context.Context, string) error                  { return nil }
func (*fakeFileAccess) Rename(context.Context, string, string, string) error  { return nil }

func (f *fakeFileAccess) Upload(_ context.Context, _ string, filename string, source io.Reader) error {
	data, err := io.ReadAll(source)
	if err == nil {
		f.uploaded = filename + ":" + string(data)
	}
	return err
}

type fakeRuntimeInfo struct {
	name string
	size int64
}

func (f fakeRuntimeInfo) Name() string       { return f.name }
func (f fakeRuntimeInfo) Size() int64        { return f.size }
func (f fakeRuntimeInfo) Mode() fs.FileMode  { return 0o600 }
func (f fakeRuntimeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeRuntimeInfo) IsDir() bool        { return false }
func (f fakeRuntimeInfo) Sys() any           { return nil }
