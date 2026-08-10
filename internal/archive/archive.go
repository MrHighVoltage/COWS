// Package archive contains the bounded, streamed ZIP writer shared by the
// file manager (in-process mounts) and the rootless file-helper subprocess.
// Both paths stream a directory tree into a ZIP archive while enforcing the
// same byte and entry limits and the same symlink rejection, so the policy
// cannot drift between the two implementations.
package archive

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
)

const (
	// MaxBytes is the upper bound on the uncompressed bytes the writer will
	// stream from source files before aborting with ErrTooLarge.
	MaxBytes int64 = 4 << 30
	// MaxEntries is the upper bound on archive entries (files plus
	// directories) before the writer aborts with ErrTooManyEntries.
	MaxEntries = 100000
)

var (
	ErrTooLarge           = errors.New("archive: too large")
	ErrTooManyEntries     = errors.New("archive: too many entries")
	ErrSymlinkUnsupported = errors.New("archive: symbolic links are not supported")
	ErrNotDirectory       = errors.New("archive: not a directory")
)

// State tracks the cumulative byte and entry counts for a single archive
// operation. It is owned by the caller so the low-level WriteZip form can be
// composed into a larger streaming pipeline.
type State struct {
	Bytes   int64
	Entries int64
}

// WriteZip walks root.FS() starting at relativePath and writes a streamed,
// bounded ZIP archive to w. The caller owns the *zip.Writer (including
// Close) and the State. Symlinks are rejected, the context bounds
// cancellation, and exceeding MaxBytes or MaxEntries aborts the walk.
//
// Path errors from the underlying filesystem are returned unwrapped so the
// caller can map them to its own error vocabulary; the archive-specific
// limits and symlink policy use the sentinel errors above.
func WriteZip(ctx context.Context, root *os.Root, relativePath string, w *zip.Writer, state *State) error {
	return fs.WalkDir(root.FS(), relativePath, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if currentPath == "." {
			return nil
		}
		if state.Entries >= MaxEntries {
			return ErrTooManyEntries
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrSymlinkUnsupported
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		archiveName := path.Clean(currentPath)
		if entry.IsDir() {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = archiveName + "/"
			header.Method = zip.Store
			if _, err := w.CreateHeader(header); err != nil {
				return err
			}
			state.Entries++
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
		output, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := root.Open(currentPath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, &boundedReader{ctx: ctx, source: file, state: state})
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		state.Entries++
		return nil
	})
}

// ZipDirectory writes a bounded ZIP archive of the directory at relativePath
// under root to output. It is the self-contained form: it validates that the
// path is a real directory, creates and closes the zip.Writer, and returns the
// first error encountered. Callers that need to drive the writer themselves
// (for example over an io.Pipe with synchronous pre-validation) should use
// WriteZip instead.
func ZipDirectory(ctx context.Context, root *os.Root, relativePath string, output io.Writer) error {
	info, err := root.Lstat(relativePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSymlinkUnsupported
	}
	if !info.IsDir() {
		return ErrNotDirectory
	}
	w := zip.NewWriter(output)
	state := State{}
	walkErr := WriteZip(ctx, root, relativePath, w, &state)
	closeErr := w.Close()
	if walkErr != nil {
		return walkErr
	}
	return closeErr
}

// boundedReader caps the cumulative bytes copied from source and aborts with
// ErrTooLarge once MaxBytes is reached, while honoring context cancellation.
type boundedReader struct {
	ctx    context.Context
	source io.Reader
	state  *State
}

func (r *boundedReader) Read(value []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
	}
	if r.state.Bytes >= MaxBytes {
		return 0, ErrTooLarge
	}
	remaining := MaxBytes - r.state.Bytes
	if int64(len(value)) > remaining {
		value = value[:int(remaining)]
	}
	count, err := r.source.Read(value)
	r.state.Bytes += int64(count)
	if r.state.Bytes >= MaxBytes && err == nil {
		return count, ErrTooLarge
	}
	return count, err
}
