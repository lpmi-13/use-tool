package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var networkInvestigation = &Investigation{
	Name:  "network",
	Title: "Network — Utilization, Saturation, Errors",
	Description: "Investigate network using Brendan Gregg's USE method.\n" +
		"Run commands at the prompt; the harness captures their output\n" +
		"and asks targeted questions about what you observed.",
	StepsFn:        networkSteps,
	Extractors:     networkExtractors,
	Observations:   networkObservations,
	SynthesisRules: networkSynthesisRules,
	Commands:       networkCommands,
}

// ----- Guide steps -----

func networkSteps(si SystemInfo) []GuideStep {
	return []GuideStep{
		{
			Name:        "interfaces",
			Intro:       "Step 1: orient yourself to the interfaces and their cumulative counters.",
			Suggested:   "ip -s link",
			QuestionsFn: ipLinkQuestions,
			AcceptAny:   true,
			Teaching: "Each interface lists RX and TX byte/packet/error/dropped counters.\n" +
				"These are cumulative since boot — useful for 'has anything ever been\n" +
				"wrong here' but not for 'is it happening now'. Use sar -n EDEV for rates.",
		},
		{
			Name:        "throughput",
			Intro:       "Step 2: measure per-interface throughput as a rate (utilization).\nsar samples once per second; compare against ethtool-reported link speed.",
			Suggested:   "sar -n DEV 1 3",
			QuestionsFn: sarDevQuestions,
			Teaching: "rxkB/s and txkB/s give the per-second receive/transmit rate.\n" +
				"To convert to a percentage of link capacity, you need ethtool to learn\n" +
				"the link speed. On VMs and containers, virtual interfaces often report\n" +
				"'Speed: Unknown!' — graceful degradation matters here.",
		},
		{
			Name:        "drops",
			Intro:       "Step 3: look for saturation at the interface level — drops and overruns.",
			Suggested:   "sar -n EDEV 1 3",
			QuestionsFn: sarEdevQuestions,
			Teaching: "rxdrop/s > 0 means the kernel discarded incoming packets, usually\n" +
				"because the NIC ring buffer or kernel queue was full. txdrop/s on the\n" +
				"send side means the qdisc dropped packets. Either is real saturation\n" +
				"that the bandwidth headline will not show you.",
		},
		{
			Name:        "tcp",
			Intro:       "Step 4: protocol-level signals — TCP retransmits and listen overflows.",
			Suggested:   "cat /proc/net/snmp /proc/net/netstat",
			QuestionsFn: catNetQuestions,
			Teaching: "RetransSegs / OutSegs gives the retransmit ratio. Healthy is < 0.5%.\n" +
				"ListenOverflows in TcpExt: counts SYNs the kernel dropped because the\n" +
				"application's listen queue was full — that's an application-side\n" +
				"bottleneck, not network loss.",
		},
		{
			Name:        "sockets",
			Intro:       "Step 5: socket summary — what's currently established and what's listening.",
			Suggested:   "ss -s",
			QuestionsFn: ssSummaryQuestions,
			AcceptAny:   true,
			Teaching: "The TCP estab count tells you connection load right now. For listen\n" +
				"queue depth, `ss -lnt` shows Recv-Q (current) vs Send-Q (max backlog)\n" +
				"per listening socket — Recv-Q approaching Send-Q is the live signal\n" +
				"that ListenOverflows is about to climb.",
		},
		{
			Name:        "errors",
			Intro:       "Step 6: kernel network errors — link state changes and NIC issues.",
			Suggested:   "dmesg -T 2>/dev/null | grep -iE 'link is down|link up|carrier|nic|ethernet' | tail",
			QuestionsFn: networkDmesgQuestions,
			AcceptAny:   true,
			Teaching: "Repeated link-up/link-down sequences mean a flapping cable, transceiver,\n" +
				"or peer port. NIC driver errors point at hardware or firmware faults.",
		},
	}
}

// ----- Comprehension extractors (per-command) -----

var networkExtractors = []Extractor{
	{BaseCmd: "ip", QuestionsFn: ipLinkQuestions},
	{BaseCmd: "sar", QuestionsFn: sarNetQuestions},
	{BaseCmd: "ss", QuestionsFn: ssSummaryQuestions},
	{BaseCmd: "cat", QuestionsFn: catNetQuestions},
	{BaseCmd: "netstat", QuestionsFn: netstatQuestions},
	{BaseCmd: "dmesg", QuestionsFn: networkDmesgQuestions},
	{BaseCmd: "journalctl", QuestionsFn: networkDmesgQuestions},
}

