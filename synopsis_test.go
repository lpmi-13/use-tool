package main

import (
	"strings"
	"testing"
)

func TestCPUSynopsisIdentifiesUSESignals(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"mpstat_idle_mean":    {Number: 4.5},
		"vmstat_r":            {Samples: []float64{2, 10, 12}},
		"dmesg_cpu_keywords":  {Text: "1/8 lines mention CPU/thermal/MCE keywords"},
		"loadavg_1min":        {Number: 12},
		"mpstat_idle_range":   {Text: "4.0% - 5.0%"},
		"dmesg_oom_count":     {Text: "0/0 lines mention OOM"},
		"psi_mem_full_avg10":  {Number: 0},
		"iostat_max_util_pct": {Number: 0},
	}}
	issues := cpuSynopsis(SystemInfo{NumCPU: 8}, snap)

	if !hasSynopsis(issues, "Utilization", "high CPU utilization") {
		t.Fatalf("expected CPU utilization issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "CPU run queue exceeded") {
		t.Fatalf("expected CPU saturation issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Errors", "kernel CPU") {
		t.Fatalf("expected CPU error issue, got %#v", issues)
	}
}

func TestCPUSynopsisIdentifiesLocalizedCPUUtilization(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"mpstat_idle_mean":  {Number: 61},
		"mpstat_idle_range": {Samples: []float64{94, 96, 97, 0, 0, 0, 97, 95}},
		"vmstat_r":          {Samples: []float64{4, 4, 3}},
		"vmstat_wa":         {Samples: []float64{1, 0, 0}},
		"vmstat_st":         {Samples: []float64{0, 0, 0}},
	}}
	issues := cpuSynopsis(SystemInfo{NumCPU: 8}, snap)

	if !hasSynopsis(issues, "Utilization", "localized CPU utilization") {
		t.Fatalf("expected localized CPU utilization issue, got %#v", issues)
	}
	if hasSynopsis(issues, "Saturation", "CPU run queue exceeded") {
		t.Fatalf("did not expect run-queue saturation issue, got %#v", issues)
	}
}

func TestCPUSynopsisDoesNotFlagMinorPerCPUVariation(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"mpstat_idle_mean":  {Number: 72},
		"mpstat_idle_range": {Samples: []float64{64, 70, 80, 85}},
	}}
	if issues := cpuSynopsis(SystemInfo{NumCPU: 8}, snap); len(issues) != 0 {
		t.Fatalf("expected no CPU issues for minor per-CPU variation, got %#v", issues)
	}
}

func TestMemorySynopsisIdentifiesSaturationAndErrors(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"mem_used_pct":           {Number: 55},
		"swap_used_pct":          {Number: 0},
		"vmstat_si":              {Samples: []float64{0, 0}},
		"vmstat_so":              {Samples: []float64{0, 8}},
		"sar_b_major_faults":     {Samples: []float64{0, 12}},
		"sar_b_reclaim_activity": {Samples: []float64{0, 5}},
		"psi_mem_full_avg10":     {Number: 0.8},
		"dmesg_oom_count":        {Text: "2/5 lines mention OOM"},
		"dmesg_cpu_keywords":     {Text: "0/0 lines mention CPU/thermal/MCE keywords"},
		"net_iface_errors_total": {Number: 0},
	}}
	issues := memorySynopsis(snap)

	if hasSynopsis(issues, "Utilization", "") {
		t.Fatalf("did not expect memory utilization issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "paging activity") {
		t.Fatalf("expected paging saturation issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "major page faults") {
		t.Fatalf("expected sar -B major fault issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "memory reclaim activity") {
		t.Fatalf("expected sar -B reclaim issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "full memory pressure") {
		t.Fatalf("expected PSI saturation issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Errors", "OOM") {
		t.Fatalf("expected OOM error issue, got %#v", issues)
	}
}

func TestMemorySynopsisIdentifiesHighSwapAllocation(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"mem_used_pct":       {Number: 52},
		"swap_used_pct":      {Number: 75},
		"vmstat_si":          {Samples: []float64{0, 0}},
		"vmstat_so":          {Samples: []float64{0, 0}},
		"psi_mem_some_avg10": {Number: 0},
		"psi_mem_full_avg10": {Number: 0},
		"dmesg_oom_count":    {Text: "0/3 lines mention OOM"},
	}}
	issues := memorySynopsis(snap)

	if !hasSynopsis(issues, "Utilization", "large swap use") {
		t.Fatalf("expected swap utilization issue, got %#v", issues)
	}
	if hasSynopsis(issues, "Saturation", "") {
		t.Fatalf("did not expect saturation from swap allocation alone, got %#v", issues)
	}
}

func TestMemorySynopsisNoSwapConfiguredIsQuiet(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"mem_used_pct":    {Number: 52},
		"swap_used_pct":   {Text: "no swap configured"},
		"dmesg_oom_count": {Text: "0/3 lines mention OOM"},
	}}
	issues := memorySynopsis(snap)

	if hasSynopsis(issues, "Utilization", "swap") {
		t.Fatalf("did not expect swap utilization issue when no swap is configured, got %#v", issues)
	}
}

