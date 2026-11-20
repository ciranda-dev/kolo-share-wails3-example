package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The sizes of the tests below are above 2^32 and 100e9: a 32-bit count anywhere in the path breaks them.
const (
	gb100 = 100_000_000_000
	tib1  = 1 << 40
)

func spaceService(free map[string]int64, vol func(string) string) *ShareService {
	s := &ShareService{}
	s.hooks.freeSpace = func(dir string) (int64, error) {
		if n, ok := free[dir]; ok {
			return n, nil
		}
		return 0, errors.New("unknown volume")
	}
	s.hooks.volume = vol
	return s
}

func TestStoreNeedForLargeItems(t *testing.T) {
	// The two stores measured with real items: 3 GiB took 3.97 GiB on disk and 4 GiB took 4.95 GiB.
	for _, m := range []struct{ item, store float64 }{{3, 3.97}, {4, 4.95}} {
		if need := float64(storeNeed(int64(m.item*(1<<30)))) / (1 << 30); need < m.store {
			t.Fatalf("a %.0f GiB item takes %.2f GiB of store, storeNeed says %.2f", m.item, m.store, need)
		}
	}
	for _, size := range []int64{1 << 20, tunedStoreFrom - 1, tunedStoreFrom, gb100, tib1, 1<<62 - 1<<56} {
		need := storeNeed(size)
		if need < size+size/spaceShare || need > size+size/8+spaceMargin(size) {
			t.Fatalf("storeNeed(%d) = %d", size, need)
		}
		if b := storeBound(size); b < need || b < 0 {
			t.Fatalf("storeBound(%d) = %d", size, b)
		}
		if saveNeed(size) < size {
			t.Fatalf("saveNeed(%d) = %d", size, saveNeed(size))
		}
	}
}

func TestReserveRefusesAndReleases(t *testing.T) {
	two := func(dir string) string { return dir } // every folder is a disk of its own
	s := spaceService(map[string]int64{"/data": 150e9, "/dest": 120e9}, two)
	rel, se := s.reserve(spaceNeed{"/data", storeNeed(gb100), "download"}, spaceNeed{"/dest", saveNeed(gb100), "save"})
	if se != nil {
		t.Fatalf("two disks that fit: %v", se)
	}
	// The first holds 106 GB of /data: a second item of 100 GB finds 44 GB.
	_, se = s.reserve(spaceNeed{"/data", storeNeed(gb100), "download"})
	if se == nil || se.Reason != "no-space-download" || !errors.Is(se, ErrNoSpace) || se.Kind != kindDisk {
		t.Fatalf("a second 100 GB item: %+v", se)
	}
	if want := humanBytes(storeNeed(gb100)); se.Detail != want {
		t.Fatalf("needs %q, want %q", se.Detail, want)
	}
	if want := humanBytes(150e9 - storeNeed(gb100)); se.Cause != want {
		t.Fatalf("free %q, want %q", se.Cause, want)
	}
	if strings.Contains(se.Message, "{") || !strings.Contains(se.Message, se.Detail) || !strings.Contains(se.Message, se.Cause) {
		t.Fatalf("sentence %q", se.Message)
	}
	rel()
	rel() // giving back twice is harmless
	rel2, se := s.reserve(spaceNeed{"/data", storeNeed(gb100), "download"})
	if se != nil {
		t.Fatalf("after the release: %v", se)
	}
	rel2()
	if len(s.space.held) != 0 {
		t.Fatalf("held: %v", s.space.held)
	}
	// Only the save folder is short.
	s = spaceService(map[string]int64{"/data": 500e9, "/dest": 50e9}, two)
	if _, se := s.reserve(spaceNeed{"/data", storeNeed(gb100), "download"}, spaceNeed{"/dest", saveNeed(gb100), "save"}); se == nil || se.Reason != "no-space-save" {
		t.Fatalf("short destination: %+v", se)
	}
	if len(s.space.held) != 0 {
		t.Fatalf("a refusal holds nothing: %v", s.space.held)
	}
}

func TestReserveSameDiskAddsBoth(t *testing.T) {
	one := func(string) string { return "disk" }
	// 150 GB hold the store (106 GB) or the files (100 GB), not both.
	s := spaceService(map[string]int64{"/data": 150e9, "/dest": 150e9}, one)
	_, se := s.reserve(spaceNeed{"/data", storeNeed(gb100), "download"}, spaceNeed{"/dest", saveNeed(gb100), "save"})
	if se == nil || se.Reason != "no-space-both" {
		t.Fatalf("one disk: %+v", se)
	}
	if want := humanBytes(storeNeed(gb100) + saveNeed(gb100)); se.Detail != want {
		t.Fatalf("needs %q, want %q", se.Detail, want)
	}
	s = spaceService(map[string]int64{"/data": 230e9}, one)
	rel, se := s.reserve(spaceNeed{"/data", storeNeed(gb100), "download"}, spaceNeed{"/data", saveNeed(gb100), "save"})
	if se != nil {
		t.Fatalf("230 GB hold 206 GB: %v", se)
	}
	rel()
}

