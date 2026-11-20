package main

import (
	"log"
	"strings"
	"unicode"
)

const (
	// maxReportRunes bounds a window's error in the log: 8192 characters, as in the other Kolo example apps.
	maxReportRunes = 8192
	// maxReportBytes bounds what is looked at before the cleaning, so a huge message costs no more than a small one.
	maxReportBytes = 64 << 10
)

// ReportError writes an error of a window, the main one or the notch, to the log, so that a person who runs the app
// from a terminal sees why a window failed. The window sends the error's message and its stack only; the text is
// cleaned of control characters and cut to maxReportRunes characters.
func (s *ShareService) ReportError(message, stack string) {
	log.Print("frontend error: ", cleanReport(head(message)+"\n"+head(stack)))
}

// head is the first maxReportBytes bytes of s.
func head(s string) string { return s[:min(len(s), maxReportBytes)] }

// cleanReport drops invalid UTF-8, control and format characters, which could rewrite the terminal, keeps the line
// breaks and cuts the text to maxReportRunes characters.
func cleanReport(s string) string {
	s = strings.Map(func(r rune) rune {
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxReportRunes {
		s = string(r[:maxReportRunes])
	}
	return s
}
