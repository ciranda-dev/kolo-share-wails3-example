package main

import (
	"math"
	"strconv"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// housingScreen is what a screen reports about its camera housing, in points.
type housingScreen struct {
	SafeTop float64 // safeAreaInsets.top: the height of the housing
	Width   float64 // the width of the screen
	Left    float64 // the width of the area left of the housing
	Right   float64 // the width of the area right of it
}

// notchArea is the camera housing over the notch page, in whole points: zero on a screen without one.
type notchArea struct{ Top, Width int }

// notchAreaOf rounds up, so no fraction of the housing covers the page.
func notchAreaOf(s housingScreen) notchArea {
	w := s.Width - s.Left - s.Right
	if !(s.SafeTop > 0 && s.Left > 0 && s.Right > 0 && w > 0) {
		return notchArea{}
	}
	return notchArea{Top: int(math.Ceil(s.SafeTop)), Width: int(math.Ceil(w))}
}

// withHousing makes the notch window taller by the housing, so the page keeps its height below it, and puts the
// housing in the page's address: the page has it before it first paints.
func withHousing(o application.NotchWindowOptions, a notchArea) application.NotchWindowOptions {
	if a.Top <= 0 {
		return o
	}
	o.Height += a.Top
	o.WindowOptions.URL += "?top=" + strconv.Itoa(a.Top) + "&housing=" + strconv.Itoa(a.Width)
	return o
}