func TestReserveIgnoresAVolumeItCannotAsk(t *testing.T) {
	s := spaceService(nil, func(d string) string { return d })
	rel, se := s.reserve(spaceNeed{"/gone", gb100, "save"})
	if se != nil {
		t.Fatalf("an unknown volume is not refused: %v", se)
	}
	rel()
}

func TestReserveIsSafeForConcurrentTransfers(t *testing.T) {
	s := spaceService(map[string]int64{"/data": 1000e9}, func(d string) string { return d })
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for range 20 {
		wg.Go(func() {
			rel, se := s.reserve(spaceNeed{"/data", 100e9, "prepare"})
			if se == nil {
				mu.Lock()
				granted++
				mu.Unlock()
				_ = rel
			}
		})
	}
	wg.Wait()
	if granted != 10 {
		t.Fatalf("1000 GB held %d items of 100 GB", granted)
	}
}

func TestDiskFreeOfARealDirectory(t *testing.T) {
	dir := t.TempDir()
	n, err := diskFree(dir)
	if err != nil || n <= 0 {
		t.Fatalf("diskFree(%s) = %d, %v", dir, n, err)
	}
	if v := volumeOf(dir); v == "" || v != volumeOf(filepath.Join(dir)) {
		t.Fatalf("volume %q", v)
	}
	if _, err := diskFree(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing folder has no free space")
	}
}

// Every no-space reason has its sentence, and the sentences name what is needed and what is free.
func TestNoSpaceSentences(t *testing.T) {
	for _, r := range []string{"no-space-prepare", "no-space-download", "no-space-save", "no-space-both"} {
		got := noSpace(r, gb100, 12e9)
		if !strings.Contains(got.Message, humanBytes(gb100)) || !strings.Contains(got.Message, humanBytes(12e9)) || got.Kind != kindDisk {
			t.Fatalf("%s: %q", r, got.Message)
		}
	}
}

// A send that finds no room for its copy fails before it stores a byte, with the sentence that says how much.
func TestSendRefusesWithoutSpace(t *testing.T) {
	var free int64 = 32 << 20
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) {
		s.hooks.freeSpace = func(string) (int64, error) { return free, nil }
	}, nil)
	sa, _ := connect(t, alice, bob)
	id, err := alice.Send(sa, writeFile(t, "small.bin", randomBytes(t, 1<<20)))
	if err != nil {
		t.Fatal(err)
	}
	tr := alice.rec.wait(t, id, stateFailed)
	if tr.Error == nil || tr.Error.Reason != "no-space-prepare" || !errors.Is(tr.Error, ErrNoSpace) {
		t.Fatalf("send: %+v", tr.Error)
	}
	if entries, _ := os.ReadDir(filepath.Join(alice.data, transfersDir)); len(entries) != 1 { // only the marker
		t.Fatalf("a refused send left %v", entries)
	}
	free = 10 << 30
	offerItem(t, alice, sa, writeFile(t, "small2.bin", randomBytes(t, 1<<20)))
}

// An offer of 100 GB, declared in its metadata, is shown, checked against the limit and against the disk, and its
// progress is counted in int64.
func TestReceiveChecksSpaceForA100GBOffer(t *testing.T) {
	var free int64 = 50e9
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) {
		s.hooks.tamperOffer = func(o *offer) { o.bytes = gb100 }
	}, func(s *ShareService) {
		s.hooks.freeSpace = func(string) (int64, error) { return free, nil }
	})
	sa, sb := connect(t, alice, bob)
	bob.SetSessionFolder(sb, t.TempDir())
	offerItem(t, alice, sa, writeFile(t, "huge.bin", randomBytes(t, 1<<20)))
	in := incoming(t, bob, sb, "huge.bin")
	if in.BytesTotal != gb100 {
		t.Fatalf("the card shows %d bytes", in.BytesTotal)
	}
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	got := bob.rec.wait(t, in.ID, stateFailed)
	if got.Error == nil || got.Error.Reason != "no-space-both" || got.BytesDone != 0 {
		t.Fatalf("50 GB for a 100 GB item: %+v", got.Error)
	}
	if want := humanBytes(storeNeed(gb100) + saveNeed(gb100)); got.Error.Detail != want {
		t.Fatalf("needs %q, want %q", got.Error.Detail, want)
	}
	if got.Error.Cause != "46.6 GiB" {
		t.Fatalf("free %q", got.Error.Cause)
	}
}

