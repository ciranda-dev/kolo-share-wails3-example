package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf16"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	kolofiles "github.com/ciranda-dev/kolo-files"
)

const (
	// maxCopies is how many "name (N)" a receive tries when the name is taken in the folder.
	maxCopies = 99
	// partPrefix and partSuffix name the entry a receive writes into before it takes its final name.
	partPrefix = ".kolo-share-"
	partSuffix = ".part"
)

// limits bound one receive: bytes downloaded and saved, and files plus folders.
type limits struct {
	bytes int64
	nodes int
	rules lengthRules // the names and paths this system can save; zero checks none
}

func limitsOf(s settings) limits {
	return limits{bytes: s.MaxDownload, nodes: s.MaxFiles, rules: rulesFor(runtime.GOOS)}
}

// metaEntries bounds the metadata log: the app's sender writes one entry to start the volume and one per file
// or folder.
func (l limits) metaEntries() uint64 { return uint64(l.nodes) + 1 }

// contentEntries bounds the content log: a file takes 3 or 4 objects (its blob's root and chunk, and an object
// table) and every further chunk one more, and kolo-blobs never cuts a chunk under MinChunkSize but the last.
// The 2x and the 64 leave room for tree nodes.
func (l limits) contentEntries() uint64 {
	return 4*uint64(l.nodes) + 2*uint64(l.bytes/koloblobs.MinChunkSize) + 64
}

// cleanName replaces in one name every control and format character (Unicode Cc and Cf: the bidi controls
// U+202A to U+202E and U+2066 to U+2069, the marks U+200E and U+200F, zero-width characters) with "_", so a
// saved name shows what it is: "invoice‮gpj.exe" is saved as "invoice_gpj.exe".
func cleanName(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return '_'
		}
		return r
	}, name)
}

// lengthRules are the limits of the system that saves: the longest name and path it can create, in bytes (a
// Windows name is checked by LocalPath, in UTF-16 units, and a Windows path is counted in bytes of UTF-16).
type lengthRules struct {
	name int // bytes of one name, 0 for no check here
	path int // bytes of a whole path, with the room for the terminating NUL taken off
	wide bool
}

// rulesFor gives the limits of goos: 255 bytes a name on macOS and Linux, a path under PATH_MAX (1024 on macOS,
// 4096 on Linux), and the 32767 UTF-16 units of an extended-length path on Windows.
func rulesFor(goos string) lengthRules {
	switch goos {
	case "windows":
		return lengthRules{path: 2 * (1<<15 - 1), wide: true}
	case "darwin":
		return lengthRules{name: 255, path: 1023}
	}
	return lengthRules{name: 255, path: 4095}
}

// copyRoom is what " (99)" adds to a top name that is already taken in the folder.
const copyRoom = len(" (99)")

// measure is the length of s in the rules' unit.
func (r lengthRules) measure(s string) int {
	if r.wide {
		return 2 * len(utf16.Encode([]rune(s)))
	}
	return len(s)
}

// savedPath is the host path, relative to the top entry's folder, of the volume path name: every name cleaned,
// then kolo-files' LocalPath, which refuses on Windows the names Windows reserves or cannot create, and a check
// that no name is longer than the system allows.
func savedPath(r lengthRules, name string) (string, error) {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = cleanName(p)
		if n := r.measure(parts[i]); r.name > 0 && n > r.name {
			return "", nameLength(n, r.name)
		}
	}
	return localPath(strings.Join(parts, "/"))
}

// savedTop is savedPath for the top entry, which leaves room for the " (N)" of a name that is taken.
func savedTop(r lengthRules, name string) (string, error) {
	local, err := savedPath(r, name)
	if err == nil && r.name > 0 && r.measure(local)+copyRoom > r.name {
		return "", nameLength(r.measure(local)+copyRoom, r.name)
	}
	return local, err
}

// nameLength is the error of a name of n bytes where the system takes at most limit. It names the length only,
// never the name.
func nameLength(n, limit int) *ShareError {
	se := shareErr("name-length", ErrNameLength, strconv.Itoa(n))
	se.Cause = strconv.Itoa(limit)
	se.Message = sentence("name-length", se.Detail, se.Cause)
	return se
}

// pathLength is the error of a path of n bytes where the system takes at most limit.
func pathLength(n, limit int) *ShareError {
	se := shareErr("path-length", ErrPathLength, strconv.Itoa(n))
	se.Cause = strconv.Itoa(limit)
	se.Message = sentence("path-length", se.Detail, se.Cause)
	return se
}

