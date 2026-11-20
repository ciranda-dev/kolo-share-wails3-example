package main

import (
	"runtime/debug"
	"testing"
)

func TestMemoryLimit(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		name     string
		env      string
		physical uint64
		want     int64
	}{
		{"half of 16 GiB", "", 16 * gib, 8 * gib},
		{"half of 64 GiB", "", 64 * gib, 32 * gib},
		{"never under 512 MiB", "", 256 << 20, 512 << 20},
		{"1 GiB is the floor", "", 1 * gib, 512 << 20},
		{"unknown memory", "", 0, 2 * gib},
		{"GOMEMLIMIT set leaves it to the runtime", "3GiB", 16 * gib, 0},
		{"GOMEMLIMIT off is also the owner's choice", "off", 16 * gib, 0},
	} {
		if got := memoryLimit(c.env, c.physical); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLimitMemorySetsTheRuntimeLimit(t *testing.T) {
	was := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(was)
	if physicalMemory() == 0 {
		t.Skip("this system does not report its memory")
	}
	got := limitMemory(func(string) string { return "" })
	if got < minMemoryLimit || debug.SetMemoryLimit(-1) != got {
		t.Fatalf("limit %d, runtime %d", got, debug.SetMemoryLimit(-1))
	}
	debug.SetMemoryLimit(was)
	if got := limitMemory(func(k string) string { return map[string]string{"GOMEMLIMIT": "1GiB"}[k] }); got != 0 || debug.SetMemoryLimit(-1) != was {
		t.Fatalf("GOMEMLIMIT set: %d, runtime %d", got, debug.SetMemoryLimit(-1))
	}
}

func TestPhysicalMemoryIsPlausible(t *testing.T) {
	n := physicalMemory()
	if n == 0 {
		t.Skip("this system does not report its memory")
	}
	if n < 64<<20 || n > 1<<50 {
		t.Fatalf("%d bytes of memory", n)
	}
}
