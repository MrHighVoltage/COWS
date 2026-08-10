package fileagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cows-project/cows/internal/archive"
)

const (
	MaxUploadBytes   int64 = 128 << 20
	MaxDownloadBytes int64 = 4 << 30
	MaxListEntries         = 10000
	MaxUsageEntries        = 1000000
)

type options struct {
	UID       int64
	GID       int64
	Root      string
	Operation string
	Path      string
	Name      string
	OldName   string
	NewName   string
	ReadOnly  bool
}

type entry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Run executes the private file-helper subcommand. It is only intended to be
// launched by the runtime adapter inside a rootless user namespace.
func Run(args []string, input io.Reader, output, errorOutput io.Writer) error {
	parsed, err := parseOptions(args, errorOutput)
	if err != nil {
		return err
	}
	if err := validateOptions(parsed); err != nil {
		return err
	}
	if err := os.Chdir(parsed.Root); err != nil {
		return fmt.Errorf("enter file root: %w", err)
	}
	if err := dropToIdentity(parsed.UID, parsed.GID); err != nil {
		return fmt.Errorf("set mapped file identity: %w", err)
	}
	root, err := os.OpenRoot(".")
	if err != nil {
		return fmt.Errorf("open rooted file access: %w", err)
	}
	defer root.Close()

	relativePath, err := cleanRelativePath(parsed.Path)
	if err != nil {
		return err
	}
	switch parsed.Operation {
	case "list":
		return list(root, relativePath, output)
	case "usage":
		return usage(root, output)
	case "stat":
		return stat(root, relativePath, output)
	case "download":
		return download(root, relativePath, output)
	case "zip":
		return archive.ZipDirectory(context.Background(), root, relativePath, output)
	case "mkdir":
		if parsed.ReadOnly {
			return errors.New("read-only mount")
		}
		return mkdir(root, relativePath, parsed.Name)
	case "delete":
		if parsed.ReadOnly {
			return errors.New("read-only mount")
		}
		return deletePath(root, relativePath)
	case "rename":
		if parsed.ReadOnly {
			return errors.New("read-only mount")
		}
		return rename(root, relativePath, parsed.OldName, parsed.NewName)
	case "upload":
		if parsed.ReadOnly {
			return errors.New("read-only mount")
		}
		return upload(root, relativePath, parsed.Name, input)
	default:
		return fmt.Errorf("unsupported file operation %q", parsed.Operation)
	}
}

func parseOptions(args []string, errorOutput io.Writer) (options, error) {
	flags := flag.NewFlagSet("file-helper", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	var value options
	flags.Int64Var(&value.UID, "uid", -1, "mapped UID")
	flags.Int64Var(&value.GID, "gid", -1, "mapped GID")
	flags.StringVar(&value.Root, "root", "", "approved absolute source directory")
	flags.StringVar(&value.Operation, "op", "", "file operation")
	flags.StringVar(&value.Path, "path", ".", "approved relative path")
	flags.StringVar(&value.Name, "name", "", "file or directory name")
	flags.StringVar(&value.OldName, "old-name", "", "old entry name")
	flags.StringVar(&value.NewName, "new-name", "", "new entry name")
	flags.BoolVar(&value.ReadOnly, "read-only", false, "read-only mount")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("unexpected file-helper arguments")
	}
	return value, nil
}

func validateOptions(value options) error {
	if value.UID < 0 || value.UID > 2147483647 || value.GID < 0 || value.GID > 2147483647 {
		return errors.New("invalid mapped file identity")
	}
	if value.Root == "" || !isAbsoluteCleanPath(value.Root) {
		return errors.New("file root must be a clean absolute path")
	}
	switch value.Operation {
	case "list", "usage", "stat", "download", "zip", "mkdir", "delete", "rename", "upload":
		return nil
	default:
		return fmt.Errorf("unsupported file operation %q", value.Operation)
	}
}

func isAbsoluteCleanPath(value string) bool {
	return value != "" && strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.Contains(value, "\x00")
}

