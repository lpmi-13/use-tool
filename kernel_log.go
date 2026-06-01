package main

import (
	"fmt"
	"strings"
)

func isKernelLogCommand(cmd string) bool {
	switch baseCmd(cmd) {
	case "dmesg", "journalctl":
		return true
	default:
		return false
	}
}

func extractKernelLogKeywords(caps []CapturedCommand, keywords []string, label string) (Value, bool) {
	seen := false
	matched := 0
	totalLines := 0
	for _, c := range caps {
		if !isKernelLogCommand(c.Cmd) {
			continue
		}
		seen = true
		for _, line := range strings.Split(c.Output, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			totalLines++
			low := strings.ToLower(line)
			for _, kw := range keywords {
				if strings.Contains(low, kw) {
					matched++
					break
				}
			}
		}
	}
	if !seen {
		return Value{}, false
	}
	return Value{
		Number: float64(matched),
		Text:   fmt.Sprintf("%d/%d lines mention %s", matched, totalLines, label),
	}, true
}
