package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// smallRanges makes the pool split files of a few KiB into ranges of 1 KiB read through a 300-byte buffer.
func smallRanges(t *testing.T) {
	t.Helper()
	r, b := copyRange, copyBuffer
	copyRange, copyBuffer = 1<<10, 300
	t.Cleanup(func() { copyRange, copyBuffer = r, b })
}

func checkoutOf(t *testing.T, v fs.FS, kind string) (string, *counter, *atomic.Int64, error) {
	t.Helper()
	dest := t.TempDir()
	p, err := planCheckout(v, kind, limits{bytes: 1 << 40, nodes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var written counter
	var n atomic.Int64
	got, err := testWriter(t, dest).checkout(context.Background(), v, p, &written, &n)
	return got, &written, &n, err
}

// Files of every size around the range, and a folder of many, are written whole and in the right place, and the
// counters add up.
func TestParallelCheckoutWritesEveryByte(t *testing.T) {
	smallRanges(t)
	m := fstest.MapFS{"top": &fstest.MapFile{Mode: fs.ModeDir | 0o755}}
	want := map[string][]byte{}
	for _, size := range []int{0, 1, 299, 300, 301, 1023, 1024, 1025, 2047, 2048, 2049, 5000, 9999} {
		name := fmt.Sprintf("top/f%05d", size)
		want[name] = randomBytes(t, size)
		m[name] = &fstest.MapFile{Data: want[name]}
	}
	for i := range 40 {
		name := fmt.Sprintf("top/many%02d", i)
		want[name] = bytes.Repeat([]byte{byte(i)}, 100+i)
		m[name] = &fstest.MapFile{Data: want[name]}
	}
	got, written, files, err := checkoutOf(t, m, kindFolder)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for name, data := range want {
		b, err := os.ReadFile(filepath.Join(got, filepath.Base(name)))
		if err != nil || !bytes.Equal(b, data) {
			t.Fatalf("%s: %d bytes (%v), want %d", name, len(b), err, len(data))
		}
		total += int64(len(data))
	}
	if written.n.Load() != total || files.Load() != int64(len(want)) {
		t.Fatalf("counted %d bytes and %d files, want %d and %d", written.n.Load(), files.Load(), total, len(want))
	}
}

// A file of 100 GB is split into ranges that cover it once, with offsets above 2^32, and a range that starts above
// 2^32 is written at its offset (the file is sparse, so it uses no disk).
func TestParallelCheckoutOffsetsAbove4GiB(t *testing.T) {
	for _, size := range []int64{5<<30 + 12345, 100_000_000_007, 1 << 40} {
		ts := splitFile(&copyFile{}, size)
		var next, high int64
		for _, task := range ts {
			if task.off != next || task.n <= 0 || task.n > copyRange || task.whole {
				t.Fatalf("size %d: range at %d of %d bytes, expected %d next", size, task.off, task.n, next)
			}
			next += task.n
			if task.off > 1<<32 {
				high++
			}
		}
		if next != size || high == 0 || int64(len(ts)) != (size+copyRange-1)/copyRange {
			t.Fatalf("size %d: ranges cover %d bytes, %d of them above 2^32, %d tasks", size, next, high, len(ts))
		}
	}
	if ts := splitFile(&copyFile{}, 2*copyRange-1); len(ts) != 1 || !ts[0].whole {
		t.Fatalf("a file under two ranges is one task: %v", ts)
	}

	const off = 5<<30 + 12345
	v := offsetFS{size: off + 3000}
	p := newCopyPool(context.Background(), v, &counter{}, &atomic.Int64{})
	path := filepath.Join(t.TempDir(), "big.bin")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &copyFile{name: "big.bin", dst: dst, readers: make([]fs.File, p.workers)}
	f.pending.Store(1)
	p.tasks <- copyTask{f: f, off: off, n: 3000}
	if err := p.wait(nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != off+3000 {
		t.Fatalf("size %v (%v), want %d", info, err, int64(off+3000))
	}
}

// offsetFS is a file system of one file of size zero bytes that can be read at any offset; it holds no data.
type offsetFS struct{ size int64 }

func (offsetFS) Open(name string) (fs.File, error) { return nil, fs.ErrNotExist }
func (o offsetFS) OpenContext(_ context.Context, name string) (fs.File, error) {
	return &zeroFile{size: o.size, name: name}, nil
}
func (o offsetFS) Stat(name string) (fs.FileInfo, error) { return zeroInfo{o.size, name}, nil }
func (o offsetFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return []fs.DirEntry{fs.FileInfoToDirEntry(zeroInfo{o.size, "big.bin"})}, nil
}

type zeroInfo struct {
	size int64
	name string
}

func (z zeroInfo) Name() string           { return z.name }
func (z zeroInfo) Size() int64            { return z.size }
func (z zeroInfo) Mode() fs.FileMode      { return 0o644 }
func (z zeroInfo) ModTime() (t time.Time) { return }
func (z zeroInfo) IsDir() bool            { return false }
func (z zeroInfo) Sys() any               { return nil }

type zeroFile struct {
	size int64
	name string
	pos  int64
}

func (z *zeroFile) Stat() (fs.FileInfo, error) { return zeroInfo{z.size, z.name}, nil }
func (z *zeroFile) Close() error               { return nil }
func (z *zeroFile) Read(p []byte) (int, error) {
	n, err := z.ReadAt(p, z.pos)
	z.pos += int64(n)
	return n, err
}
func (z *zeroFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= z.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), z.size-off))
	clear(p[:n])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// A read that fails in the middle of one range ends the checkout with that error, closes every file and leaves
// nothing in the folder.
func TestParallelCheckoutReadError(t *testing.T) {
	smallRanges(t)
	data := randomBytes(t, 9000)
	m := fstest.MapFS{"f.bin": &fstest.MapFile{Data: data}}
	v := failFS{FS: m, at: 4000}
	dest := t.TempDir()
	p, err := planCheckout(m, kindFile, limits{bytes: 1 << 20, nodes: 10})
	if err != nil {
		t.Fatal(err)
	}
	var written counter
	var n atomic.Int64
	_, err = testWriter(t, dest).checkout(context.Background(), v, p, &written, &n)
	if !errors.Is(err, errBadRead) {
		t.Fatalf("checkout: %v", err)
	}
	assertEmpty(t, dest)
	if n.Load() != 0 {
		t.Fatalf("%d files counted", n.Load())
	}
}

var errBadRead = errors.New("bad read")

// failFS opens files whose ReadAt fails at or past offset at.
type failFS struct {
	fs.FS
	at int64
}

func (f failFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err != nil || info.IsDir() {
		return file, err
	}
	return &failFile{File: file, at: f.at}, nil
}

type failFile struct {
	fs.File
	at int64
}

func (f *failFile) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.at {
		return 0, errBadRead
	}
	return f.File.(io.ReaderAt).ReadAt(p, off)
}

// A cancel in the middle ends the checkout at once and leaves nothing.
func TestParallelCheckoutCancel(t *testing.T) {
	smallRanges(t)
	m := fstest.MapFS{}
	for i := range 50 {
		m[fmt.Sprintf("d/f%02d", i)] = &fstest.MapFile{Data: randomBytes(t, 8000)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var seen atomic.Int64
	v := watchFS{m, func(string) {
		if seen.Add(1) == 5 {
			cancel()
		}
	}}
	dest := t.TempDir()
	p, err := planCheckout(m, kindFolder, limits{bytes: 1 << 30, nodes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var written counter
	var n atomic.Int64
	_, err = testWriter(t, dest).checkout(ctx, v, p, &written, &n)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("checkout: %v", err)
	}
	assertEmpty(t, dest)
}
