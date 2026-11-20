package main

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync/atomic"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	kolofiles "github.com/ciranda-dev/kolo-files"
	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

// checkFolder says whether folder can take what a session saves.
func checkFolder(folder string) error {
	if folder == "" {
		return shareErr("folder-missing", ErrBadFolder, "")
	}
	if !filepath.IsAbs(folder) {
		return shareErr("folder-relative", ErrBadFolder, "")
	}
	info, err := os.Stat(folder)
	if err != nil {
		return shareErr("folder-unusable", err, folder)
	}
	if !info.IsDir() {
		return shareErr("folder-not-dir", ErrBadFolder, folder)
	}
	return nil
}

// volume is what a pull keeps from one attempt to the next, so that a new attempt asks only for the entries it
// lacks: the two replicas and, once the metadata is whole, the tree they describe.
type volume struct {
	meta, content *kolostore.Replica
	tree          *kolofiles.Volume // the metadata as a read-only volume; it owns meta once set
	late          lateContent       // the content backend of tree, set when the content is whole
	check         func(*kolofiles.Volume) error
	checked       bool
}

// lateContent is the content backend of a tree that is opened before its content is pulled: it serves nothing
// until set, which is after the whole content is in the store.
type lateContent struct {
	b atomic.Pointer[koloblobs.Backend]
}

func (l *lateContent) Get(ctx context.Context, h koloblobs.Hash) ([]byte, error) {
	b := l.b.Load()
	if b == nil {
		return nil, ErrIncomplete
	}
	return (*b).Get(ctx, h)
}

func (l *lateContent) Put(context.Context, koloblobs.Hash, []byte) error {
	return koloblobs.ErrReadOnly
}

// pullVolume copies the sender's two logs into vol, the metadata log first, and runs vol.check on the tree the
// metadata describes before a byte of content is pulled, so an item this computer cannot save is refused before
// it is downloaded. kolo-sync checks every entry against the sender's signed head, and each log's head is checked
// against the limits before a byte of it is pulled. pulling holds the replica being pulled, for the watcher. It
// can run again on the same vol after an error: what the replicas hold is verified and stays.
func pullVolume(ctx context.Context, conn kolosync.Conn, st *kolostore.Store, owner ed25519.PublicKey, metaID kololog.LogID, lim limits, pulling *atomic.Pointer[kolostore.Replica], vol *volume) error {
	if vol.tree == nil {
		if err := pullLog(ctx, conn, st, owner, metaID, lim.metaEntries(), pulling, &vol.meta); err != nil {
			return err
		}
		tree, err := kolofiles.OpenReader(vol.meta, &vol.late, nil)
		if err != nil {
			return err
		}
		vol.tree = tree
	}
	if !vol.checked && vol.check != nil {
		if err := vol.check(vol.tree); err != nil {
			return err
		}
		vol.checked = true
	}
	id, err := kolofiles.ContentLog(vol.meta)
	if err != nil {
		return err
	}
	return pullLog(ctx, conn, st, owner, id, lim.contentEntries(), pulling, &vol.content)
}

// headEntries is the size of a signed head; tests put a smaller one in its place to play a log that grows
// between its head and the pull.
var headEntries = func(h kololog.VerifiedHead) uint64 { return h.Size() }

// pull is kolo-sync's Pull with the "transfer:" log lines; tests count its calls.
var pull = meteredPull

// pullLog brings *slot, the replica of log id, up to date; it opens the replica first when *slot is nil.
func pullLog(ctx context.Context, conn kolosync.Conn, st *kolostore.Store, owner ed25519.PublicKey, id kololog.LogID, maxEntries uint64, pulling *atomic.Pointer[kolostore.Replica], slot **kolostore.Replica) error {
	head, err := kolosync.FetchHead(ctx, conn, owner, id)
	if err != nil {
		return err
	}
	if headEntries(head) > maxEntries {
		return ErrTooManyEntries
	}
	if *slot == nil {
		header, err := kolosync.FetchHeader(ctx, conn, owner, id)
		if err != nil {
			return err
		}
		if *slot, err = st.OpenReplica(owner, id, header); err != nil {
			return err
		}
	}
	rep := *slot
	pulling.Store(rep)
	if err := pull(ctx, conn, rep); err != nil {
		return err
	}
	if rep.Len() > maxEntries { // grown since its head: the watcher may not have seen it
		return ErrTooManyEntries
	}
	return nil
}
