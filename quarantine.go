package main

import (
	"fmt"
	"time"
)

// zoneIdentifier is the Windows "Mark of the Web" of a file from the internet zone (3), which SmartScreen and
// Office Protected View check.
const zoneIdentifier = "[ZoneTransfer]\r\nZoneId=3\r\n"

// quarantineValue is the macOS com.apple.quarantine value of a download at t: flags 0081 (downloaded, not yet
// checked by Gatekeeper), the time in hex seconds and the agent's name.
func quarantineValue(t time.Time) string {
	return fmt.Sprintf("0081;%x;Kolo Share;", t.Unix())
}