// localPath is kolo-files' LocalPath; tests put a stricter one in its place to play Windows on any system.
var localPath = kolofiles.LocalPath

// plan is what a checkout writes: one top entry, a file or a folder, with this many files, nodes and bytes.
type plan struct {
	top   string // the top entry's name in the volume
	local string // the name it is saved under, before a "(2)"
	dir   bool
	files int
	nodes int
	bytes int64
	// longest is the length of the longest saved path, from the top name on, with room for " (99)".
	longest int
	rules   lengthRules
}

// fits refuses a plan whose longest path, saved in folder, is longer than the system opens. The folder is not
// known where an item is offered, so this runs where it is saved.
func (p plan) fits(folder string) error {
	if n := p.rules.measure(folder) + 1 + p.longest; p.rules.path > 0 && n > p.rules.path {
		return pathLength(n, p.rules.path)
	}
	return nil
}

// planCheckout checks the whole tree before anything is written: one top entry of the code's kind, every name
// safe on this system, at most lim.nodes files and folders and at most lim.bytes bytes.
func planCheckout(v fs.FS, kind string, lim limits) (plan, error) {
	top, err := fs.ReadDir(v, ".")
	if err != nil {
		return plan{}, err
	}
	if len(top) != 1 || top[0].IsDir() != (kind == kindFolder) {
		return plan{}, ErrWrongKind
	}
	p := plan{top: top[0].Name(), dir: top[0].IsDir(), rules: lim.rules}
	if p.local, err = savedTop(lim.rules, p.top); err != nil {
		return plan{}, err
	}
	err = fs.WalkDir(v, p.top, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p.nodes++; p.nodes > lim.nodes {
			return ErrTooManyFiles
		}
		local, err := savedPath(lim.rules, name)
		if err != nil {
			return err
		}
		p.longest = max(p.longest, lim.rules.measure(local)+copyRoom)
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		p.files++
		if p.bytes += info.Size(); p.bytes > lim.bytes || p.bytes < 0 {
			return ErrTooLarge
		}
		return nil
	})
	return p, err
}

// writer saves receives into one folder. mark sets the "downloaded" mark of the system on a file or folder it
// made; pending records the entries a receive is writing, so a crash leaves nothing that the next start does
// not remove.
type writer struct {
	folder  string
	rules   lengthRules
	mark    func(f *os.File) error
	pending *pendingList
}

func (s *ShareService) writer(folder string) writer {
	return writer{folder: folder, rules: s.limitsNow().rules, mark: s.mark, pending: s.pending}
}

// checkout writes the planned tree of v into the folder and returns the path of what it wrote. As in kolo-files'
// guide, every folder and file is created under an os.Root with Mkdir and O_EXCL, so no name leaves the folder,
// no link in it is followed, and a name that aliases another on this disk fails instead of overwriting. The tree
// is written under a ".kolo-share-*.part" name and renamed onto a name reserved first ("name", "name (2)", ...)
// once all of it is there. On any error, both are removed.
func (w writer) checkout(ctx context.Context, v fs.FS, p plan, written io.Writer, files *atomic.Int64) (string, error) {
	root, err := os.OpenRoot(w.folder)
	if err != nil {
		return "", err
	}
	defer root.Close()
	final, err := createTop(root, p.local, p.dir)
	if err != nil {
		return "", err
	}
	var rnd [8]byte
	rand.Read(rnd[:])
	part := partPrefix + hex.EncodeToString(rnd[:]) + partSuffix
	entry := pendingEntry{Folder: w.folder, Part: part, Top: final}
	if err := w.pending.add(entry); err != nil {
		root.Remove(final)
		return "", err
	}
	err = w.writeTree(ctx, v, root, p, part, written, files)
	if err == nil {
		err = context.Cause(ctx) // a cancel that came during the last copy, which a whole file makes without a check
	}
	if err == nil {
		err = renameOnto(root, part, final, p.dir)
	}
	if err != nil {
		root.RemoveAll(part)
		root.Remove(final)
		err = inShare(err, part, p.local)
	}
	w.pending.remove(entry)
	if err != nil {
		return "", err
	}
	return filepath.Join(w.folder, final), nil
}

// inShare names a path error by its place in the share, top/..., not by the temporary part name it was written
// under, so a sentence never shows ".kolo-share-*.part".
func inShare(err error, part, top string) error {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return err
	}
	if rest, ok := strings.CutPrefix(pe.Path, part); ok {
		p := *pe
		p.Path = top + rest
		return fmt.Errorf("%w", &p)
	}
	return err
}

