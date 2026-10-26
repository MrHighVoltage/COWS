package archive

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
)

const directoryBatchSize = 128

// WalkDir visits at most maxEntries filesystem entries, including the starting
// path. Unlike fs.WalkDir, it never reads or sorts an entire directory at once.
// The traversal order is unspecified. Symlinks are rejected before visiting.
func WalkDir(ctx context.Context, root *os.Root, relativePath string, maxEntries int, visit func(string, fs.DirEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := root.Lstat(relativePath)
	if err != nil {
		return err
	}
	return walkDir(ctx, root.FS(), relativePath, fs.FileInfoToDirEntry(info), &maxEntries, visit)
}

func walkDir(ctx context.Context, root fs.FS, currentPath string, entry fs.DirEntry, remaining *int, visit func(string, fs.DirEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if *remaining <= 0 {
		return ErrTooManyEntries
	}
	*remaining -= 1
	if entry.Type()&os.ModeSymlink != 0 {
		return ErrSymlinkUnsupported
	}
	if err := visit(currentPath, entry); err != nil {
		return err
	}
	if !entry.IsDir() {
		return nil
	}
	file, err := root.Open(currentPath)
	if err != nil {
		return err
	}
	walkErr := walkDirectory(ctx, root, currentPath, file.(fs.ReadDirFile), remaining, visit)
	return errors.Join(walkErr, file.Close())
}

func walkDirectory(ctx context.Context, root fs.FS, currentPath string, directory fs.ReadDirFile, remaining *int, visit func(string, fs.DirEntry) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Read one extra entry only to distinguish an exhausted budget from an
		// exactly-full tree. No allocation depends on the directory's size.
		entries, readErr := directory.ReadDir(min(directoryBatchSize, *remaining+1))
		for _, entry := range entries {
			if err := walkDir(ctx, root, path.Join(currentPath, entry.Name()), entry, remaining, visit); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