// sarNetQuestions dispatches by output shape (sar has many sub-modes).
func sarNetQuestions(si SystemInfo, c CapturedCommand) []Question {
	var qs []Question
	qs = append(qs, sarDevQuestions(si, c)...)
	qs = append(qs, sarEdevQuestions(si, c)...)
	return qs
}

// catNetQuestions dispatches based on path looked at.
func catNetQuestions(si SystemInfo, c CapturedCommand) []Question {
	var qs []Question
	if strings.Contains(c.Cmd, "/proc/net/snmp") {
		qs = append(qs, snmpQuestions(si, c)...)
	}
	if strings.Contains(c.Cmd, "/proc/net/netstat") {
		qs = append(qs, netstatExtQuestions(si, c)...)
	}
	if strings.Contains(c.Cmd, "/proc/net/dev") {
		qs = append(qs, procNetDevQuestions(si, c)...)
	}
	return qs
}

func ipLinkQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "RX:") || !strings.Contains(c.Output, "TX:") {
		return nil
	}
	return []Question{
		{
			Stem:    "In `ip -s link`, what's the difference between the RX `dropped` and `overrun` counters?",
			Correct: "`dropped` includes any packet the kernel discarded (full queue, filter, no protocol handler); `overrun` specifically counts packets the NIC's hardware FIFO overflowed before the driver could read them",
			Distractors: []string{
				"They are aliases for the same counter exposed in two formats",
				"`dropped` is software-side; `overrun` is filesystem-side",
				"`dropped` is per-second; `overrun` is cumulative since boot",
			},
		},
		{
			Stem:    "The RX/TX counters in `ip -s link` are cumulative since boot. Why is that important when interpreting them?",
			Correct: "A non-zero count tells you something happened *at some point*, not whether it's happening now; you need a rate (sar -n EDEV) to know if the issue is current",
			Distractors: []string{
				"They overflow every 24 hours, so values older than that are unreliable",
				"They are per-CPU and must be summed manually before comparing",
				"They reset on every link state change, so they only reflect the current uptime of the link",
			},
		},
	}
}

var sarDevHeaderRe = regexp.MustCompile(`(?m)^[\d:]+\s+IFACE.*rxkB/s.*txkB/s`)

func sarDevQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarDevHeaderRe.MatchString(c.Output) {
		return nil
	}
	return []Question{
		{
			Stem:    "In `sar -n DEV` output, what does `rxkB/s` measure?",
			Correct: "Receive throughput in kilobytes per second, averaged over the sample interval",
			Distractors: []string{
				"Receive packets per second, ignoring packet size",
				"Total kilobytes received since the sar process started",
				"Maximum link-capacity headroom currently free, in kilobytes per second",
			},
		},
		{
			Stem:    "If `rxkB/s` is well below the interface's link speed but the application reports network slowness, what's the right next step?",
			Correct: "Check sar -n EDEV for drops and `cat /proc/net/snmp` for retransmits; bandwidth headroom does not rule out loss",
			Distractors: []string{
				"Conclude the network is fine and look at CPU instead",
				"Run sar -n DEV with a longer interval until rxkB/s climbs",
				"Restart the network interface — sustained low utilization indicates a misconfigured driver",
			},
		},
	}
}

var sarEdevHeaderRe = regexp.MustCompile(`(?m)^[\d:]+\s+IFACE.*rxdrop/s`)

func sarEdevQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarEdevHeaderRe.MatchString(c.Output) {
		return nil
	}
	return []Question{{
		Stem:    "Sustained `rxdrop/s` > 0 on a physical interface most commonly indicates:",
		Correct: "The NIC ring buffer or kernel queue is filling faster than the system can drain it (can be tuned with `ethtool -G`)",
		Distractors: []string{
			"The peer is sending malformed frames that the NIC rejects",
			"Disk I/O latency is causing TCP buffers to back up",
			"The application has crashed and its connections are being torn down",
		},
	}}
}

func ssSummaryQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "TCP:") || !strings.Contains(c.Output, "estab") {
		return nil
	}
	return []Question{{
		Stem:    "In `ss -s` output, the `estab` count under TCP refers to:",
		Correct: "The number of TCP connections currently in the ESTABLISHED state",
		Distractors: []string{
			"The total number of TCP connections opened since boot",
			"The number of sockets that have completed a SYN handshake but not yet been accept()ed",
			"The number of TCP listen sockets currently bound to ports",
		},
	}}
}

var snmpTcpHeaderRe = regexp.MustCompile(`(?m)^Tcp:\s+RtoAlgorithm`)

func snmpQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !snmpTcpHeaderRe.MatchString(c.Output) {
		return nil
	}
	return []Question{
		{
			Stem:    "`/proc/net/snmp` reports TCP `OutSegs` and `RetransSegs`. What does the ratio RetransSegs / OutSegs tell you?",
			Correct: "The fraction of TCP segments that had to be retransmitted — a proxy for end-to-end packet loss; healthy is well under 1%",
			Distractors: []string{
				"The fraction of segments that took longer than RTO to acknowledge",
				"The fraction of bandwidth wasted on TCP control packets",
				"The fraction of connections currently in fast retransmit",
			},
		},
		{
			Stem:    "If the retransmit ratio is high but `ip -s link` shows zero RX/TX errors and zero drops, where is loss most likely happening?",
			Correct: "Downstream of this host — an intermediate switch, the peer, or somewhere along the path",
			Distractors: []string{
				"In the local TCP stack — usually a kernel bug",
				"In the application — it's failing to read fast enough",
				"On the local NIC — the error counters are probably broken",
			},
		},
	}
}

var netstatExtTcpExtRe = regexp.MustCompile(`(?m)^TcpExt:\s+\w`)

func netstatExtQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !netstatExtTcpExtRe.MatchString(c.Output) {
		return nil
	}
	return []Question{{
		Stem:    "In /proc/net/netstat, a non-zero `ListenOverflows` means:",
		Correct: "The kernel dropped a SYN because the application's listen queue (Send-Q in `ss -lnt`) was full — the bottleneck is in user space",
		Distractors: []string{
			"The kernel ran out of file descriptors and could not create a new socket",
			"A TCP connection had its receive window overflow",
			"The system has too many sockets in TIME_WAIT and can't open new ones",
		},
	}}
}

func procNetDevQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "Inter-") || !strings.Contains(c.Output, "Receive") {
		return nil
	}
	return []Question{{
		Stem:    "/proc/net/dev gives cumulative per-interface counters. To get a *rate*, what should you do?",
		Correct: "Sample twice with a known time delta and compute the difference, or use a rate-aware tool like sar -n DEV",
		Distractors: []string{
			"Multiply the cumulative value by the system tick rate",
			"Divide by the interface's link speed",
			"Read /proc/net/dev_rate, which exposes the same data already differentiated",
		},
	}}
}

func netstatQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if !strings.Contains(low, "tcp") || !strings.Contains(low, "segments") {
		return nil
	}
	// netstat -s output contains "segments retransmitted"; we re-use the snmp question shape.
	return []Question{{
		Stem:    "In `netstat -s` output, a high count of `segments retransmitted` relative to `segments sent` indicates:",
		Correct: "End-to-end packet loss — somewhere between this host and the peer, segments are being dropped",
		Distractors: []string{
			"The local TCP stack is misconfigured",
			"Excessive retransmit timeout firing due to a clock drift",
			"The application is closing connections without a FIN",
		},
	}}
}

func networkDmesgQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if !strings.Contains(low, "link is") &&
		!strings.Contains(low, "carrier") &&
		!strings.Contains(low, "nic link") {
		return nil
	}
	return []Question{{
		Stem:    "Repeated `Link is Down` followed by `Link is Up` for the same interface in dmesg suggests:",
		Correct: "A flapping cable, transceiver, or peer port — the physical layer is intermittently disconnecting",
		Distractors: []string{
			"The kernel is rotating IP addresses for that interface",
			"DHCP is renewing the lease and briefly losing the link",
			"The NIC driver is being reloaded by udev",
		},
	}}
}

// ----- Observations -----