// renameOnto moves part onto the reserved name final. A file replaces its empty placeholder; a folder replaces
// an empty one on Unix, and Windows wants the placeholder gone first.
func renameOnto(root *os.Root, part, final string, dir bool) error {
	err := root.Rename(part, final)
	if err != nil && dir {
		if rerr := root.Remove(final); rerr == nil {
			err = root.Rename(part, final)
		}
	}
	return err
}

// createTop reserves the first free name of name, "name (2)", ...: an empty folder, or an empty file.
func createTop(root *os.Root, name string, dir bool) (string, error) {
	for i := 1; i <= maxCopies; i++ {
		n := copyName(name, i, dir)
		var err error
		if dir {
			err = root.Mkdir(n, 0o777)
		} else {
			var f *os.File
			if f, err = root.OpenFile(n, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666); err == nil {
				err = f.Close()
			}
		}
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", &ShareError{Kind: kindDisk, Reason: "copies-exhausted", Detail: name, Message: sentence("copies-exhausted", name, ""), Err: fs.ErrExist}
}

// copyName is name for i == 1, else name with " (i)" before the extension of a file.
func copyName(name string, i int, dir bool) string {
	if i == 1 {
		return name
	}
	ext := ""
	if !dir {
		ext = filepath.Ext(name)
		if ext == name {
			ext = "" // ".profile" has no extension to keep
		}
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
}

// openContext opens name of v with a context that ends its reads, when v can.
func openContext(ctx context.Context, v fs.FS, name string) (fs.File, error) {
	if c, ok := v.(interface {
		OpenContext(context.Context, string) (fs.File, error)
	}); ok {
		return c.OpenContext(ctx, name)
	}
	return v.Open(name)
}

// writeTree writes the planned tree of v under base, a new name in root. Folders are made in order; the content of
// the files is copied on a few goroutines (see copyPool).
func (w writer) writeTree(ctx context.Context, v fs.FS, root *os.Root, p plan, base string, written io.Writer, files *atomic.Int64) error {
	marked := false
	mark := func(f *os.File) {
		if err := w.mark(f); err != nil && !marked {
			marked = true
			log.Print("could not mark the received files as downloaded: ", err)
		}
	}
	pool := newCopyPool(ctx, v, written, files)
	err := fs.WalkDir(v, p.top, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := pool.ctx.Err(); err != nil {
			return context.Cause(pool.ctx)
		}
		to := base
		if rel := strings.TrimPrefix(name, p.top+"/"); name != p.top {
			local, err := savedPath(w.rules, rel)
			if err != nil {
				return err
			}
			to = filepath.Join(base, local)
		}
		if d.IsDir() {
			if err := root.Mkdir(to, 0o777); err != nil {
				return err
			}
			if f, err := root.Open(to); err == nil {
				mark(f)
				f.Close()
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		dst, err := root.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			return err
		}
		mark(dst)
		return pool.add(name, dst, info.Size())
	})
	return pool.wait(err)
}

// copyRange is the part of a large file that one task copies: a range is read and written in order, so a chunk that
// straddles two ranges is read twice, one chunk in 64. copyBuffer is each worker's read and write buffer. Tests
// make both small.
var (
	copyRange  int64 = 64 << 20
	copyBuffer       = 4 << 20
)

// maxCopyWorkers bounds the goroutines that copy: reading a chunk back from the store checks it and decrypts it,
// about a gigabyte a second on one core, and a few of them keep a disk busy. The big benchmark lowers it to 1.
var maxCopyWorkers = 4

// copyFile is one file being written: its tasks are done when pending reaches 0, and the last one closes it.
type copyFile struct {
	name    string
	dst     *os.File
	pending atomic.Int32
	readers []fs.File // one per worker, opened by it on first use and closed with the file
}

type copyTask struct {
	f      *copyFile
	off, n int64
	whole  bool // the whole file, read in order, for a file that is not seekable or small
}

// copyPool copies the content of the files of one checkout on a few goroutines: a file smaller than two ranges is
// one task, a larger one is split into ranges that run side by side, each worker with its own reader of the
// file. The walk that makes the folders and creates the files runs ahead, bounded by the queue.
type copyPool struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	v       fs.FS
	written io.Writer
	files   *atomic.Int64
	tasks   chan copyTask
	wg      sync.WaitGroup
	workers int
	mu      sync.Mutex
	failed  error
	open    []*copyFile // files created and not finished, closed on failure
}

func newCopyPool(ctx context.Context, v fs.FS, written io.Writer, files *atomic.Int64) *copyPool {
	workers := max(1, min(runtime.GOMAXPROCS(0), maxCopyWorkers))
	p := &copyPool{v: v, written: written, files: files, workers: workers, tasks: make(chan copyTask, 2*workers)}
	p.ctx, p.cancel = context.WithCancelCause(ctx)
	for id := range workers {
		p.wg.Go(func() { p.work(id) })
	}
	return p
}

// splitFile is the tasks that copy a file of size bytes: one for a file smaller than two ranges, else one per range.
func splitFile(f *copyFile, size int64) []copyTask {
	if size < 2*copyRange {
		return []copyTask{{f: f, n: size, whole: true}}
	}
	ts := make([]copyTask, 0, size/copyRange+1)
	for off := int64(0); off < size; off += copyRange {
		ts = append(ts, copyTask{f: f, off: off, n: min(copyRange, size-off)})
	}
	return ts
}

// add queues the copy of the file name into dst, which it owns from now on, and returns when the queue has room.
func (p *copyPool) add(name string, dst *os.File, size int64) error {
	f := &copyFile{name: name, dst: dst, readers: make([]fs.File, p.workers)}
	p.mu.Lock()
	p.open = append(p.open, f)
	p.mu.Unlock()
	ts := splitFile(f, size)
	f.pending.Store(int32(len(ts)))
	for _, t := range ts {
		select {
		case p.tasks <- t:
		case <-p.ctx.Done():
			return context.Cause(p.ctx)
		}
	}
	return nil
}

// wait ends the queue, waits for the workers and returns the first error: the walk's err, else a copy's. A file
// that was not finished is closed.
func (p *copyPool) wait(walkErr error) error {
	if walkErr != nil {
		p.cancel(walkErr)
	}
	close(p.tasks)
	p.wg.Wait()
	p.cancel(nil)
	p.mu.Lock()
	defer p.mu.Unlock()
	if walkErr == nil {
		walkErr = p.failed
	}
	for _, f := range p.open {
		if f.dst != nil {
			f.dst.Close()
		}
		for _, r := range f.readers {
			if r != nil {
				r.Close()
			}
		}
	}
	return walkErr
}

func (p *copyPool) fail(err error) {
	p.mu.Lock()
	if p.failed == nil {
		p.failed = err
	}
	p.mu.Unlock()
	p.cancel(err)
}

func (p *copyPool) work(id int) {
	buf := make([]byte, copyBuffer)
	defer clear(buf)
	for t := range p.tasks {
		if p.ctx.Err() != nil {
			continue
		}
		if err := p.copy(id, t, buf); err != nil {
			p.fail(err)
			continue
		}
		if t.f.pending.Add(-1) == 0 {
			p.finish(t.f)
		}
	}
}

// finish closes a file whose tasks are all done.
func (p *copyPool) finish(f *copyFile) {
	p.mu.Lock()
	dst := f.dst
	f.dst = nil
	readers := f.readers
	f.readers = nil
	p.mu.Unlock()
	for _, r := range readers {
		if r != nil {
			r.Close()
		}
	}
	if err := dst.Close(); err != nil {
		p.fail(err)
		return
	}
	p.files.Add(1)
}

// copy runs one task with the worker's buffer.
func (p *copyPool) copy(id int, t copyTask, buf []byte) error {
	r := t.f.readers[id]
	if r == nil {
		var err error
		if r, err = openContext(p.ctx, p.v, t.f.name); err != nil {
			return err
		}
		t.f.readers[id] = r
	}
	if t.whole {
		_, err := io.CopyBuffer(io.MultiWriter(t.f.dst, p.written), r, buf)
		return err
	}
	at, ok := r.(io.ReaderAt)
	if !ok {
		return fmt.Errorf("%s: content that cannot be read in ranges", t.f.name)
	}
	for off, end := t.off, t.off+t.n; off < end; {
		if err := p.ctx.Err(); err != nil {
			return context.Cause(p.ctx)
		}
		piece := buf[:min(int64(len(buf)), end-off)]
		n, err := at.ReadAt(piece, off)
		if n < len(piece) && err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		if _, err := t.f.dst.WriteAt(piece, off); err != nil {
			return err
		}
		p.written.Write(piece)
		off += int64(len(piece))
	}
	return nil
}
