package main

// syncDir does nothing: Windows cannot sync a folder, and its rename is already durable once the file is synced.
func syncDir(string) error { return nil }