var networkObservations = []Observation{
	{
		Name:    "net_peak_rx_kbps",
		Title:   "Peak RX rate (across non-lo ifaces)",
		Section: "Utilization",
		Extract: extractSarNetPeak("rxkB/s"),
	},
	{
		Name:    "net_peak_tx_kbps",
		Title:   "Peak TX rate (across non-lo ifaces)",
		Section: "Utilization",
		Extract: extractSarNetPeak("txkB/s"),
	},
	{
		Name:    "tcp_estab_connections",
		Title:   "Established TCP connections",
		Section: "Utilization",
		Extract: extractTCPEstab,
	},
	{
		Name:    "net_rx_drops_per_sec_max",
		Title:   "Max rxdrop/s (sar -n EDEV)",
		Section: "Saturation",
		Extract: extractSarEdevPeak("rxdrop/s"),
		Recall:  netRxDropsRecall,
	},
	{
		Name:    "tcp_retransmit_ratio_pct",
		Title:   "TCP retransmit ratio",
		Section: "Saturation",
		Extract: extractTCPRetransmitRatio,
	},
	{
		Name:    "tcp_listen_overflows",
		Title:   "Listen overflows (cumulative)",
		Section: "Saturation",
		Extract: extractListenOverflows,
	},
	{
		Name:    "net_iface_errors_total",
		Title:   "Cumulative interface errors",
		Section: "Errors",
		Extract: extractInterfaceErrors,
	},
	{
		Name:    "dmesg_net_keywords",
		Title:   "dmesg link/NIC events",
		Section: "Errors",
		Extract: extractDmesgNetKeywords,
	},
}

// extractSarNetPeak returns a function that finds the peak value of the named
// column across all non-loopback interfaces in any captured `sar -n DEV` output.
func extractSarNetPeak(column string) func(SystemInfo, []CapturedCommand) (Value, bool) {
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		max := 0.0
		seen := false
		for _, c := range caps {
			if baseCmd(c.Cmd) != "sar" {
				continue
			}
			rows := parseSarTable(c.Output, "rxkB/s", "txkB/s") // only matches DEV table
			for _, r := range rows {
				if r["IFACE"] == "lo" {
					continue
				}
				v, err := strconv.ParseFloat(r[column], 64)
				if err != nil {
					continue
				}
				seen = true
				if v > max {
					max = v
				}
			}
		}
		if !seen {
			return Value{}, false
		}
		return Value{Number: max, Unit: " kB/s"}, true
	}
}

// extractSarEdevPeak finds the peak value of the named column in any captured
// `sar -n EDEV` output (errors/drops table).
func extractSarEdevPeak(column string) func(SystemInfo, []CapturedCommand) (Value, bool) {
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		max := 0.0
		seen := false
		for _, c := range caps {
			if baseCmd(c.Cmd) != "sar" {
				continue
			}
			rows := parseSarTable(c.Output, "rxdrop/s", "rxerr/s") // only matches EDEV
			for _, r := range rows {
				if r["IFACE"] == "lo" {
					continue
				}
				v, err := strconv.ParseFloat(r[column], 64)
				if err != nil {
					continue
				}
				seen = true
				if v > max {
					max = v
				}
			}
		}
		if !seen {
			return Value{}, false
		}
		return Value{Number: max, Unit: " /s", Note: "across non-loopback interfaces"}, true
	}
}

// parseSarTable parses sar's column-headed output into a slice of column-name->value
// maps. The mustHaveAll list is used as a sanity check that we're parsing the
// expected sub-mode of sar (DEV vs EDEV), since both share the IFACE column.
func parseSarTable(output string, mustHaveAll ...string) []map[string]string {
	lines := strings.Split(output, "\n")
	var rows []map[string]string
	var headers []string

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			headers = nil
			continue
		}
		// A header row begins with a HH:MM:SS timestamp followed by IFACE.
		if len(fields) >= 2 && isTimestamp(fields[0]) && fields[1] == "IFACE" {
			headers = fields // includes the timestamp column at index 0
			for _, must := range mustHaveAll {
				if indexOfStr(headers, must) == -1 {
					headers = nil
					break
				}
			}
			continue
		}
		if headers == nil {
			continue
		}
		// Skip "Average:" rows that sar adds at the end.
		if strings.HasPrefix(fields[0], "Average:") {
			continue
		}
		if len(fields) != len(headers) {
			continue
		}
		row := make(map[string]string, len(headers))
		for i, h := range headers {
			row[h] = fields[i]
		}
		rows = append(rows, row)
	}
	return rows
}

