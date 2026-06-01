package main

import (
	"regexp"
	"strconv"
	"strings"
)

var psiAvg10LineRe = regexp.MustCompile(`^(some|full)\s+avg10=([0-9.]+)`)

func extractPSIAvg10(path, which string) func(SystemInfo, []CapturedCommand) (Value, bool) {
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		for i := len(caps) - 1; i >= 0; i-- {
			c := caps[i]
			if path != "" && !strings.Contains(c.Cmd, path) {
				continue
			}
			for _, line := range strings.Split(c.Output, "\n") {
				m := psiAvg10LineRe.FindStringSubmatch(line)
				if m == nil || m[1] != which {
					continue
				}
				n, err := strconv.ParseFloat(m[2], 64)
				if err != nil {
					continue
				}
				return Value{Number: n, Unit: "%"}, true
			}
		}
		return Value{}, false
	}
}