func cleanRelativePath(value string) (string, error) {
	if value == "" {
		return ".", nil
	}
	if strings.ContainsAny(value, "\x00\r\n") || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", errors.New("invalid relative path")
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("invalid relative path")
	}
	return cleaned, nil
}

func cleanName(value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00\r\n") || !utf8.ValidString(value) || len(value) > 255 {
		return errors.New("invalid file name")
	}
	return nil
}

func list(root *os.Root, relativePath string, output io.Writer) error {
	directory, err := root.Open(relativePath)
	if err != nil {
		return err
	}
	defer directory.Close()
	items, err := directory.ReadDir(MaxListEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(items) > MaxListEntries {
		return errors.New("directory contains too many entries")
	}
	entries := make([]entry, 0, len(items))
	for _, item := range items {
		if item.Type()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not supported")
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		entries = append(entries, entry{Name: item.Name(), IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return json.NewEncoder(output).Encode(entries)
}

func usage(root *os.Root, output io.Writer) error {
	var total int64
	entries := 0
	err := fs.WalkDir(root.FS(), ".", func(currentPath string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > MaxUsageEntries {
			return errors.New("usage contains too many entries")
		}
		if item.Type()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not supported")
		}
		if !item.Type().IsRegular() {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Size() < 0 || total > (1<<62)-info.Size() {
			return errors.New("usage is too large")
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, total)
	return err
}

func stat(root *os.Root, relativePath string, output io.Writer) error {
	info, err := root.Lstat(relativePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symbolic links are not supported")
	}
	return json.NewEncoder(output).Encode(entry{Name: info.Name(), IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
}

func download(root *os.Root, relativePath string, output io.Writer) error {
	info, err := root.Lstat(relativePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if info.Size() > MaxDownloadBytes {
		return errors.New("download is too large")
	}
	file, err := root.Open(relativePath)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(output, file)
	return err
}

func mkdir(root *os.Root, relativePath, name string) error {
	if err := cleanName(name); err != nil {
		return err
	}
	target := path.Join(relativePath, name)
	if _, err := root.Lstat(target); err == nil {
		return errors.New("file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.Mkdir(target, 0o700)
}

func deletePath(root *os.Root, relativePath string) error {
	if relativePath == "." {
		return errors.New("cannot delete file root")
	}
	if _, err := root.Lstat(relativePath); err != nil {
		return err
	}
	return root.RemoveAll(relativePath)
}

func rename(root *os.Root, relativePath, oldName, newName string) error {
	if err := cleanName(oldName); err != nil {
		return err
	}
	if err := cleanName(newName); err != nil {
		return err
	}
	oldPath := path.Join(relativePath, oldName)
	newPath := path.Join(relativePath, newName)
	oldInfo, err := root.Lstat(oldPath)
	if err != nil {
		return err
	}
	if oldInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("symbolic links are not supported")
	}
	if _, err := root.Lstat(newPath); err == nil {
		return errors.New("file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.Rename(oldPath, newPath)
}

func upload(root *os.Root, relativePath, name string, input io.Reader) error {
	if err := cleanName(name); err != nil {
		return err
	}
	target := path.Join(relativePath, name)
	if existing, err := root.Lstat(target); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not supported")
		}
		return errors.New("file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := ".cows-upload-" + randomName()
	file, err := root.OpenFile(path.Join(relativePath, temporary), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	count, copyErr := io.Copy(file, io.LimitReader(input, MaxUploadBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || count > MaxUploadBytes {
		_ = root.Remove(path.Join(relativePath, temporary))
		if count > MaxUploadBytes {
			return errors.New("upload is too large")
		}
		return firstError(copyErr, closeErr)
	}
	if err := root.Rename(path.Join(relativePath, temporary), target); err != nil {
		_ = root.Remove(path.Join(relativePath, temporary))
		return err
	}
	return nil
}

func randomName() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "temporary"
	}
	return hex.EncodeToString(value)
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
