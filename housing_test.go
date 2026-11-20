package main

import (
	"math"
	"testing"
)

func TestNotchAreaOf(t *testing.T) {
	for _, c := range []struct {
		name string
		s    housingScreen
		want notchArea
	}{
		{"housing", housingScreen{SafeTop: 32, Width: 1512, Left: 656, Right: 656}, notchArea{Top: 32, Width: 200}},
		{"fractions round up", housingScreen{SafeTop: 37.5, Width: 1728, Left: 763.25, Right: 763.5}, notchArea{Top: 38, Width: 202}},
		{"no housing", housingScreen{Width: 1920}, notchArea{}},
		{"safe area without the areas beside a housing", housingScreen{SafeTop: 24, Width: 1920}, notchArea{}},
		{"areas without a safe area", housingScreen{Width: 1512, Left: 656, Right: 656}, notchArea{}},
		{"areas that leave no room", housingScreen{SafeTop: 32, Width: 1000, Left: 600, Right: 600}, notchArea{}},
		{"not a number", housingScreen{SafeTop: math.NaN(), Width: 1512, Left: 656, Right: 656}, notchArea{}},
	} {
		if got := notchAreaOf(c.s); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestWithHousing(t *testing.T) {
	base := notchOptions()
	o := withHousing(base, notchAreaOf(housingScreen{SafeTop: 32, Width: 1512, Left: 656, Right: 656}))
	if o.Height != notchWindowH+32 || o.Width != notchWindowWidth {
		t.Fatalf("size %dx%d", o.Width, o.Height)
	}
	if o.WindowOptions.URL != "/notch/?top=32&housing=200" {
		t.Fatalf("url %q", o.WindowOptions.URL)
	}
	if o := withHousing(base, notchAreaOf(housingScreen{Width: 1920})); o.Height != notchWindowH || o.WindowOptions.URL != "/notch/" {
		t.Fatalf("without a housing: %d %q", o.Height, o.WindowOptions.URL)
	}
}