func isTimestamp(s string) bool {
	if len(s) < 5 {
		return false
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

func indexOfStr(headers []string, name string) int {
	for i, h := range headers {
		if h == name {
			return i
		}
	}
	return -1
}

// findSnmpOutput locates the most recent capture whose output contains
// /proc/net/snmp's Tcp: header pair.
func findSnmpOutput(caps []CapturedCommand) (string, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if !strings.Contains(caps[i].Cmd, "/proc/net/snmp") {
			continue
		}
		if snmpTcpHeaderRe.MatchString(caps[i].Output) {
			return caps[i].Output, true
		}
	}
	return "", false
}

// parseSnmpTcp extracts the Tcp: header/data line pair from /proc/net/snmp
// and returns it as a name -> value map.
func parseSnmpTcp(output string) map[string]float64 {
	var headers []string
	out := map[string]float64{}
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "Tcp:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Detect header (non-numeric second field) vs data (numeric) line.
		if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
			headers = fields
			continue
		}
		if headers == nil || len(fields) != len(headers) {
			continue
		}
		for i := 1; i < len(fields); i++ {
			n, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				continue
			}
			out[headers[i]] = n
		}
		break
	}
	return out
}

func extractTCPEstab(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	out, ok := findSnmpOutput(caps)
	if !ok {
		return Value{}, false
	}
	tcp := parseSnmpTcp(out)
	estab, ok := tcp["CurrEstab"]
	if !ok {
		return Value{}, false
	}
	return Value{Number: estab}, true
}

func extractTCPRetransmitRatio(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	out, ok := findSnmpOutput(caps)
	if !ok {
		return Value{}, false
	}
	tcp := parseSnmpTcp(out)
	out_, oOk := tcp["OutSegs"]
	retrans, rOk := tcp["RetransSegs"]
	if !oOk || !rOk || out_ == 0 {
		return Value{}, false
	}
	ratio := retrans / out_ * 100
	return Value{Number: ratio, Unit: "%", Note: fmt.Sprintf("%.0f / %.0f", retrans, out_)}, true
}

// findNetstatExtOutput locates a /proc/net/netstat capture with a TcpExt: line.
func findNetstatExtOutput(caps []CapturedCommand) (string, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if !strings.Contains(caps[i].Cmd, "/proc/net/netstat") &&
			!strings.Contains(caps[i].Cmd, "/proc/net/snmp") {
			// /proc/net/netstat is what we want, but `cat /proc/net/snmp /proc/net/netstat`
			// concatenates both — so the snmp path also matches.
			continue
		}
		if netstatExtTcpExtRe.MatchString(caps[i].Output) {
			return caps[i].Output, true
		}
	}
	return "", false
}

// parseNetstatExtTcp extracts the TcpExt: header/data line pair.
func parseNetstatExtTcp(output string) map[string]float64 {
	var headers []string
	out := map[string]float64{}
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "TcpExt:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
			headers = fields
			continue
		}
		if headers == nil || len(fields) != len(headers) {
			continue
		}
		for i := 1; i < len(fields); i++ {
			n, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				continue
			}
			out[headers[i]] = n
		}
		break
	}
	return out
}

func extractListenOverflows(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	out, ok := findNetstatExtOutput(caps)
	if !ok {
		return Value{}, false
	}
	tcpExt := parseNetstatExtTcp(out)
	overflows := tcpExt["ListenOverflows"]
	drops := tcpExt["ListenDrops"]
	if _, ok := tcpExt["ListenOverflows"]; !ok {
		return Value{}, false
	}
	note := "since boot"
	if drops > 0 {
		note = fmt.Sprintf("since boot; ListenDrops=%.0f", drops)
	}
	return Value{Number: overflows, Note: note}, true
}

// findProcNetDev locates a /proc/net/dev capture or an `ip -s link` capture
// for cumulative interface error totals.
func findProcNetDev(caps []CapturedCommand) (string, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if strings.Contains(caps[i].Cmd, "/proc/net/dev") &&
			strings.Contains(caps[i].Output, "Inter-") {
			return caps[i].Output, true
		}
	}
	return "", false
}

