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
	"time"
	"unicode/utf8"

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
	ErrNotDirectory      = errors.New("files: not a directory")
	ErrArchiveTooLarge   = errors.New("files: archive is too large")
	ErrArchiveTooMany    = errors.New("files: archive contains too many entries")
)

const (
	MaxUploadBytes    int64 = 128 << 20
	MaxArchiveBytes   int64 = 4 << 30
	MaxArchiveEntries       = 100000
)

type MountResolver interface {
	ListFileMounts(ctx context.Context, actorID, workspaceID string) ([]workspace.FileMount, error)
}

type Service struct {
	workspaces MountResolver
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

func New(workspaces MountResolver) *Service {
	return &Service{workspaces: workspaces}
}

func (s *Service) Mounts(ctx context.Context, actorID, workspaceID string) ([]workspace.FileMount, error) {
	if s.workspaces == nil {
		return nil, ErrFileManagerAccess
	}
	return s.workspaces.ListFileMounts(ctx, actorID, workspaceID)
}

func (s *Service) List(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (Listing, error) {
	mount, root, relativePath, err := s.openMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return Listing{}, err
	}
	defer root.Close()
	items, err := fs.ReadDir(root.FS(), relativePath)
	if err != nil {
		return Listing{}, mapPathError(err)
	}
	listing := Listing{Mount: mount, Path: relativePath, Items: make([]Entry, 0, len(items))}
	for _, item := range items {
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

func (s *Service) OpenDownload(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (*os.File, fs.FileInfo, error) {
	_, root, relativePath, err := s.openMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return nil, nil, err
	}
	file, err := root.Open(relativePath)
	if err != nil {
		root.Close()
		return nil, nil, mapPathError(err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		root.Close()
		return nil, nil, mapPathError(err)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		root.Close()
		return nil, nil, ErrNotRegular
	}
	if err := root.Close(); err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

// OpenZip starts a bounded, streamed ZIP response for an approved directory.
// The archive is generated in a pipe so COWS never buffers the directory in
// memory or creates a second temporary copy on disk.
func (s *Service) OpenZip(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (io.ReadCloser, string, error) {
	mount, root, relativePath, err := s.openMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return nil, "", err
	}
	info, err := root.Lstat(relativePath)
	if err != nil {
		root.Close()
		return nil, "", mapPathError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		root.Close()
		return nil, "", ErrInvalidPath
	}
	if !info.IsDir() {
		root.Close()
		return nil, "", ErrNotDirectory
	}
	name := mount.Name
	if relativePath != "." {
		name = path.Base(relativePath)
	}
	reader, writer := io.Pipe()
	go func() {
		defer root.Close()
		archive := zip.NewWriter(writer)
		state := archiveState{}
		writeErr := writeZip(ctx, root, relativePath, archive, &state)
		closeErr := archive.Close()
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
	return reader, name + ".zip", nil
}

type archiveState struct {
	bytes   int64
	entries int
}

func writeZip(ctx context.Context, root *os.Root, relativePath string, archive *zip.Writer, state *archiveState) error {
	return fs.WalkDir(root.FS(), relativePath, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return mapPathError(walkErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if currentPath == "." {
			return nil
		}
		if state.entries >= MaxArchiveEntries {
			return ErrArchiveTooMany
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrInvalidPath
		}
		info, err := entry.Info()
		if err != nil {
			return mapPathError(err)
		}
		archiveName := path.Clean(currentPath)
		if entry.IsDir() {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = archiveName + "/"
			header.Method = zip.Store
			if _, err := archive.CreateHeader(header); err != nil {
				return err
			}
			state.entries++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = archiveName
		header.Method = zip.Deflate
		output, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := root.Open(currentPath)
		if err != nil {
			return mapPathError(err)
		}
		_, copyErr := io.Copy(output, &archiveReader{ctx: ctx, source: file, state: state})
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		state.entries++
		return nil
	})
}

type archiveReader struct {
	ctx    context.Context
	source io.Reader
	state  *archiveState
}

func (r *archiveReader) Read(value []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
	}
	if r.state.bytes >= MaxArchiveBytes {
		return 0, ErrArchiveTooLarge
	}
	remaining := MaxArchiveBytes - r.state.bytes
	if int64(len(value)) > remaining {
		value = value[:int(remaining)]
	}
	count, err := r.source.Read(value)
	r.state.bytes += int64(count)
	if r.state.bytes >= MaxArchiveBytes && err == nil {
		return count, ErrArchiveTooLarge
	}
	return count, err
}

func (s *Service) CreateDirectory(ctx context.Context, actorID, workspaceID, mountName, relativePath, name string) error {
	mount, root, relativePath, err := s.openMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if mount.ReadOnly {
		return ErrReadOnly
	}
	name, err = cleanName(name)
	if err != nil {
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
	return nil
}

func (s *Service) Delete(ctx context.Context, actorID, workspaceID, mountName, relativePath string) error {
	mount, root, relativePath, err := s.openMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if mount.ReadOnly || relativePath == "." {
		if relativePath == "." {
			return ErrInvalidPath
		}
		return ErrReadOnly
	}
	if _, err := root.Lstat(relativePath); err != nil {
		return mapPathError(err)
	}
	if err := root.RemoveAll(relativePath); err != nil {
		return mapPathError(err)
	}
	return nil
}

func (s *Service) Rename(ctx context.Context, actorID, workspaceID, mountName, relativePath, oldName, newName string) error {
	mount, root, relativePath, err := s.openMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	defer root.Close()
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
	oldPath := path.Join(relativePath, oldName)
	newPath := path.Join(relativePath, newName)
	if _, err := root.Lstat(oldPath); err != nil {
		return mapPathError(err)
	}
	if _, err := root.Lstat(newPath); err == nil {
		return ErrNameConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return mapPathError(err)
	}
	if err := root.Rename(oldPath, newPath); err != nil {
		return mapPathError(err)
	}
	return nil
}

func (s *Service) Upload(ctx context.Context, actorID, workspaceID, mountName, relativePath, filename string, source io.Reader) error {
	mount, root, relativePath, err := s.openMount(ctx, actorID, workspaceID, mountName, relativePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if mount.ReadOnly {
		return ErrReadOnly
	}
	filename, err = cleanName(filename)
	if err != nil {
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
	return nil
}

func (s *Service) openMount(ctx context.Context, actorID, workspaceID, mountName, relativePath string) (workspace.FileMount, *os.Root, string, error) {
	if s.workspaces == nil {
		return workspace.FileMount{}, nil, "", ErrFileManagerAccess
	}
	mounts, err := s.Mounts(ctx, actorID, workspaceID)
	if err != nil {
		return workspace.FileMount{}, nil, "", err
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
		return workspace.FileMount{}, nil, "", ErrFileManagerAccess
	}
	cleaned, err := cleanRelativePath(relativePath)
	if err != nil {
		return workspace.FileMount{}, nil, "", err
	}
	root, err := os.OpenRoot(mount.Root)
	if err != nil {
		return workspace.FileMount{}, nil, "", ErrFileManagerAccess
	}
	return mount, root, cleaned, nil
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
