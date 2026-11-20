package main

import (
	"net"
	"strings"
	"time"
)

// trayUsable reports whether goos can show a tray icon. On Linux the tray is a D-Bus service, and Wails crashes
// when it changes the icon in a session without a session bus, so the app runs without a tray there.
func trayUsable(goos string, getenv func(string) string, dial func(path string) error) bool {
	if goos != "linux" {
		return true
	}
	for _, path := range sessionBusSockets(getenv) {
		if dial(path) == nil {
			return true
		}
	}
	return false
}

// sessionBusSockets lists where the D-Bus session bus may listen: the unix: addresses of
// DBUS_SESSION_BUS_ADDRESS (path=, or abstract=, written with a leading "@"), else the "bus" socket of
// XDG_RUNTIME_DIR.
func sessionBusSockets(getenv func(string) string) []string {
	addrs := getenv("DBUS_SESSION_BUS_ADDRESS")
	if addrs == "" {
		if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
			return []string{dir + "/bus"}
		}
		return nil
	}
	var out []string
	for _, addr := range strings.Split(addrs, ";") {
		kind, params, ok := strings.Cut(addr, ":")
		if !ok || kind != "unix" {
			continue
		}
		for _, kv := range strings.Split(params, ",") {
			switch k, v, _ := strings.Cut(kv, "="); k {
			case "path":
				out = append(out, v)
			case "abstract":
				out = append(out, "@"+v)
			}
		}
	}
	return out
}

func dialUnix(path string) error {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		c.Close()
	}
	return err
}
