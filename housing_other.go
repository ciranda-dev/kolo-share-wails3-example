//go:build !darwin

package main

// primaryScreen reports no camera housing: only macOS has a notch window.
func primaryScreen() housingScreen { return housingScreen{} }
