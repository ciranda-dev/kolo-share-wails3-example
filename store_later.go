package main

import (
	"errors"
	"slices"
	"sync"

	kolostore "github.com/ciranda-dev/kolo-store"
)

const (
	// laterQueueMax is the bytes of batches that may wait to be written; a Commit past it waits for the disk.
	laterQueueMax = 64 << 20
	// laterBatchMax is the most bytes one write to the disk takes from the queue, at least one batch.
	laterBatchMax = 32 << 20
)

// laterBackend is a kolostore.Backend whose batches return from Commit when they are queued, and reach the disk in
// larger batches, one sync for many. It is for the stores of one transfer, which the app removes when the transfer
// ends and never reads again after a restart: there is nothing to keep across a crash. A kolo-store commit is a
// synced write, a few milliseconds on most disks, once for each chunk of about 1 MiB, and that, not the CPU, held a
// large item to about 100 MB/s.
//
// Reads see the queued writes, a Scan waits until they are written, and a batch with a range delete is written at
// once. The batches reach the disk in the order they were committed, so what is on disk at any moment is a prefix
// of them: a store opened after a crash holds fewer entries, never a hole. A failed write ends every later Commit
// with its error, and the queued data stays readable.
type laterBackend struct {
	in kolostore.Backend

	mu      sync.Mutex
	c       *sync.Cond // the queue grew or shrank, the backend closed or failed
	queue   []*laterBatch
	bytes   int
	overlay map[string]laterValue // the newest queued value of each key
	seq     uint64
	err     error
	closed  bool
	done    chan struct{} // closed when the writer goroutine ends
	writes  int           // writes to the disk so far, for tests
}

var errBatchUsed = errors.New("kolo-share: batch already committed")

type laterValue struct {
	val []byte
	del bool
	seq uint64
}

func newLaterBackend(in kolostore.Backend) *laterBackend {
	l := &laterBackend{in: in, overlay: map[string]laterValue{}, done: make(chan struct{})}
	l.c = sync.NewCond(&l.mu)
	go l.run()
	return l
}

type laterOp struct {
	kind       byte // 'p' put, 'd' delete, 'r' range delete
	key, value []byte
}

type laterBatch struct {
	l      *laterBackend
	ops    []laterOp
	size   int
	ranges bool
	used   bool
	seq    uint64
}

func (l *laterBackend) NewBatch() kolostore.Batch { return &laterBatch{l: l} }

func (b *laterBatch) Put(key, value []byte) {
	b.ops = append(b.ops, laterOp{'p', slices.Clone(key), slices.Clone(value)})
	b.size += len(key) + len(value) + 16
}

func (b *laterBatch) Delete(key []byte) {
	b.ops = append(b.ops, laterOp{'d', slices.Clone(key), nil})
	b.size += len(key) + 16
}

func (b *laterBatch) DeleteRange(start, end []byte) {
	b.ops = append(b.ops, laterOp{'r', slices.Clone(start), slices.Clone(end)})
	b.size += len(start) + len(end) + 16
	b.ranges = true
}

// apply puts the operations of bs, in order, into one batch of the real backend and commits it.
func (l *laterBackend) apply(bs []*laterBatch) error {
	nb := l.in.NewBatch()
	for _, b := range bs {
		for _, o := range b.ops {
			switch o.kind {
			case 'p':
				nb.Put(o.key, o.value)
			case 'd':
				nb.Delete(o.key)
			case 'r':
				nb.DeleteRange(o.key, o.value)
			}
		}
	}
	return nb.Commit()
}

func (b *laterBatch) Commit() error {
	l := b.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if b.used {
		return errBatchUsed
	}
	b.used = true
	if l.closed {
		return kolostore.ErrClosed
	}
	if b.ranges {
		// A range delete is rare (a log is removed): it goes straight to the disk, behind what is queued.
		if err := l.waitEmpty(); err != nil {
			return err
		}
		l.mu.Unlock()
		err := l.apply([]*laterBatch{b})
		l.mu.Lock()
		return err
	}
	for l.bytes > laterQueueMax && l.err == nil && !l.closed {
		l.c.Wait()
	}
	if l.err != nil {
		return l.err
	}
	if l.closed {
		return kolostore.ErrClosed
	}
	l.seq++
	b.seq = l.seq
	l.queue = append(l.queue, b)
	l.bytes += b.size
	for _, o := range b.ops {
		l.overlay[string(o.key)] = laterValue{val: o.value, del: o.kind == 'd', seq: b.seq}
	}
	l.c.Broadcast()
	return nil
}

// waitEmpty waits until every queued batch is on the disk, or one failed.
func (l *laterBackend) waitEmpty() error {
	for len(l.queue) > 0 && l.err == nil {
		l.c.Wait()
	}
	return l.err
}

// run writes the queue to the disk, many batches at a time, until the backend closes.
func (l *laterBackend) run() {
	defer close(l.done)
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		for len(l.queue) == 0 && !l.closed {
			l.c.Wait()
		}
		if len(l.queue) == 0 || l.err != nil {
			return
		}
		n, size := 0, 0
		for n < len(l.queue) && (n == 0 || size+l.queue[n].size <= laterBatchMax) {
			size += l.queue[n].size
			n++
		}
		take := slices.Clone(l.queue[:n])
		l.mu.Unlock()
		err := l.apply(take)
		l.mu.Lock()
		if err != nil {
			l.err = err
			l.c.Broadcast()
			return
		}
		l.writes++
		l.queue = slices.Delete(l.queue, 0, n)
		l.bytes -= size
		for _, b := range take {
			for _, o := range b.ops {
				if v, ok := l.overlay[string(o.key)]; ok && v.seq == b.seq {
					delete(l.overlay, string(o.key))
				}
				clear(o.value)
			}
		}
		l.c.Broadcast()
	}
}

func (l *laterBackend) Get(key []byte) ([]byte, error) {
	l.mu.Lock()
	if v, ok := l.overlay[string(key)]; ok {
		defer l.mu.Unlock()
		if v.del {
			return nil, kolostore.ErrNotFound
		}
		return slices.Clone(v.val), nil
	}
	l.mu.Unlock()
	return l.in.Get(key)
}

func (l *laterBackend) Put(key, value []byte) error {
	b := l.NewBatch()
	b.Put(key, value)
	return b.Commit()
}

func (l *laterBackend) Delete(key []byte) error {
	b := l.NewBatch()
	b.Delete(key)
	return b.Commit()
}

// Scan waits until the queue is written, so it reads what every Commit before it wrote.
func (l *laterBackend) Scan(start, end []byte, reverse bool, limit int, fn func(key, value []byte) bool) error {
	l.mu.Lock()
	err := l.waitEmpty()
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return l.in.Scan(start, end, reverse, limit, fn)
}

// Close writes what is queued, stops the writer and closes the real backend.
func (l *laterBackend) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	err := l.waitEmpty()
	l.closed = true
	l.c.Broadcast()
	l.mu.Unlock()
	<-l.done
	if cerr := l.in.Close(); err == nil {
		err = cerr
	}
	return err
}
