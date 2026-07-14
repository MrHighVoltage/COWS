package files

import (
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
)

const MaxUploadBytes int64 = 128 << 20

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
