package main

import (
	"encoding/json"
	"log"
	"os"
	"runtime"
	"strings"
	"testing"
)

// A window's error reaches the log as one entry, cleaned and cut to maxReportRunes characters.
func TestReportError(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	flags := log.Flags()
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	s := NewShareService(Config{}, func(string, any) {}, nil)

	s.ReportError("TypeError: x is null\x1b[31m\u202e", "    at App\n    at Rail\n")
	if got, want := buf.String(), "frontend error: TypeError: x is null[31m\n    at App\n    at Rail\n"; got != want {
		t.Errorf("logged %q, want %q", got, want)
	}

	buf.Reset()
	long := "TypeError: " + strings.Repeat("é", 2*maxReportRunes)
	s.ReportError(long, "    at App")
	if got, want := buf.String(), "frontend error: "+string([]rune(long)[:maxReportRunes])+"\n"; got != want {
		t.Errorf("logged %d characters, want the %d of the bound and nothing else", len([]rune(got)), len([]rune(want)))
	}
}

// A message of hundreds of megabytes is cut before it is cleaned, so it costs no more memory than a small one.
func TestReportErrorCutsBeforeCleaning(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	flags := log.Flags()
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	s := NewShareService(Config{}, func(string, any) {}, nil)
	huge := strings.Repeat("\u00e9", 8<<20) // 16 MiB
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s.ReportError(huge, huge)
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 4<<20 {
		t.Fatalf("a 32 MiB error allocated %d bytes", got)
	}
	if n := len([]rune(buf.String())); n > len("frontend error: ")+maxReportRunes+1 {
		t.Fatalf("logged %d characters", n)
	}
}

// An event reaches the window as Go sent it, so a nil slice would be null: every list the service sends or
// returns, empty, is [].
func TestListsAreNeverNull(t *testing.T) {
	var sent []any
	s := NewShareService(Config{}, func(_ string, data any) { sent = append(sent, data) }, nil)
	h, err := openHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.hist = h
	s.filesDropped(nil)
	if err := s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d events, want 2", len(sent))
	}
	for _, v := range append(sent, s.Sessions(), s.History(), s.Transfers()) {
		if b, _ := json.Marshal(v); string(b) != "[]" {
			t.Errorf("%T is %s, want []", v, b)
		}
	}
}
