package main

import (
	"math"
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
