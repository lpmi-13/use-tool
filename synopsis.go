package main

import (
	"fmt"
	"strconv"
	"strings"
)

type SynopsisIssue struct {
	Section  string
	Summary  string
	Evidence string
}

func printSynopsis(inv *Investigation, si SystemInfo, snap Snapshot) {
	fmt.Println("--- USE synopsis ---")
	issues := synopsisIssues(inv, si, snap)
	if len(issues) == 0 {
		fmt.Printf("No high-level USE issues identified from captured %s observations.\n", inv.Name)
		if len(snap.NotCaptured) > 0 {
			fmt.Println("Uncaptured checks are omitted from this synopsis.")
		}
		fmt.Println()
		return
	}
	for _, issue := range issues {
		fmt.Printf("%s: %s. Evidence: %s.\n", issue.Section, issue.Summary, issue.Evidence)
	}
	if len(snap.NotCaptured) > 0 {
		fmt.Println("Uncaptured checks are omitted from this synopsis.")
	}
	fmt.Println()
}

func synopsisIssues(inv *Investigation, si SystemInfo, snap Snapshot) []SynopsisIssue {
	switch inv.Name {
	case "cpu":
		return cpuSynopsis(si, snap)
	case "memory":
		return memorySynopsis(snap)
	case "disk":
		return diskSynopsis(snap)
	case "network":
		return networkSynopsis(snap)
	default:
		return nil
	}
}

func cpuSynopsis(si SystemInfo, snap Snapshot) []SynopsisIssue {
	var issues []SynopsisIssue
	if v, ok := snap.Values["mpstat_idle_mean"]; ok && v.Number <= 20 {
		issues = append(issues, SynopsisIssue{
			Section:  "Utilization",
			Summary:  "high CPU utilization",
			Evidence: fmt.Sprintf("mean mpstat %%idle was %s", formatNumber(v.Number, "%")),
		})
	}
	if v, ok := snap.Values["mpstat_idle_range"]; ok {
		minIdle, maxIdle, hotSamples, ok := idleRangeSignal(v)
		if ok {
			issues = append(issues, SynopsisIssue{
				Section: "Utilization",
				Summary: "localized CPU utilization on one or more logical CPUs",
				Evidence: fmt.Sprintf("per-CPU %%idle ranged from %s to %s; %d/%d samples were at or below 5%% idle",
					formatNumber(minIdle, "%"), formatNumber(maxIdle, "%"), hotSamples, len(v.Samples)),
			})
		}
	}
	if v, ok := snap.Values["vmstat_r"]; ok && v.Max() > float64(si.NumCPU) {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "CPU run queue exceeded available logical CPUs",
			Evidence: fmt.Sprintf("vmstat r max was %s with NumCPU=%d", formatNumber(v.Max(), ""), si.NumCPU),
		})
	}
	if v, ok := snap.Values["vmstat_wa"]; ok && v.Max() >= 20 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "CPU time was blocked behind I/O wait",
			Evidence: fmt.Sprintf("vmstat wa max was %s", formatNumber(v.Max(), "%")),
		})
	}
	if v, ok := snap.Values["vmstat_st"]; ok && v.Max() >= 5 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "hypervisor steal time was visible",
			Evidence: fmt.Sprintf("vmstat st max was %s", formatNumber(v.Max(), "%")),
		})
	}
	if v, ok := snap.Values["dmesg_cpu_keywords"]; ok && textCountPositive(v.Text) {
		issues = append(issues, SynopsisIssue{
			Section:  "Errors",
			Summary:  "kernel CPU, thermal, or machine-check messages were observed",
			Evidence: v.Text,
		})
	}
	return issues
}

func idleRangeSignal(v Value) (float64, float64, int, bool) {
	if len(v.Samples) == 0 {
		return 0, 0, 0, false
	}
	minIdle := v.Min()
	maxIdle := v.Max()
	hotSamples := 0
	for _, sample := range v.Samples {
		if sample <= 5 {
			hotSamples++
		}
	}
	if hotSamples == 0 || maxIdle-minIdle < 50 {
		return minIdle, maxIdle, hotSamples, false
	}
	return minIdle, maxIdle, hotSamples, true
}

func memorySynopsis(snap Snapshot) []SynopsisIssue {
	var issues []SynopsisIssue
	if v, ok := snap.Values["mem_used_pct"]; ok && v.Number >= 90 {
		issues = append(issues, SynopsisIssue{
			Section:  "Utilization",
			Summary:  "high memory utilization",
			Evidence: fmt.Sprintf("memory used was %s using MemAvailable as reclaimable headroom", formatNumber(v.Number, "%")),
		})
	}
	if v, ok := snap.Values["swap_used_pct"]; ok && v.Text == "" && v.Number >= 50 {
		issues = append(issues, SynopsisIssue{
			Section:  "Utilization",
			Summary:  "substantial swap allocation",
			Evidence: fmt.Sprintf("swap used was %s; use vmstat si/so and PSI to distinguish old swapped pages from active pressure", formatNumber(v.Number, "%")),
		})
	}
	siMax, siOK := sampleMax(snap, "vmstat_si")
	soMax, soOK := sampleMax(snap, "vmstat_so")
	if (siOK && siMax > 0) || (soOK && soMax > 0) {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "paging activity was observed",
			Evidence: fmt.Sprintf("vmstat si max %s, so max %s", formatOptionalNumber(siMax, siOK, ""), formatOptionalNumber(soMax, soOK, "")),
		})
	}
	if v, ok := snap.Values["psi_mem_full_avg10"]; ok && v.Number > 0 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "full memory pressure was observed",
			Evidence: fmt.Sprintf("PSI memory full avg10 was %s", formatNumber(v.Number, "%")),
		})
	} else if v, ok := snap.Values["psi_mem_some_avg10"]; ok && v.Number > 1 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "some memory pressure was observed",
			Evidence: fmt.Sprintf("PSI memory some avg10 was %s", formatNumber(v.Number, "%")),
		})
	}
	if v, ok := snap.Values["dmesg_oom_count"]; ok && textCountPositive(v.Text) {
		issues = append(issues, SynopsisIssue{
			Section:  "Errors",
			Summary:  "OOM-related kernel messages were observed",
			Evidence: v.Text,
		})
	}
	return issues
}

