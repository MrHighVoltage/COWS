package files

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cows-project/cows/internal/archive"
	"github.com/cows-project/cows/internal/runtime"
	"github.com/cows-project/cows/internal/workspace"
)

var (
	ErrInvalidPath       = errors.New("files: invalid path")
	ErrNotFound          = errors.New("files: not found")
	ErrReadOnly          = errors.New("files: mount is read-only")
	ErrNotRegular        = errors.New("files: not a regular file")
	ErrUploadTooLarge    = errors.New("files: upload is too large")
	ErrNameConflict      = errors.New("files: name already exists")
	ErrFileManagerAccess = errors.New("files: file manager access is unavailable")
	ErrNotDirectory      = archive.ErrNotDirectory
	ErrArchiveTooLarge   = archive.ErrTooLarge
	ErrArchiveTooMany    = archive.ErrTooManyEntries
	ErrTooManyEntries    = errors.New("files: directory contains too many entries")
	ErrDownloadTooLarge  = errors.New("files: download is too large")
)

const (
	MaxUploadBytes      int64 = 128 << 20
	MaxDownloadBytes    int64 = 4 << 30
	MaxArchiveBytes           = archive.MaxBytes
	MaxDirectoryEntries       = 10000
	MaxArchiveEntries         = archive.MaxEntries
)

type MountResolver interface {
	ListFileMounts(ctx context.Context, actorID, workspaceID string) ([]workspace.FileMount, error)
}

type AccessCoordinator interface {
	BeginFileAccess(ctx context.Context, actorID, workspaceID string) (func(), error)
}

type AuditRecorder interface {
	RecordFileAudit(ctx context.Context, actorID, workspaceID, eventType string, metadata map[string]string)
}

type Service struct {
	workspaces  MountResolver
	runtime     runtime.FileAccessRuntime
	coordinator AccessCoordinator
	audit       AuditRecorder
}

type Entry struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

type Listing struct {
	Mount workspace.FileMount
	Path  string
	Items []Entry
}

func New(workspaces MountResolver, runtimes ...runtime.FileAccessRuntime) *Service {
	var runtimeAccess runtime.FileAccessRuntime
	if len(runtimes) > 0 {
		runtimeAccess = runtimes[0]
	}
	coordinator, _ := workspaces.(AccessCoordinator)
	audit, _ := workspaces.(AuditRecorder)
	return &Service{workspaces: workspaces, runtime: runtimeAccess, coordinator: coordinator, audit: audit}
}

func (s *Service) Mounts(ctx context.Context, actorID, workspaceID string) ([]workspace.FileMount, error) {
	if s.workspaces == nil {
		return nil, ErrFileManagerAccess
	}
	return s.workspaces.ListFileMounts(ctx, actorID, workspaceID)
}