func TestMemorySynopsisQuietWhenNoSignalsCrossThresholds(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"mem_used_pct":       {Number: 42},
		"swap_used_pct":      {Number: 0},
		"vmstat_si":          {Samples: []float64{0, 0}},
		"vmstat_so":          {Samples: []float64{0, 0}},
		"sar_b_major_faults": {Samples: []float64{0, 0}},
		// Non-zero fault/s and pgpgout/s are context-only and should not
		// produce a synopsis issue without major faults or reclaim activity.
		"sar_b_paging_context":   {Text: "fault/s max 3, pgpgout/s max 88"},
		"sar_b_reclaim_activity": {Samples: []float64{0, 0}},
		"psi_mem_some_avg10":     {Number: 0},
		"psi_mem_full_avg10":     {Number: 0},
		"dmesg_oom_count":        {Text: "0/3 lines mention OOM"},
	}}
	if issues := memorySynopsis(snap); len(issues) != 0 {
		t.Fatalf("expected no memory issues, got %#v", issues)
	}
}

func TestDiskSynopsisIdentifiesUSESignals(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"iostat_max_util_pct": {Number: 95},
		"iostat_max_aqu_sz":   {Number: 4.2},
		"iostat_max_await_ms": {Number: 32},
		"dmesg_io_errors":     {Text: "1/4 lines mention I/O errors or read-only remounts"},
	}}
	issues := diskSynopsis(snap)

	if !hasSynopsis(issues, "Utilization", "disk device was busy") {
		t.Fatalf("expected disk utilization issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "requests were queueing") {
		t.Fatalf("expected disk queue saturation issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Errors", "I/O error") {
		t.Fatalf("expected disk error issue, got %#v", issues)
	}
}

func TestNetworkSynopsisIdentifiesSaturationAndErrors(t *testing.T) {
	snap := Snapshot{Values: map[string]Value{
		"net_peak_throughput_mbps": {Number: 316},
		"net_rx_drops_per_sec_max": {Number: 3},
		"tcp_retransmit_ratio_pct": {Number: 2.5},
		"tcp_listen_overflows":     {Number: 7},
		"net_iface_errors_total":   {Number: 9},
		"dmesg_net_keywords":       {Text: "1/5 lines mention link/NIC events"},
	}}
	issues := networkSynopsis(snap)

	if !hasSynopsis(issues, "Utilization", "network throughput") {
		t.Fatalf("expected network throughput utilization issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "receive drops") {
		t.Fatalf("expected network drop saturation issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Saturation", "TCP retransmits") {
		t.Fatalf("expected retransmit issue, got %#v", issues)
	}
	if !hasSynopsis(issues, "Errors", "interface RX/TX errors") {
		t.Fatalf("expected interface error issue, got %#v", issues)
	}
}

func TestLeadingTextCount(t *testing.T) {
	matched, total, ok := leadingTextCount("2/5 lines mention OOM")
	if !ok || matched != 2 || total != 5 {
		t.Fatalf("leadingTextCount() = %d, %d, %v; want 2, 5, true", matched, total, ok)
	}
	if textCountPositive("0/5 lines mention OOM") {
		t.Fatal("zero matched count should not be positive")
	}
	if textCountPositive("no count here") {
		t.Fatal("unparseable count should not be positive")
	}
}

func hasSynopsis(issues []SynopsisIssue, section, summaryPart string) bool {
	for _, issue := range issues {
		if issue.Section != section {
			continue
		}
		if summaryPart == "" || strings.Contains(issue.Summary, summaryPart) {
			return true
		}
	}
	return false
}