// With room, a 100 GB offer is pulled; the real item is 1 MiB, so the check of the tree against the offer ends it as a
// mismatch, with every count above 2^32 intact on the way.
func TestReceiveOfA100GBOfferWithRoom(t *testing.T) {
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) {
		s.hooks.tamperOffer = func(o *offer) { o.bytes = gb100 }
	}, func(s *ShareService) {
		s.hooks.freeSpace = func(string) (int64, error) { return 4 << 40, nil }
	})
	sa, sb := connect(t, alice, bob)
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	offerItem(t, alice, sa, writeFile(t, "huge.bin", randomBytes(t, 1<<20)))
	in := incoming(t, bob, sb, "huge.bin")
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	got := bob.rec.wait(t, in.ID, stateFailed, stateDone)
	if got.State != stateFailed || got.Error == nil || got.Error.Reason != "offer-mismatch" {
		t.Fatalf("receive: %+v %+v", got, got.Error)
	}
	if got.BytesTotal != gb100 || got.BytesDone > got.BytesTotal {
		t.Fatalf("progress %d of %d", got.BytesDone, got.BytesTotal)
	}
	assertEmpty(t, dest)
}

func TestDefaultDownloadLimitIs1TiB(t *testing.T) {
	c, err := LoadConfig(nil, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	c.DataDir = t.TempDir()
	s, err := c.resolve()
	if err != nil || s.MaxDownload != tib1 || defaultMaxDownload != tib1 {
		t.Fatalf("default limit %d (%v)", s.MaxDownload, err)
	}
	// The limit is a setting: a variable lowers or raises it, and the checks of a bad value stay.
	for _, v := range []int64{gb100, 4 * tib1} {
		c, err := LoadConfig(nil, func(k string) string {
			if k == "KOLO_SHARE_MAX_DOWNLOAD" {
				return strconv.FormatInt(v, 10)
			}
			return ""
		}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		c.DataDir = t.TempDir()
		if s, err := c.resolve(); err != nil || s.MaxDownload != v {
			t.Fatalf("KOLO_SHARE_MAX_DOWNLOAD=%d gives %d (%v)", v, s.MaxDownload, err)
		}
	}
}

// An offer of 2 TiB is declined at once with the limit in the sentence; with the limit raised it is shown.
func TestOffersAroundTheDefaultLimit(t *testing.T) {
	for _, c := range []struct {
		limit, size int64
		declined    bool
	}{{0, 2 * tib1, true}, {0, tib1, false}, {4 * tib1, 2 * tib1, false}} {
		alice, bob := networkWith(t, Config{}, Config{MaxDownload: c.limit}, func(s *ShareService) {
			s.hooks.tamperOffer = func(o *offer) { o.bytes = c.size }
		}, nil)
		sa, sb := connect(t, alice, bob)
		bob.SetSessionFolder(sb, t.TempDir())
		id, err := alice.Send(sa, writeFile(t, "size.bin", randomBytes(t, 1<<20)))
		if err != nil {
			t.Fatal(err)
		}
		if c.declined {
			alice.rec.wait(t, id, stateDeclined)
			if n := bob.Sessions()[0].Notice; n == nil || n.Reason != "too-large" || n.Detail != "1 TiB" {
				t.Fatalf("notice %+v", n)
			}
			continue
		}
		alice.rec.wait(t, id, stateOffered)
		if in := incoming(t, bob, sb, "size.bin"); in.BytesTotal != c.size {
			t.Fatalf("the card shows %d bytes, want %d", in.BytesTotal, c.size)
		}
	}
}

// Every sentence of the disk checks has its text in the three languages of the window, with the same two places.
func TestNoSpaceSentencesAreTranslated(t *testing.T) {
	for _, lang := range []string{"en", "es", "pt"} {
		src, err := os.ReadFile(filepath.Join("frontend", "src", "messages", lang+".js"))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []string{"no-space-prepare", "no-space-download", "no-space-save", "no-space-both", "disk-full"} {
			key := "'error." + r + "': '"
			i := strings.Index(string(src), key)
			if i < 0 {
				t.Fatalf("%s.js has no text for %s", lang, r)
			}
			line := string(src)[i:]
			line = line[:strings.IndexByte(line, '\n')]
			want := strings.Count(reasons[r].text, "{detail}")
			if got := strings.Count(line, "{detail}"); got != want {
				t.Fatalf("%s.js %s has %d of {detail}, the Go text has %d", lang, r, got, want)
			}
			if got, want := strings.Count(line, "{cause}"), strings.Count(reasons[r].text, "{cause}"); got != want {
				t.Fatalf("%s.js %s has %d of {cause}, the Go text has %d", lang, r, got, want)
			}
		}
	}
}