// extractInterfaceErrors sums RX errors + TX errors across all non-loopback
// interfaces from /proc/net/dev. (Falls back gracefully if the user only ran
// `ip -s link`, which has a different format we don't parse here.)
func extractInterfaceErrors(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	out, ok := findProcNetDev(caps)
	if !ok {
		return Value{}, false
	}
	total := 0.0
	for _, line := range strings.Split(out, "\n") {
		// Lines look like:  "  eth0: 12345 678 0 0 0 0 0 0 9876 543 0 0 0 0 0 0"
		// 16 fields after the iface name. RX errors at index 3, TX errors at index 11.
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" || name == "Inter-|" || name == "face |bytes" {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 16 {
			continue
		}
		if rxErr, err := strconv.ParseFloat(fields[2], 64); err == nil {
			total += rxErr
		}
		if txErr, err := strconv.ParseFloat(fields[10], 64); err == nil {
			total += txErr
		}
	}
	return Value{Number: total, Note: "RX+TX errors, cumulative since boot"}, true
}

func extractDmesgNetKeywords(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	seen := false
	matched := 0
	totalLines := 0
	keywords := []string{"link is down", "link is up", "carrier", "nic link"}
	for _, c := range caps {
		b := baseCmd(c.Cmd)
		if b != "dmesg" && b != "journalctl" {
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
	return Value{Text: fmt.Sprintf("%d/%d lines mention link/NIC events", matched, totalLines)}, true
}

// ----- Recall question generators -----

func netRxDropsRecall(v Value) []Question {
	return []Question{{
		Stem:    "What was the highest `rxdrop/s` value you observed across non-loopback interfaces?",
		Correct: fmt.Sprintf("%.1f", v.Number),
		Distractors: []string{
			fmt.Sprintf("%.1f", v.Number*5+1),
			fmt.Sprintf("%.1f", v.Number*0.25),
			"0.0",
		},
	}}
}

// ----- Synthesis rules -----

var networkSynthesisRules = []SynthesisRule{
	bandwidthLossConsistency,
	listenQueueAttribution,
}

// bandwidthLossConsistency teaches the marquee network lesson: the headline
// throughput rate is necessary but insufficient. Loss can be present even
// when bandwidth is well below capacity.
var bandwidthLossConsistency = SynthesisRule{
	Requires: []string{"net_rx_drops_per_sec_max", "tcp_retransmit_ratio_pct"},
	Generate: func(si SystemInfo, vs map[string]Value) (Question, bool) {
		drops := vs["net_rx_drops_per_sec_max"].Number
		retrans := vs["tcp_retransmit_ratio_pct"].Number

		var correct string
		switch {
		case drops < 1 && retrans < 0.5:
			correct = "Healthy — no local interface drops and a low retransmit ratio. The network layer is not the bottleneck."
		case drops < 1 && retrans >= 1:
			correct = "Loss is downstream of this host — non-trivial retransmits with zero local interface drops point at an intermediate switch, the peer, or the path between."
		case drops >= 5:
			correct = "Local interface is dropping packets — typically an undersized NIC ring buffer, a misconfigured driver, or the kernel queue can't drain fast enough. Tunable from this host."
		default:
			return Question{}, false
		}

		pool := []string{
			"Healthy — no local interface drops and a low retransmit ratio. The network layer is not the bottleneck.",
			"Loss is downstream of this host — non-trivial retransmits with zero local interface drops point at an intermediate switch, the peer, or the path between.",
			"Local interface is dropping packets — typically an undersized NIC ring buffer, a misconfigured driver, or the kernel queue can't drain fast enough. Tunable from this host.",
			"Cannot be assessed — these two metrics measure unrelated things.",
		}
		var distractors []string
		for _, p := range pool {
			if p != correct {
				distractors = append(distractors, p)
			}
		}
		if len(distractors) > 3 {
			distractors = distractors[:3]
		}

		return Question{
			Stem: fmt.Sprintf(
				"Max rxdrop/s observed: %.1f; TCP retransmit ratio: %.2f%%.\n"+
					"Which best describes where loss (if any) is occurring?",
				drops, retrans),
			Correct:     correct,
			Distractors: distractors,
		}, true
	},
}

// listenQueueAttribution teaches that "the network is slow" can mean
// "the application isn't accept()ing fast enough" — the listen queue
// overflows are a user-space signal, not a network-layer signal.
var listenQueueAttribution = SynthesisRule{
	Requires: []string{"tcp_listen_overflows", "tcp_retransmit_ratio_pct", "net_rx_drops_per_sec_max"},
	Generate: func(si SystemInfo, vs map[string]Value) (Question, bool) {
		overflows := vs["tcp_listen_overflows"].Number
		retrans := vs["tcp_retransmit_ratio_pct"].Number
		drops := vs["net_rx_drops_per_sec_max"].Number

		var correct string
		switch {
		case overflows == 0:
			return Question{}, false // not the interesting case
		case overflows > 0 && retrans < 0.5 && drops < 1:
			correct = "Application-side bottleneck — the listen queue is overflowing but retransmits and interface drops are healthy. The userspace process can't accept() new connections fast enough."
		case overflows > 0 && (retrans >= 1 || drops >= 5):
			correct = "Both layers are under pressure — listen queue overflows AND network-layer loss. Investigate the application's accept loop and the network in parallel."
		default:
			return Question{}, false
		}

		pool := []string{
			"Application-side bottleneck — the listen queue is overflowing but retransmits and interface drops are healthy. The userspace process can't accept() new connections fast enough.",
			"Both layers are under pressure — listen queue overflows AND network-layer loss. Investigate the application's accept loop and the network in parallel.",
			"Healthy — listen overflows are a normal background noise from past traffic spikes.",
			"Cannot be assessed — these signals measure unrelated subsystems.",
		}
		var distractors []string
		for _, p := range pool {
			if p != correct {
				distractors = append(distractors, p)
			}
		}
		if len(distractors) > 3 {
			distractors = distractors[:3]
		}

		return Question{
			Stem: fmt.Sprintf(
				"Listen overflows: %.0f cumulative; TCP retransmit ratio: %.2f%%; max rxdrop/s: %.1f.\n"+
					"Which best describes where the bottleneck lives?",
				overflows, retrans, drops),
			Correct:     correct,
			Distractors: distractors,
		}, true
	},
}

// ----- Command reference -----

var networkCommands = []CommandRef{
	{
		Cmd:     "ip -s link",
		Section: "Utilization",
		Summary: "Per-interface RX/TX counters (cumulative since boot).\nFastest orientation; useful for 'has anything ever gone wrong here'.",
	},
	{
		Cmd:     "sar -n DEV 1 N",
		Section: "Utilization",
		Summary: "Per-interface throughput rate over N intervals.\nrxkB/s and txkB/s are the headline numbers. (sysstat package.)",
	},
	{
		Cmd:     "ethtool eth0",
		Section: "Utilization",
		Summary: "Link speed/duplex for an interface.\nNeeded to convert sar's kB/s into a percent of capacity.\nReports 'Speed: Unknown!' on many virtual interfaces.",
	},
	{
		Cmd:     "ss -s",
		Section: "Utilization",
		Summary: "Socket count summary by protocol and state.\nThe TCP estab number is current connection load.",
	},
	{
		Cmd:     "sar -n EDEV 1 N",
		Section: "Saturation",
		Summary: "Per-interface error/drop rates.\nrxdrop/s > 0 = NIC ring buffer or kernel queue is filling.\n(sysstat package.)",
	},
	{
		Cmd:     "cat /proc/net/snmp",
		Section: "Saturation",
		Summary: "Protocol-level counters. The Tcp: line carries\nOutSegs and RetransSegs — divide for retransmit ratio.",
	},
	{
		Cmd:     "cat /proc/net/netstat",
		Section: "Saturation",
		Summary: "Extended TCP/IP counters. TcpExt: ListenOverflows counts\nSYNs the kernel dropped because the application's accept queue was full.",
	},
	{
		Cmd:     "ss -lnt",
		Section: "Saturation",
		Summary: "TCP listen sockets with Recv-Q (current queue) vs Send-Q (max backlog).\nRecv-Q approaching Send-Q is the live signal that ListenOverflows is climbing.",
	},
	{
		Cmd:     "netstat -s",
		Section: "Saturation",
		Summary: "Human-readable summary of /proc/net/snmp + /proc/net/netstat.\nUbiquitous but being deprecated in favour of `ss`.",
	},
	{
		Cmd:     "cat /proc/net/dev",
		Section: "Errors",
		Summary: "Cumulative per-interface counters (bytes, packets, errs, drop, ...).\nField order is stable; raw source for many other tools.",
	},
	{
		Cmd:     "dmesg -T 2>/dev/null | grep -iE 'link is|carrier|nic|ethernet'",
		Section: "Errors",
		Summary: "Kernel link-state changes and NIC driver errors.\nRepeated up/down sequences indicate a flapping cable or peer port.",
	},
}
