package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cows-project/cows/internal/runtime"
)

type fileAccess struct {
	adapter *Adapter
	spec    runtime.FileAccessSpec
	root    string
}

func (a *Adapter) OpenFileAccess(ctx context.Context, spec runtime.FileAccessSpec) (runtime.FileAccess, error) {
	if spec.ContainerUID < 0 || spec.ContainerUID > 2147483647 || spec.ContainerGID < 0 || spec.ContainerGID > 2147483647 {
		return nil, runtime.ErrConflict
	}
	if spec.MountType != "directory" && spec.MountType != "volume" {
		return nil, runtime.ErrConflict
	}
	if a.helperExecutable == "" {
		return nil, fmt.Errorf("%w: COWS executable path is unavailable", runtime.ErrNotSupported)
	}
	root := spec.Source
	if spec.MountType == "volume" {
		var err error
		root, err = a.volumePath(ctx, spec.Source)
		if err != nil {
			return nil, err
		}
	}
	if !isCleanAbsolutePath(root) {
		return nil, fmt.Errorf("%w: runtime returned invalid file source", runtime.ErrInvalidObservation)
	}
	info, err := a.hostInfo(ctx)
	if err != nil {
		return nil, err
	}
	if !info.Rootless {
		return nil, fmt.Errorf("%w: host file access is only implemented for rootless Podman", runtime.ErrNotSupported)
	}
	return &fileAccess{adapter: a, spec: spec, root: root}, nil
}

func (a *Adapter) volumePath(ctx context.Context, name string) (string, error) {
	if !validWorkspaceID(name) {
		return "", runtime.ErrConflict
	}
	var response struct {
		Mountpoint string `json:"mountpoint"`
	}
	if err := a.get(ctx, "/libpod/volumes/"+url.PathEscape(name)+"/json", &response); err != nil {
		return "", err
	}
	if !isCleanAbsolutePath(response.Mountpoint) {
		return "", fmt.Errorf("%w: Podman returned invalid volume mountpoint", runtime.ErrInvalidObservation)
	}
	return response.Mountpoint, nil
}

func (a *Adapter) fileCommand(ctx context.Context, access *fileAccess, operation, relativePath string, values ...string) (*exec.Cmd, error) {
	if !isCleanAbsolutePath(access.root) {
		return nil, runtime.ErrConflict
	}
	args := []string{"unshare", access.adapter.helperExecutable, "file-helper", "--uid", strconv.FormatInt(access.spec.ContainerUID, 10), "--gid", strconv.FormatInt(access.spec.ContainerGID, 10), "--root", access.root, "--op", operation, "--path", relativePath}
	if access.spec.ReadOnly {
		args = append(args, "--read-only")
	}
	for _, value := range values {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, runtime.ErrConflict
		}
		args = append(args, value)
	}
	return exec.CommandContext(ctx, access.adapter.podmanBinary, args...), nil
}

func (a *fileAccess) List(ctx context.Context, relativePath string) ([]runtime.FileEntry, error) {
	command, err := a.adapter.fileCommand(ctx, a, "list", relativePath)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, helperErrorText(stderr.String(), err)
	}
	var entries []runtime.FileEntry
	if err := json.Unmarshal(output, &entries); err != nil {
		return nil, fmt.Errorf("%w: invalid file-helper listing", runtime.ErrInvalidObservation)
	}
	return entries, nil
}

func (a *fileAccess) Usage(ctx context.Context) (int64, error) {
	command, err := a.adapter.fileCommand(ctx, a, "usage", ".")
	if err != nil {
		return 0, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return 0, helperErrorText(stderr.String(), err)
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: invalid file-helper usage", runtime.ErrInvalidObservation)
	}
	return value, nil
}

