package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"testing"
	"time"
)

func TestWalkDirBoundsDirectoryReadsAndStopsAtBudget(t *testing.T) {
	for _, budget := range []int{4, 300} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			directory := &countedDirectory{left: 10000000}
			remaining, visited := budget, 0
			err := walkDir(context.Background(), directory, ".", fs.FileInfoToDirEntry(walkInfo{mode: fs.ModeDir}), &remaining, func(string, fs.DirEntry) error {
				visited++
				return nil
			})
			if !errors.Is(err, ErrTooManyEntries) || visited != budget {
				t.Fatalf("walk = %v, visited %d; want entry limit after %d visits", err, visited, budget)
			}
			if directory.read != budget { // root consumes one; the extra item detects overflow.
				t.Fatalf("read %d child entries, want %d", directory.read, budget)
			}
			if !directory.closed {
				t.Fatal("directory was not closed after exhausting the budget")
			}
		})
	}
}

func TestWalkDirAcceptsExactlyFullTree(t *testing.T) {
	directory := &countedDirectory{left: 3}
	remaining := 4
	if err := walkDir(context.Background(), directory, ".", fs.FileInfoToDirEntry(walkInfo{mode: fs.ModeDir}), &remaining, func(string, fs.DirEntry) error { return nil }); err != nil {
		t.Fatalf("walk exact budget: %v", err)
	}
	if remaining != 0 || directory.read != 3 || !directory.closed {
		t.Fatalf("remaining=%d read=%d closed=%v", remaining, directory.read, directory.closed)
	}
}

func TestWalkDirCancellationStopsBeforeReadingDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	directory := &countedDirectory{left: 10000000}
	remaining := 1000
	err := walkDir(ctx, directory, ".", fs.FileInfoToDirEntry(walkInfo{mode: fs.ModeDir}), &remaining, func(string, fs.DirEntry) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || directory.read != 0 || !directory.closed {
		t.Fatalf("error=%v read=%d closed=%v", err, directory.read, directory.closed)
	}
}

func TestWalkDirCancellationStopsWithinBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	directory := &countedDirectory{left: 10000000}
	remaining, visited := 1000, 0
	err := walkDir(ctx, directory, ".", fs.FileInfoToDirEntry(walkInfo{mode: fs.ModeDir}), &remaining, func(string, fs.DirEntry) error {
		visited++
		if visited == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || visited != 2 || directory.read != directoryBatchSize || !directory.closed {
		t.Fatalf("error=%v visited=%d read=%d closed=%v", err, visited, directory.read, directory.closed)
	}
}

// countedDirectory models an arbitrarily large directory without allocating it.
// Any unbounded ReadDir call fails the test rather than allocating the listing.
type countedDirectory struct {
	left   int
	read   int
	closed bool
}

func (d *countedDirectory) Open(name string) (fs.File, error) {
	if name != "." {
		return nil, fs.ErrNotExist
	}
	return d, nil
}
func (d *countedDirectory) Stat() (fs.FileInfo, error) { return walkInfo{mode: fs.ModeDir}, nil }
func (d *countedDirectory) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }
func (d *countedDirectory) Close() error               { d.closed = true; return nil }
func (d *countedDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 || n > directoryBatchSize {
		return nil, fmt.Errorf("unbounded directory read: %d", n)
	}
	var entries []fs.DirEntry
	for range min(n, d.left) {
		entries = append(entries, fs.FileInfoToDirEntry(walkInfo{name: fmt.Sprint(d.read)}))
		d.read++
		d.left--
	}
	if d.left == 0 {
		return entries, io.EOF
	}
	return entries, nil
}

type walkInfo struct {
	name string
	mode fs.FileMode
}

func (i walkInfo) Name() string       { return i.name }
func (i walkInfo) Size() int64        { return 0 }
func (i walkInfo) Mode() fs.FileMode  { return i.mode }
func (i walkInfo) ModTime() time.Time { return time.Time{} }
func (i walkInfo) IsDir() bool        { return i.mode.IsDir() }
func (i walkInfo) Sys() any           { return nil }
