//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// primaryHousing reads the first screen: the Wails library hangs the notch window from it.
static void primaryHousing(double *top, double *width, double *left, double *right) {
	*top = *width = *left = *right = 0;
	if (@available(macOS 12.0, *)) {
		NSScreen *s = [[NSScreen screens] firstObject];
		if (s != nil) {
			*top = s.safeAreaInsets.top;
			*width = NSWidth(s.frame);
			*left = NSWidth(s.auxiliaryTopLeftArea);
			*right = NSWidth(s.auxiliaryTopRightArea);
		}
	}
}
*/
import "C"

// primaryScreen reads the camera housing of the screen that gets the notch window. AppKit wants the main
// thread: call it from main, where Wails keeps the main goroutine.
func primaryScreen() housingScreen {
	var top, width, left, right C.double
	C.primaryHousing(&top, &width, &left, &right)
	return housingScreen{SafeTop: float64(top), Width: float64(width), Left: float64(left), Right: float64(right)}
}