func diskSynopsis(snap Snapshot) []SynopsisIssue {
	var issues []SynopsisIssue
	if v, ok := snap.Values["iostat_max_util_pct"]; ok && v.Number >= 80 {
		issues = append(issues, SynopsisIssue{
			Section:  "Utilization",
			Summary:  "a disk device was busy for much of the sample window",
			Evidence: fmt.Sprintf("iostat max %%util was %s", formatNumber(v.Number, "%")),
		})
	}
	if v, ok := snap.Values["iostat_max_aqu_sz"]; ok && v.Number >= 2 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "disk requests were queueing",
			Evidence: fmt.Sprintf("iostat aqu-sz max was %s", formatNumber(v.Number, "")),
		})
	}
	if v, ok := snap.Values["iostat_max_await_ms"]; ok && v.Number >= 20 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "disk request latency was elevated",
			Evidence: fmt.Sprintf("iostat await max was %s", formatNumber(v.Number, " ms")),
		})
	}
	if v, ok := snap.Values["psi_io_full_avg10"]; ok && v.Number > 0 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "full I/O pressure was observed",
			Evidence: fmt.Sprintf("PSI io full avg10 was %s", formatNumber(v.Number, "%")),
		})
	} else if v, ok := snap.Values["psi_io_some_avg10"]; ok && v.Number > 1 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "some I/O pressure was observed",
			Evidence: fmt.Sprintf("PSI io some avg10 was %s", formatNumber(v.Number, "%")),
		})
	}
	if v, ok := snap.Values["dmesg_io_errors"]; ok && textCountPositive(v.Text) {
		issues = append(issues, SynopsisIssue{
			Section:  "Errors",
			Summary:  "kernel I/O error messages were observed",
			Evidence: v.Text,
		})
	}
	return issues
}

func networkSynopsis(snap Snapshot) []SynopsisIssue {
	var issues []SynopsisIssue
	if v, ok := snap.Values["net_rx_drops_per_sec_max"]; ok && v.Number > 0 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "receive drops were observed",
			Evidence: fmt.Sprintf("sar -n EDEV rxdrop/s max was %s", formatNumber(v.Number, "")),
		})
	}
	if v, ok := snap.Values["tcp_retransmit_ratio_pct"]; ok && v.Number >= 1 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "TCP retransmits were elevated",
			Evidence: fmt.Sprintf("TCP retransmit ratio was %s", formatNumber(v.Number, "%")),
		})
	}
	if v, ok := snap.Values["tcp_listen_overflows"]; ok && v.Number > 0 {
		issues = append(issues, SynopsisIssue{
			Section:  "Saturation",
			Summary:  "TCP listen queues overflowed",
			Evidence: fmt.Sprintf("ListenOverflows was %s", formatNumber(v.Number, "")),
		})
	}
	if v, ok := snap.Values["net_iface_errors_total"]; ok && v.Number > 0 {
		issues = append(issues, SynopsisIssue{
			Section:  "Errors",
			Summary:  "interface RX/TX errors were observed",
			Evidence: fmt.Sprintf("interface error total was %s", formatNumber(v.Number, "")),
		})
	}
	if v, ok := snap.Values["dmesg_net_keywords"]; ok && textCountPositive(v.Text) {
		issues = append(issues, SynopsisIssue{
			Section:  "Errors",
			Summary:  "kernel link or NIC messages were observed",
			Evidence: v.Text,
		})
	}
	return issues
}

func sampleMax(snap Snapshot, name string) (float64, bool) {
	v, ok := snap.Values[name]
	if !ok || len(v.Samples) == 0 {
		return 0, false
	}
	return v.Max(), true
}

func formatOptionalNumber(n float64, ok bool, unit string) string {
	if !ok {
		return "not captured"
	}
	return formatNumber(n, unit)
}

func textCountPositive(text string) bool {
	matched, _, ok := leadingTextCount(text)
	return ok && matched > 0
}

func leadingTextCount(text string) (int, int, bool) {
	first, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	left, right, ok := strings.Cut(first, "/")
	if !ok {
		return 0, 0, false
	}
	matched, err := strconv.Atoi(left)
	if err != nil {
		return 0, 0, false
	}
	total, err := strconv.Atoi(right)
	if err != nil {
		return 0, 0, false
	}
	return matched, total, true
}