func (s *Service) List(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (Listing, error) {
	mount, relativePath, err := s.resolveMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return Listing{}, err
	}
	release, err := s.beginAccess(ctx, actorID, workspaceID)
	if err != nil {
		return Listing{}, err
	}
	defer release()
	backend, err := s.openBackend(ctx, mount)
	if err != nil {
		return Listing{}, err
	}
	defer backend.Close()
	if backend.access != nil {
		access := backend.access
		items, err := access.List(ctx, relativePath)
		if err != nil {
			return Listing{}, mapRuntimeError(err)
		}
		if len(items) > MaxDirectoryEntries {
			return Listing{}, ErrTooManyEntries
		}
		listing := Listing{Mount: mount, Path: relativePath, Items: make([]Entry, 0, len(items))}
		for _, item := range items {
			itemPath := item.Name
			if relativePath != "." {
				itemPath = path.Join(relativePath, item.Name)
			}
			listing.Items = append(listing.Items, Entry{Name: item.Name, Path: itemPath, IsDir: item.IsDir, Size: item.Size, ModTime: item.ModTime})
		}
		return listing, nil
	}
	root := backend.root
	items, err := readDirectory(root, relativePath)
	if err != nil {
		return Listing{}, mapPathError(err)
	}
	listing := Listing{Mount: mount, Path: relativePath, Items: make([]Entry, 0, len(items))}
	for _, item := range items {
		if item.Type()&os.ModeSymlink != 0 {
			return Listing{}, ErrInvalidPath
		}
		info, err := item.Info()
		if err != nil {
			return Listing{}, mapPathError(err)
		}
		itemPath := item.Name()
		if relativePath != "." {
			itemPath = path.Join(relativePath, item.Name())
		}
		listing.Items = append(listing.Items, Entry{Name: item.Name(), Path: itemPath, IsDir: item.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return listing, nil
}

func (s *Service) OpenDownload(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (io.ReadCloser, fs.FileInfo, error) {
	mount, relativePath, err := s.resolveMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return nil, nil, err
	}
	release, err := s.beginAccess(ctx, actorID, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	backend, err := s.openBackend(ctx, mount)
	if err != nil {
		release()
		return nil, nil, err
	}
	if backend.access != nil {
		access := backend.access
		file, info, err := access.OpenRead(ctx, relativePath)
		if err != nil {
			access.Close()
			release()
			return nil, nil, mapRuntimeError(err)
		}
		if info.Size() > MaxDownloadBytes {
			file.Close()
			access.Close()
			release()
			return nil, nil, ErrDownloadTooLarge
		}
		s.recordAudit(ctx, actorID, workspaceID, "file.downloaded", mount, relativePath)
		return &accessFile{reader: &boundedReadCloser{ReadCloser: file, remaining: MaxDownloadBytes}, access: access, release: release}, info, nil
	}
	root := backend.root
	pathInfo, err := root.Lstat(relativePath)
	if err != nil {
		root.Close()
		release()
		return nil, nil, mapPathError(err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		root.Close()
		release()
		return nil, nil, ErrInvalidPath
	}
	file, err := root.Open(relativePath)
	if err != nil {
		root.Close()
		release()
		return nil, nil, mapPathError(err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		backend.Close()
		release()
		return nil, nil, mapPathError(err)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		backend.Close()
		release()
		return nil, nil, ErrNotRegular
	}
	if info.Size() > MaxDownloadBytes {
		file.Close()
		backend.Close()
		release()
		return nil, nil, ErrDownloadTooLarge
	}
	if err := backend.Close(); err != nil {
		file.Close()
		release()
		return nil, nil, err
	}
	s.recordAudit(ctx, actorID, workspaceID, "file.downloaded", mount, relativePath)
	return &releaseFile{ReadCloser: &boundedReadCloser{ReadCloser: file, remaining: MaxDownloadBytes}, release: release}, info, nil
}

// OpenZip starts a bounded, streamed ZIP response for an approved directory.
// The archive is generated in a pipe so COWS never buffers the directory in
// memory or creates a second temporary copy on disk.
func (s *Service) OpenZip(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (io.ReadCloser, string, error) {
	mount, relativePath, err := s.resolveMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return nil, "", err
	}
	release, err := s.beginAccess(ctx, actorID, workspaceID)
	if err != nil {
		return nil, "", err
	}
	name := mount.Name
	if relativePath != "." {
		name = path.Base(relativePath)
	}
	backend, err := s.openBackend(ctx, mount)
	if err != nil {
		release()
		return nil, "", err
	}
	if backend.access != nil {
		access := backend.access
		archive, err := access.OpenZip(ctx, relativePath)
		if err != nil {
			access.Close()
			release()
			return nil, "", mapRuntimeError(err)
		}
		s.recordAudit(ctx, actorID, workspaceID, "file.archive_downloaded", mount, relativePath)
		return &accessFile{reader: archive, access: access, release: release}, name + ".zip", nil
	}
	root := backend.root
	info, err := root.Lstat(relativePath)
	if err != nil {
		backend.Close()
		release()
		return nil, "", mapPathError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		backend.Close()
		release()
		return nil, "", ErrInvalidPath
	}
	if !info.IsDir() {
		root.Close()
		release()
		return nil, "", ErrNotDirectory
	}
	reader, writer := io.Pipe()
	go func() {
		defer release()
		defer backend.Close()
		archiveWriter := zip.NewWriter(writer)
		state := archive.State{}
		writeErr := archive.WriteZip(ctx, root, relativePath, archiveWriter, &state)
		if writeErr != nil {
			writeErr = mapPathError(writeErr)
		}
		closeErr := archiveWriter.Close()
		if writeErr != nil {
			_ = writer.CloseWithError(writeErr)
			return
		}
		if closeErr != nil {
			_ = writer.CloseWithError(closeErr)
			return
		}
		_ = writer.Close()
	}()
	s.recordAudit(ctx, actorID, workspaceID, "file.archive_downloaded", mount, relativePath)
	return reader, name + ".zip", nil
}

// OpenRuntimeZip is used only by the administrator retained-volume recovery
// path. The caller has already authenticated and selected a database-backed
// tombstone; this method accepts no browser path and still uses the runtime
// file-access boundary.
func (s *Service) OpenRuntimeZip(ctx context.Context, spec runtime.FileAccessSpec, name string) (io.ReadCloser, error) {
	if s.runtime == nil || spec.MountType != "volume" || spec.Source == "" || name == "" || strings.ContainsAny(name, "/\\\x00\r\n") {
		return nil, ErrFileManagerAccess
	}
	access, err := s.runtime.OpenFileAccess(ctx, spec)
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	archive, err := access.OpenZip(ctx, ".")
	if err != nil {
		_ = access.Close()
		return nil, mapRuntimeError(err)
	}
	return &accessFile{reader: archive, access: access}, nil
}

func (s *Service) CreateDirectory(ctx context.Context, actorID, workspaceID, mountName, relativePath, name string) error {
	mount, relativePath, err := s.resolveMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	release, err := s.beginAccess(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	defer release()
	if mount.ReadOnly {
		return ErrReadOnly
	}
	name, err = cleanName(name)
	if err != nil {
		return err
	}
	backend, err := s.openBackend(ctx, mount)
	if err != nil {
		return err
	}
	if backend.access != nil {
		access := backend.access
		defer backend.Close()
		if err := ensureDirectoryCapacity(ctx, access, nil, relativePath); err != nil {
			return mapRuntimeError(err)
		}
		if err := access.CreateDirectory(ctx, relativePath, name); err != nil {
			return mapRuntimeError(err)
		}
		s.recordAudit(ctx, actorID, workspaceID, "file.directory_created", mount, path.Join(relativePath, name))
		return nil
	}
	root := backend.root
	defer backend.Close()
	if err := ensureDirectoryCapacity(ctx, nil, root, relativePath); err != nil {
		return err
	}
	target := path.Join(relativePath, name)
	if _, err := root.Lstat(target); err == nil {
		return ErrNameConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return mapPathError(err)
	}
	if err := root.Mkdir(target, 0o700); err != nil {
		return mapPathError(err)
	}
	s.recordAudit(ctx, actorID, workspaceID, "file.directory_created", mount, target)
	return nil
}

func (s *Service) Delete(ctx context.Context, actorID, workspaceID, mountName, relativePath string) error {
	mount, relativePath, err := s.resolveMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	release, err := s.beginAccess(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	defer release()
	if mount.ReadOnly || relativePath == "." {
		if relativePath == "." {
			return ErrInvalidPath
		}
		return ErrReadOnly
	}
	backend, err := s.openBackend(ctx, mount)
	if err != nil {
		return err
	}
	if backend.access != nil {
		access := backend.access
		defer backend.Close()
		if err := access.Delete(ctx, relativePath); err != nil {
			return mapRuntimeError(err)
		}
		s.recordAudit(ctx, actorID, workspaceID, "file.deleted", mount, relativePath)
		return nil
	}
	root := backend.root
	defer backend.Close()
	if _, err := root.Lstat(relativePath); err != nil {
		return mapPathError(err)
	}
	if err := root.RemoveAll(relativePath); err != nil {
		return mapPathError(err)
	}
	s.recordAudit(ctx, actorID, workspaceID, "file.deleted", mount, relativePath)
	return nil
}

func (s *Service) Rename(ctx context.Context, actorID, workspaceID, mountName, relativePath, oldName, newName string) error {
	mount, relativePath, err := s.resolveMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	release, err := s.beginAccess(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	defer release()
	if mount.ReadOnly {
		return ErrReadOnly
	}
	oldName, err = cleanName(oldName)
	if err != nil {
		return err
	}
	newName, err = cleanName(newName)
	if err != nil {
		return err
	}
	backend, err := s.openBackend(ctx, mount)
	if err != nil {
		return err
	}
	if backend.access != nil {
		access := backend.access
		defer backend.Close()
		if err := access.Rename(ctx, relativePath, oldName, newName); err != nil {
			return mapRuntimeError(err)
		}
		s.recordAudit(ctx, actorID, workspaceID, "file.renamed", mount, path.Join(relativePath, oldName)+" -> "+path.Join(relativePath, newName))
		return nil
	}
	root := backend.root
	defer backend.Close()
	oldPath := path.Join(relativePath, oldName)
	newPath := path.Join(relativePath, newName)
	oldInfo, err := root.Lstat(oldPath)
	if err != nil {
		return mapPathError(err)
	}
	if oldInfo.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidPath
	}
	if _, err := root.Lstat(newPath); err == nil {
		return ErrNameConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return mapPathError(err)
	}
	if err := root.Rename(oldPath, newPath); err != nil {
		return mapPathError(err)
	}
	s.recordAudit(ctx, actorID, workspaceID, "file.renamed", mount, oldPath+" -> "+newPath)
	return nil
}

func (s *Service) Upload(ctx context.Context, actorID, workspaceID, mountName, relativePath, filename string, source io.Reader) error {
	mount, relativePath, err := s.resolveMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	release, err := s.beginAccess(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	defer release()
	if mount.ReadOnly {
		return ErrReadOnly
	}
	filename, err = cleanName(filename)
	if err != nil {
		return err
	}
	backend, err := s.openBackend(ctx, mount)
	if err != nil {
		return err
	}
	if backend.access != nil {
		access := backend.access
		defer backend.Close()
		if err := ensureDirectoryCapacity(ctx, access, nil, relativePath); err != nil {
			return mapRuntimeError(err)
		}
		if err := access.Upload(ctx, relativePath, filename, source); err != nil {
			return mapRuntimeError(err)
		}
		s.recordAudit(ctx, actorID, workspaceID, "file.uploaded", mount, path.Join(relativePath, filename))
		return nil
	}
	root := backend.root
	defer backend.Close()
	if err := ensureDirectoryCapacity(ctx, nil, root, relativePath); err != nil {
		return err
	}
	target := path.Join(relativePath, filename)
	if existing, statErr := root.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidPath
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return mapPathError(statErr)
	}
	temporary, err := randomTemporaryName()
	if err != nil {
		return err
	}
	temporaryPath := path.Join(relativePath, temporary)
	file, err := root.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return mapPathError(err)
	}
	count, copyErr := io.Copy(file, io.LimitReader(source, MaxUploadBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || count > MaxUploadBytes {
		_ = root.Remove(temporaryPath)
		if count > MaxUploadBytes {
			return ErrUploadTooLarge
		}
		return mapPathError(firstError(copyErr, closeErr))
	}
	if err := root.Rename(temporaryPath, target); err != nil {
		_ = root.Remove(temporaryPath)
		return mapPathError(err)
	}
	s.recordAudit(ctx, actorID, workspaceID, "file.uploaded", mount, target)
	return nil
}

func (s *Service) resolveMount(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (workspace.FileMount, string, error) {
	if s.workspaces == nil {
		return workspace.FileMount{}, "", ErrFileManagerAccess
	}
	mounts, err := s.Mounts(ctx, actorID, workspaceID)
	if err != nil {
		return workspace.FileMount{}, "", err
	}
	var mount workspace.FileMount
	for _, candidate := range mounts {
		if candidate.Name == mountName {
			mount = candidate
			break
		}
	}
	if mount.Name == "" && len(mounts) > 0 && mountName == "" {
		mount = mounts[0]
	}
	if mount.Name == "" {
		return workspace.FileMount{}, "", ErrFileManagerAccess
	}
	cleaned, err := cleanRelativePath(relativePath)
	if err != nil {
		return workspace.FileMount{}, "", err
	}
	return mount, cleaned, nil
}

func (s *Service) openLocalMount(mount workspace.FileMount) (*os.Root, error) {
	if mount.Root == "" {
		return nil, ErrFileManagerAccess
	}
	root, err := os.OpenRoot(mount.Root)
	if err != nil {
		return nil, ErrFileManagerAccess
	}
	return root, nil
}

func readDirectory(root *os.Root, relativePath string) ([]fs.DirEntry, error) {
	directory, err := root.Open(relativePath)
	if err != nil {
		return nil, mapPathError(err)
	}
	defer directory.Close()
	items, err := directory.ReadDir(MaxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, mapPathError(err)
	}
	if len(items) > MaxDirectoryEntries {
		return nil, ErrTooManyEntries
	}
	return items, nil
}

func ensureDirectoryCapacity(ctx context.Context, access runtime.FileAccess, root *os.Root, relativePath string) error {
	if access != nil {
		items, err := access.List(ctx, relativePath)
		if err != nil {
			return err
		}
		if len(items) >= MaxDirectoryEntries {
			return ErrTooManyEntries
		}
		return nil
	}
	items, err := readDirectory(root, relativePath)
	if err != nil {
		return err
	}
	if len(items) >= MaxDirectoryEntries {
		return ErrTooManyEntries
	}
	return nil
}

func (s *Service) openRuntimeAccess(ctx context.Context, mount workspace.FileMount) (runtime.FileAccess, error) {
	if s.runtime == nil || mount.RuntimeID == "" || mount.Source == "" {
		return nil, ErrFileManagerAccess
	}
	access, err := s.runtime.OpenFileAccess(ctx, runtime.FileAccessSpec{
		RuntimeID:     mount.RuntimeID,
		MountType:     mount.MountType,
		Source:        mount.Source,
		ContainerPath: mount.ContainerPath,
		ContainerUID:  mount.ContainerUID,
		ContainerGID:  mount.ContainerGID,
		ReadOnly:      mount.ReadOnly,
	})
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	return access, nil
}

type fileBackend struct {
	access runtime.FileAccess
	root   *os.Root
}

func (b *fileBackend) Close() error {
	if b.access != nil {
		return b.access.Close()
	}
	return b.root.Close()
}

func (s *Service) openBackend(ctx context.Context, mount workspace.FileMount) (*fileBackend, error) {
	if s.runtime != nil {
		access, err := s.openRuntimeAccess(ctx, mount)
		if err == nil {
			return &fileBackend{access: access}, nil
		}
		// Directory mounts use the server-owned
		// rooted path. Named volumes have no safe host fallback.
		if mount.Root == "" || !errors.Is(err, ErrFileManagerAccess) {
			return nil, err
		}
	}
	root, err := s.openLocalMount(mount)
	if err != nil {
		return nil, err
	}
	return &fileBackend{root: root}, nil
}

func (s *Service) beginAccess(ctx context.Context, actorID, workspaceID string) (func(), error) {
	if s.coordinator == nil {
		return func() {}, nil
	}
	return s.coordinator.BeginFileAccess(ctx, actorID, workspaceID)
}

func (s *Service) recordAudit(ctx context.Context, actorID, workspaceID, eventType string, mount workspace.FileMount, value string) {
	if s.audit == nil {
		return
	}
	s.audit.RecordFileAudit(ctx, actorID, workspaceID, eventType, map[string]string{"mount": mount.Name, "path": value})
}

type accessFile struct {
	reader  io.ReadCloser
	access  runtime.FileAccess
	release func()
	once    sync.Once
}

func (f *accessFile) Read(value []byte) (int, error) { return f.reader.Read(value) }

func (f *accessFile) Close() error {
	err := firstError(f.reader.Close(), f.access.Close())
	if f.release != nil {
		f.once.Do(f.release)
	}
	return err
}

type releaseFile struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

type boundedReadCloser struct {
	io.ReadCloser
	remaining int64
}

func (r *boundedReadCloser) Read(value []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		count, err := r.ReadCloser.Read(extra[:])
		if count > 0 {
			return 0, ErrDownloadTooLarge
		}
		return 0, err
	}
	if int64(len(value)) > r.remaining {
		value = value[:r.remaining]
	}
	count, err := r.ReadCloser.Read(value)
	r.remaining -= int64(count)
	return count, err
}

func (f *releaseFile) Close() error {
	err := f.ReadCloser.Close()
	if f.release != nil {
		f.once.Do(f.release)
	}
	return err
}

func cleanRelativePath(value string) (string, error) {
	if value == "" {
		return ".", nil
	}
	if strings.ContainsAny(value, "\x00\r\n") || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", ErrInvalidPath
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidPath
	}
	return cleaned, nil
}

func cleanName(value string) (string, error) {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00\r\n") || !utf8.ValidString(value) || len(value) > 255 {
		return "", ErrInvalidPath
	}
	return value, nil
}

func randomTemporaryName() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return ".cows-upload-" + hex.EncodeToString(bytes), nil
}

func firstError(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

func mapPathError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

func mapRuntimeError(err error) error {
	switch {
	case errors.Is(err, runtime.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, runtime.ErrTooManyEntries):
		return ErrTooManyEntries
	case errors.Is(err, runtime.ErrConflict):
		return ErrNameConflict
	case errors.Is(err, runtime.ErrNotSupported), errors.Is(err, runtime.ErrUnavailable):
		return ErrFileManagerAccess
	default:
		return err
	}
}