func (a *fileAccess) Stat(ctx context.Context, relativePath string) (fs.FileInfo, error) {
	command, err := a.adapter.fileCommand(ctx, a, "stat", relativePath)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, helperErrorText(stderr.String(), err)
	}
	var entry runtime.FileEntry
	if err := json.Unmarshal(output, &entry); err != nil {
		return nil, fmt.Errorf("%w: invalid file-helper stat", runtime.ErrInvalidObservation)
	}
	return runtimeFileInfo{entry: entry}, nil
}

func (a *fileAccess) OpenRead(ctx context.Context, relativePath string) (io.ReadCloser, fs.FileInfo, error) {
	info, err := a.Stat(ctx, relativePath)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, runtime.ErrConflict
	}
	command, err := a.adapter.fileCommand(ctx, a, "download", relativePath)
	if err != nil {
		return nil, nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, nil, helperErrorText(stderr.String(), err)
	}
	return &processReader{reader: output, command: command, stderr: &stderr}, info, nil
}

func (a *fileAccess) OpenZip(ctx context.Context, relativePath string) (io.ReadCloser, error) {
	command, err := a.adapter.fileCommand(ctx, a, "zip", relativePath)
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, helperErrorText(stderr.String(), err)
	}
	return &processReader{reader: output, command: command, stderr: &stderr}, nil
}

func (a *fileAccess) CreateDirectory(ctx context.Context, relativePath, name string) error {
	return a.runMutation(ctx, "mkdir", relativePath, "--name", name)
}

func (a *fileAccess) Delete(ctx context.Context, relativePath string) error {
	return a.runMutation(ctx, "delete", relativePath)
}

func (a *fileAccess) Rename(ctx context.Context, relativePath, oldName, newName string) error {
	return a.runMutation(ctx, "rename", relativePath, "--old-name", oldName, "--new-name", newName)
}

func (a *fileAccess) Upload(ctx context.Context, relativePath, filename string, source io.Reader) error {
	command, err := a.adapter.fileCommand(ctx, a, "upload", relativePath, "--name", filename)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return helperErrorText(stderr.String(), err)
	}
	_, copyErr := io.Copy(input, source)
	closeErr := input.Close()
	waitErr := command.Wait()
	return firstFileError(copyErr, closeErr, helperErrorText(stderr.String(), waitErr))
}

func (a *fileAccess) runMutation(ctx context.Context, operation, relativePath string, values ...string) error {
	command, err := a.adapter.fileCommand(ctx, a, operation, relativePath, values...)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return helperErrorText(stderr.String(), err)
	}
	return nil
}

func (a *fileAccess) Close() error { return nil }

type processReader struct {
	reader  io.ReadCloser
	command *exec.Cmd
	stderr  *bytes.Buffer
	closed  bool
}

func (r *processReader) Read(value []byte) (int, error) { return r.reader.Read(value) }

func (r *processReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	closeErr := r.reader.Close()
	waitErr := r.command.Wait()
	return firstFileError(closeErr, helperErrorText(r.stderr.String(), waitErr))
}

type runtimeFileInfo struct{ entry runtime.FileEntry }

func (value runtimeFileInfo) Name() string { return value.entry.Name }
func (value runtimeFileInfo) Size() int64  { return value.entry.Size }
func (value runtimeFileInfo) Mode() fs.FileMode {
	if value.entry.IsDir {
		return fs.ModeDir | 0o700
	}
	return 0o600
}
func (value runtimeFileInfo) ModTime() time.Time { return value.entry.ModTime }
func (value runtimeFileInfo) IsDir() bool        { return value.entry.IsDir }
func (value runtimeFileInfo) Sys() any           { return nil }

func helperErrorText(stderr string, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(stderr, "file already exists") {
		return runtime.ErrConflict
	}
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "no such file") {
		return runtime.ErrNotFound
	}
	if strings.Contains(stderr, "read-only mount") {
		return runtime.ErrConflict
	}
	return fmt.Errorf("%w: file helper: %v", runtime.ErrUnavailable, err)
}

func firstFileError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func isCleanAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
