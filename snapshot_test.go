package main

import (
	"bytes"
	"math"
	"os"
	"strings"
	"testing"
)

func TestValueStatsEmpty(t *testing.T) {
	v := Value{}
	if !math.IsNaN(v.Min()) {
		t.Errorf("Min on empty: expected NaN, got %v", v.Min())
	}
	if !math.IsNaN(v.Max()) {
		t.Errorf("Max on empty: expected NaN, got %v", v.Max())
	}
	if !math.IsNaN(v.Mean()) {
		t.Errorf("Mean on empty: expected NaN, got %v", v.Mean())
	}
}

func TestValueStatsSamples(t *testing.T) {
	v := Value{Samples: []float64{2, 5, 1, 8, 4}}
	if v.Min() != 1 {
		t.Errorf("Min: got %v, want 1", v.Min())
	}
	if v.Max() != 8 {
		t.Errorf("Max: got %v, want 8", v.Max())
	}
	if v.Mean() != 4 {
		t.Errorf("Mean: got %v, want 4", v.Mean())
	}
}

func TestSnapshotGroupsBySection(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}}
	s := &Session{Investigation: memoryInvestigation, System: SystemInfo{}, Captured: caps}
	snap := s.Snapshot()

	sectionsSeen := map[string]bool{}
	for _, sec := range snap.Sections {
		sectionsSeen[sec.Title] = true
	}
	if !sectionsSeen["Utilization"] {
		t.Error("expected Utilization section in snapshot")
	}
	// Saturation and Errors should be in NotCaptured because we only fed meminfo
	notCapturedNames := map[string]bool{}
	for _, o := range snap.NotCaptured {
		notCapturedNames[o.Name] = true
	}
	if !notCapturedNames["vmstat_si"] {
		t.Error("expected vmstat_si in NotCaptured when no vmstat output present")
	}
	if !notCapturedNames["dmesg_oom_count"] {
		t.Error("expected dmesg_oom_count in NotCaptured when no dmesg output present")
	}
}

func TestSnapshotSourcesDeduped(t *testing.T) {
	caps := []CapturedCommand{
		{Cmd: "cat /proc/meminfo", Output: sampleMeminfo},
		{Cmd: "cat /proc/meminfo", Output: sampleMeminfo},
	}
	s := &Session{Investigation: memoryInvestigation, System: SystemInfo{}, Captured: caps}
	snap := s.Snapshot()
	count := 0
	for _, src := range snap.Sources {
		if src == "cat /proc/meminfo" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected dedup of identical commands, got %d entries", count)
	}
}

func TestSnapshotPrintAlignsValueColumnToLongestTitle(t *testing.T) {
	snap := Snapshot{
		Sources: []string{"iostat -xz 1 3"},
		Sections: []SnapshotSection{{
			Title: "Utilization",
			Items: []SnapshotItem{
				{Title: "Max %util across devices", Value: Value{Number: 2.31, Unit: "%"}},
				{Title: "Peak per-device IOPS (r/s + w/s)", Value: Value{Number: 19.1, Unit: " IOPS"}},
			},
		}},
	}

	out := captureStdout(func() {
		snap.Print()
	})

	lines := strings.Split(out, "\n")
	positions := []int{}
	for _, line := range lines {
		switch {
		case strings.Contains(line, "Max %util across devices"):
			positions = append(positions, strings.Index(line, "2.31%"))
		case strings.Contains(line, "Peak per-device IOPS"):
			positions = append(positions, strings.Index(line, "19.1 IOPS"))
		}
	}
	if len(positions) != 2 {
		t.Fatalf("expected 2 snapshot item lines, got positions %v in output:\n%s", positions, out)
	}
	if positions[0] != positions[1] {
		t.Fatalf("value columns not aligned: positions %v in output:\n%s", positions, out)
	}
}

func TestSnapshotItemTitleWidthHasMinimumAndExpands(t *testing.T) {
	short := Snapshot{Sections: []SnapshotSection{{Items: []SnapshotItem{{Title: "short"}}}}}
	if got := short.itemTitleWidth(); got != 30 {
		t.Fatalf("short title width = %d, want 30", got)
	}
	longTitle := "Peak per-device IOPS (r/s + w/s)"
	long := Snapshot{Sections: []SnapshotSection{{Items: []SnapshotItem{{Title: longTitle}}}}}
	if got := long.itemTitleWidth(); got != len(longTitle) {
		t.Fatalf("long title width = %d, want %d", got, len(longTitle))
	}
}

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	r.Close()
	return buf.String()
}
