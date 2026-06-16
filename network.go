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
		"and asks specific questions about what you saw.",
	StepsFn:      networkSteps,
	Observations: networkObservations,
	Commands:     networkCommands,
	DiagnoseNotes: map[string]string{
		"Utilization": "`%ifutil` is the best relative signal when available; absolute throughput can still identify high offered load on virtual or lab links, but it does not prove physical link saturation without link speed.",
	},
}

const (
	networkModerateThroughputMbit = 10.0
	networkHighThroughputMbit     = 80.0
)

// ----- Guide steps -----

func networkSteps(si SystemInfo) []GuideStep {
	interfaceVariants := networkInterfaceVariants()
	interfacePick := pickStepVariant(si, interfaceVariants)

	return []GuideStep{
		{
			Name:          "interfaces",
			Intro:         "Step 1: get familiar with the interfaces and their since-boot counters.",
			Suggested:     interfacePick.Cmd,
			QuestionsFn:   combineVariantQuestions(interfaceVariants),
			QuestionCount: 3,
			AcceptAny:     true,
			Filter:        filterNoiseInterfaces,
			Teaching:      interfacePick.Teaching,
		},
		{
			Name:          "throughput",
			Intro:         "Step 2: measure per-interface throughput as a rate (utilization).\nsar samples once per second; compare against ethtool-reported link speed.",
			Suggested:     "sar -n DEV 1 3",
			QuestionsFn:   sarDevColumnQuestions,
			QuestionCount: 3,
			Filter:        filterNoiseInterfaces,
			Teaching: "rxkB/s and txkB/s give the per-second receive/transmit rate.\n" +
				"To convert to a percentage of link capacity, you need ethtool to learn\n" +
				"the link speed. On VMs and containers, virtual interfaces often report\n" +
				"'Speed: Unknown!' — graceful degradation matters here.\n\n" +
				"About those `veth*` rows: each is one end of a virtual ethernet pair\n" +
				"that connects a container's network namespace to the host bridge\n" +
				"(`docker0` or similar). The unclear hex suffix is the host-side end —\n" +
				"the matching container-side end lives inside the container. To tie a\n" +
				"`veth` back to a service name, `docker stats --format '{{.Name}} {{.NetIO}}'`\n" +
				"shows per-container net I/O directly; or `ip -n <container-pid> link`\n" +
				"shows the container-side name.",
		},
		{
			Name:          "drops",
			Intro:         "Step 3: look for saturation at the interface level — drops and overruns.",
			Suggested:     "sar -n EDEV 1 3",
			QuestionsFn:   sarEdevColumnQuestions,
			QuestionCount: 3,
			Filter:        filterNoiseInterfaces,
			Teaching: "rxdrop/s > 0 means the kernel dropped incoming packets, usually\n" +
				"because the NIC ring buffer or kernel queue was full. txdrop/s on the\n" +
				"send side means the qdisc dropped packets. Either is real saturation\n" +
				"that the bandwidth headline will not show you.",
		},
		{
			Name:          "tcp",
			Intro:         "Step 4: protocol-level signals — TCP retransmits and listen overflows.",
			Suggested:     "netstat -s",
			Alternatives:  []string{"ss -tin"},
			QuestionsFn:   combineVariantQuestions(networkTCPVariants()),
			QuestionCount: 3,
			Teaching: "`netstat -s` is the reliable first pass here because it always shows\n" +
				"cumulative TCP counters like retransmits and listen-queue overflows.\n" +
				"`ss -tin` is the live complement: it shows per-socket TCP state only\n" +
				"for currently established TCP connections, so on an idle host it may\n" +
				"print only the header line.",
		},
		{
			Name:          "sockets",
			Intro:         "Step 5: socket summary — what's currently established and what's listening.",
			Suggested:     "ss -s",
			QuestionsFn:   ssSummaryColumnQuestions,
			QuestionCount: 3,
			AcceptAny:     true,
			Teaching: "The TCP estab count tells you connection load right now. For listen\n" +
				"queue depth, `ss -lnt` shows Recv-Q (current) vs Send-Q (max backlog)\n" +
				"per listening socket — Recv-Q approaching Send-Q is the live signal\n" +
				"that ListenOverflows is about to climb.",
		},
		{
			Name:               "errors",
			Intro:              "Step 6: kernel network errors — link state changes and NIC issues.\n" + dmesgPermissionNote,
			Suggested:          "dmesg -T | grep -iE 'link is down|link up|carrier|nic|ethernet' | tail",
			Alternatives:       journalctlAlternative(si, "journalctl -k -b --no-pager | grep -iE 'link is down|link up|carrier|nic|ethernet' | tail"),
			QuestionsFn:        networkDmesgQuestions,
			AcceptAny:          true,
			EmptyOutputMessage: "No matching link or NIC errors found.",
			Teaching: "Repeated link-up/link-down sequences mean a flapping cable, transceiver,\n" +
				"or peer port. NIC driver errors point at hardware or firmware faults.",
		},
	}
}

// networkInterfaceVariants is the pool for the interfaces step. Both
// commands expose cumulative per-interface counters; their output formats
// differ enough that each carries its own question pool.
func networkInterfaceVariants() []stepVariant {
	return []stepVariant{
		{
			Cmd:         "ip -s link",
			QuestionsFn: ipLinkColumnQuestions,
			Teaching: "Each interface lists RX and TX byte/packet/error/dropped counters.\n" +
				"These are running totals since boot — useful for 'has anything ever been\n" +
				"wrong here' but not for 'is it happening now'. Use sar -n EDEV for rates.",
		},
		{
			Cmd:         "cat /proc/net/dev",
			QuestionsFn: procNetDevColumnQuestions,
			Teaching: "`/proc/net/dev` is what every interface tool (ip, sar, netstat -i)\n" +
				"reads under the hood. Format is positional, not labelled: after the\n" +
				"interface name, 8 RX fields (bytes, packets, errs, drop, fifo, frame,\n" +
				"compressed, multicast) followed by 8 TX fields in the same shape.\n" +
				"Running totals since boot — diff two reads with a known interval for a\n" +
				"rate, or just use `sar -n DEV`.",
		},
	}
}

// ipLinkHeaderRe matches the per-interface header line `ip -s link` prints,
// e.g. `2: eth0: <BROADCAST,...` or `7: veth1a2b@if6: <...`. The captured
// group is the interface name (without the `@ifN` peer suffix).
var ipLinkHeaderRe = regexp.MustCompile(`^\d+:\s+([^:@\s]+)`)

// noiseInterfacePrefixes are interface-name prefixes that belong to container,
// virtualization, and overlay subsystems rather than the physical/uplink path
// a USE walkthrough cares about. `lo` is handled separately as an exact match.
var noiseInterfacePrefixes = []string{
	"veth", "docker", "br-", "virbr", "vnet",
	"cni", "flannel", "cali", "cilium", "kube",
}

func isNoiseInterface(name string) bool {
	name = interfaceBaseName(name)
	if name == "lo" {
		return true
	}
	for _, p := range noiseInterfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func interfaceBaseName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	return name
}

func isLoopbackInterface(name string) bool {
	return interfaceBaseName(name) == "lo"
}

// filterNoiseInterfaces drops loopback and inactive container/virtual interface
// rows or stanzas from network guide output so the learner sees USE-relevant
// links. Active sar rows are kept even for veth/docker-style names because
// local lab traffic often uses those links. As a safety net, filtering returns
// the original output unchanged if it would hide every interface.
func filterNoiseInterfaces(c CapturedCommand) string {
	switch baseCmd(c.Cmd) {
	case "ip":
		return filterIPLinkOutput(c.Output)
	case "cat":
		return filterProcNetDevOutput(c.Output)
	case "sar":
		return filterSarNetworkOutput(c.Output)
	default:
		return c.Output
	}
}

func filterIPLinkOutput(output string) string {
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	keep := true
	sawIface, keptIface := false, false
	for _, line := range lines {
		if m := ipLinkHeaderRe.FindStringSubmatch(line); m != nil {
			sawIface = true
			keep = !isNoiseInterface(m[1])
			keptIface = keptIface || keep
		}
		if keep {
			out = append(out, line)
		}
	}
	if sawIface && !keptIface {
		return output
	}
	return strings.Join(out, "\n")
}

func filterProcNetDevOutput(output string) string {
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	sawIface, keptIface := false, false
	for _, line := range lines {
		idx := strings.IndexByte(line, ':')
		// Header rows ("Inter-|  Receive", " face |bytes ...") carry a `|`
		// and no interface name; keep them and any non-data lines verbatim.
		if idx < 0 || strings.Contains(line, "|") {
			out = append(out, line)
			continue
		}
		sawIface = true
		if isNoiseInterface(line[:idx]) {
			continue
		}
		keptIface = true
		out = append(out, line)
	}
	if sawIface && !keptIface {
		return output
	}
	return strings.Join(out, "\n")
}

func filterSarNetworkOutput(output string) string {
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	ifaceField := -1
	sawIface, keptIface := false, false
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			ifaceField = -1
			out = append(out, line)
			continue
		}
		if idx := sarIfaceHeaderIndex(fields); idx >= 0 {
			ifaceField = idx
			out = append(out, line)
			continue
		}
		if !isSarTableRowPrefix(fields[0]) || ifaceField < 0 || len(fields) <= ifaceField {
			out = append(out, line)
			continue
		}

		iface := fields[ifaceField]
		sawIface = true
		if isLoopbackInterface(iface) {
			continue
		}
		if isNoiseInterface(iface) && !sarRowHasNonZeroMetric(fields, ifaceField) {
			continue
		}
		keptIface = true
		out = append(out, line)
	}
	if sawIface && !keptIface {
		return output
	}
	return strings.Join(out, "\n")
}

func sarRowHasNonZeroMetric(fields []string, ifaceField int) bool {
	for _, field := range fields[ifaceField+1:] {
		v, err := strconv.ParseFloat(field, 64)
		if err == nil && v != 0 {
			return true
		}
	}
	return false
}

func sarIfaceHeaderIndex(fields []string) int {
	for i, f := range fields {
		if f != "IFACE" {
			continue
		}
		if i == 0 {
			return -1
		}
		if isSarTableRowPrefix(fields[0]) {
			return i
		}
	}
	return -1
}

func isSarTableRowPrefix(s string) bool {
	return isTimestamp(s) || s == "Average:"
}

// networkTCPVariants is the pool for the TCP-signals step. We use the
// human-readable `netstat -s` rather than a raw `cat /proc/net/snmp
// /proc/net/netstat`: the proc files dump hundreds of unlabeled,
// space-separated counters whose header and data lines are far apart —
// unreadable for a learner — while `netstat -s` surfaces the same signals
// (retransmit ratio, listen-queue overflows) with names attached, and is
// ubiquitous even on hosts without sysstat installed.
func networkTCPVariants() []stepVariant {
	return []stepVariant{
		{
			Cmd:         "ss -tin",
			QuestionsFn: ssInfoQuestions,
			Teaching: "`ss -tin` shows live established TCP sockets with kernel TCP\n" +
				"state. In the table, Send-Q is locally queued/unacknowledged data.\n" +
				"In the indented TCP-info line, `retrans:` shows live retransmission\n" +
				"state and fields like `rwnd_limited` / `sndbuf_limited` show time\n" +
				"spent limited by peer receive window or local send buffer pressure.\n" +
				"If there are no established TCP sockets at capture time, `ss -tin`\n" +
				"may print only the header row.",
		},
		{
			Cmd:         "netstat -s",
			QuestionsFn: netstatSColumnQuestions,
			Teaching: "`netstat -s` is the human-readable view of /proc/net/snmp and\n" +
				"/proc/net/netstat. Look for the Tcp section's `segments retransmitted`\n" +
				"vs `segments sent out` (the retransmit ratio) and TcpExt entries like\n" +
				"`times the listen queue of a socket overflowed` (the listen-overflow\n" +
				"count).",
		},
	}
}

// ----- Comprehension extractors (per-command) -----

// sarNetQuestions dispatches by output shape (sar has many sub-modes).

// catNetQuestions dispatches based on path looked at.

func ipLinkColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "RX:") || !strings.Contains(c.Output, "TX:") {
		return nil
	}
	return []Question{
		{
			Stem:    "In `ip -s link` RX counters, what does `bytes` represent?",
			Correct: "Running total of bytes received by the interface since the counter was reset",
			Distractors: []string{
				"Current receive throughput in bytes per second",
				"Bytes waiting in the receive queue right now",
				"Bytes dropped by the receive path",
			},
		},
		{
			Stem:    "In `ip -s link` RX counters, what does `packets` represent?",
			Correct: "Running total of packets received by the interface since the counter was reset",
			Distractors: []string{
				"Current receive packets per second",
				"Packets currently queued for userspace",
				"Packets retransmitted by TCP",
			},
		},
		{
			Stem:    "In `ip -s link` RX counters, what does `errors` represent?",
			Correct: "Running total of receive-side packet errors reported by the interface",
			Distractors: []string{
				"Application-level socket errors",
				"Packets intentionally dropped by firewall rules only",
				"Current receive error percentage",
			},
		},
		{
			Stem:    "In `ip -s link` RX counters, what does `dropped` represent?",
			Correct: "Running total of received packets dropped before delivery up the stack",
			Distractors: []string{
				"Packets dropped by remote peers",
				"Packets retransmitted after loss",
				"Current queue depth for receive packets",
			},
		},
		{
			Stem:    "In `ip -s link` RX counters, what does `overrun` represent?",
			Correct: "Running total of receive FIFO overruns where packets arrived faster than the NIC or driver could drain them",
			Distractors: []string{
				"Packets larger than the interface MTU",
				"Packets dropped by the transmit queue",
				"TCP connections that exceeded their receive window",
			},
		},
		{
			Stem:    "In `ip -s link` RX counters, what does `mcast` represent?",
			Correct: "Running total of multicast packets received by the interface",
			Distractors: []string{
				"Packets sent to the interface's MAC address only",
				"Packets dropped because of checksum errors",
				"Current multicast group membership count",
			},
		},
		{
			Stem:    "In `ip -s link` TX counters, what does `bytes` represent?",
			Correct: "Running total of bytes transmitted by the interface since the counter was reset",
			Distractors: []string{
				"Current transmit throughput in bytes per second",
				"Bytes currently waiting in the transmit queue",
				"Bytes received from remote peers",
			},
		},
		{
			Stem:    "In `ip -s link` TX counters, what does `packets` represent?",
			Correct: "Running total of packets transmitted by the interface since the counter was reset",
			Distractors: []string{
				"Current transmit packets per second",
				"Packets currently queued in TCP send buffers",
				"Packets received and forwarded by the kernel",
			},
		},
		{
			Stem:    "In `ip -s link` TX counters, what does `dropped` represent?",
			Correct: "Running total of outgoing packets dropped before transmission",
			Distractors: []string{
				"Incoming packets dropped by the peer",
				"TCP segments retransmitted after timeout",
				"Packets sent to a multicast address",
			},
		},
		{
			Stem:    "In `ip -s link` TX counters, what does `carrier` represent?",
			Correct: "Running total of transmit carrier errors reported by the interface",
			Distractors: []string{
				"The current negotiated carrier speed",
				"The number of carrier-grade NAT translations",
				"Packets carried successfully by TCP",
			},
		},
	}
}

var sarDevHeaderRe = regexp.MustCompile(`(?m)^[\d:]+(?:\s+[AP]M)?\s+IFACE.*rxkB/s.*txkB/s`)

func sarDevColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarDevHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, sarDevQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In `sar -n DEV` output, what does `%s` represent?", column)
		},
	)
}

var sarDevQuestionPicks = []columnQuestionPick{
	{
		Column:  "IFACE",
		Correct: "The NIC or virtual link name for the row",
		Distractors: []string{
			"The peer host name for the traffic",
			"The socket protocol family",
			"The configured IP address on that link",
		},
	},
	{
		Column:  "rxpck/s",
		Correct: "Packets received per second during the sample interval",
		Distractors: []string{
			"Kilobytes received per second",
			"Packets received since boot",
			"Packets dropped per second",
		},
	},
	{
		Column:  "txpck/s",
		Correct: "Packets transmitted per second during the sample interval",
		Distractors: []string{
			"Kilobytes transmitted per second",
			"Packets transmitted since boot",
			"Transmit errors per second",
		},
	},
	{
		Column:  "rxkB/s",
		Correct: "Receive throughput in kilobytes per second during the sample interval",
		Distractors: []string{
			"Receive packets per second",
			"Kilobytes received since boot",
			"Receive queue depth in kilobytes",
		},
	},
	{
		Column:  "txkB/s",
		Correct: "Transmit throughput in kilobytes per second during the sample interval",
		Distractors: []string{
			"Transmit packets per second",
			"Kilobytes transmitted since boot",
			"Transmit queue depth in kilobytes",
		},
	},
	{
		Column:  "rxcmp/s",
		Correct: "Compressed packets received per second",
		Distractors: []string{
			"Packets received with checksum errors per second",
			"Packets compressed by TCP per second",
			"Receive completions from the NIC per second",
		},
	},
	{
		Column:  "txcmp/s",
		Correct: "Compressed packets transmitted per second",
		Distractors: []string{
			"Packets transmitted with checksum errors per second",
			"Transmit completions from the NIC per second",
			"Packets compressed by TLS per second",
		},
	},
	{
		Column:  "rxmcst/s",
		Correct: "Multicast packets received per second",
		Distractors: []string{
			"Multicast packets transmitted per second",
			"Packets received from the most active peer",
			"Receive errors caused by multicast traffic",
		},
	},
	{
		Column:  "%ifutil",
		Correct: "Estimated percentage of link capacity in use",
		Distractors: []string{
			"Percent of packets that were dropped",
			"Percent of CPU time spent handling this link",
			"Percent of socket buffers currently allocated",
		},
	},
}

var sarEdevHeaderRe = regexp.MustCompile(`(?m)^[\d:]+(?:\s+[AP]M)?\s+IFACE.*rxdrop/s`)

func sarEdevColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarEdevHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, sarEdevQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In `sar -n EDEV` output, what does `%s` represent?", column)
		},
	)
}

var sarEdevQuestionPicks = []columnQuestionPick{
	{
		Column:  "IFACE",
		Correct: "The NIC or virtual link name for the row",
		Distractors: []string{
			"The remote endpoint for the errors",
			"The IP protocol being reported",
			"The device driver's module name",
		},
	},
	{
		Column:  "rxerr/s",
		Correct: "Receive errors per second during the sample interval",
		Distractors: []string{
			"Receive packets per second",
			"Receive drops since boot",
			"TCP retransmits per second",
		},
	},
	{
		Column:  "txerr/s",
		Correct: "Transmit errors per second during the sample interval",
		Distractors: []string{
			"Transmit packets per second",
			"Transmit drops since boot",
			"TCP reset packets per second",
		},
	},
	{
		Column:  "coll/s",
		Correct: "Collisions per second, mainly relevant to shared or half-duplex media",
		Distractors: []string{
			"Socket accept collisions per second",
			"Packets dropped by qdisc per second",
			"TCP checksum collisions per second",
		},
	},
	{
		Column:  "rxdrop/s",
		Correct: "Received packets dropped per second before delivery up the stack",
		Distractors: []string{
			"Packets retransmitted by TCP per second",
			"Receive packets with checksum errors per second",
			"Incoming connections refused per second",
		},
	},
	{
		Column:  "txdrop/s",
		Correct: "Outgoing packets dropped per second before transmission",
		Distractors: []string{
			"Transmit packets with checksum errors per second",
			"TCP segments retransmitted per second",
			"Connections dropped by the application per second",
		},
	},
	{
		Column:  "txcarr/s",
		Correct: "Transmit carrier errors per second",
		Distractors: []string{
			"Transmit packets carried successfully per second",
			"Carrier speed changes per second",
			"TCP connection carry-over events per second",
		},
	},
	{
		Column:  "rxfram/s",
		Correct: "Receive frame alignment errors per second",
		Distractors: []string{
			"Receive packets per second after framing overhead",
			"Firewall frame drops per second",
			"Frames forwarded by the kernel per second",
		},
	},
	{
		Column:  "rxfifo/s",
		Correct: "Receive FIFO overrun errors per second",
		Distractors: []string{
			"Receive queue length sampled once per second",
			"Packets received from FIFO sockets per second",
			"Receive packets forwarded per second",
		},
	},
	{
		Column:  "txfifo/s",
		Correct: "Transmit FIFO errors per second",
		Distractors: []string{
			"Transmit queue length sampled once per second",
			"Packets transmitted through FIFO sockets per second",
			"Transmit packets forwarded per second",
		},
	},
}

func ssSummaryColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "TCP:") || !strings.Contains(c.Output, "estab") {
		return nil
	}
	return columnQuestionsFromPicks(
		availableSSSummaryQuestionPicks(c.Output),
		func(column string) string {
			return fmt.Sprintf("In `ss -s` output, what does `%s` represent?", column)
		},
	)
}

var ssSummaryQuestionPicks = []columnQuestionPick{
	{
		Column:  "estab",
		Correct: "TCP connections currently fully open after completing the handshake",
		Distractors: []string{
			"TCP connections opened since boot",
			"TCP listen sockets waiting for accept",
			"TCP connections currently retransmitting",
		},
	},
	{
		Column:  "closed",
		Correct: "TCP sockets currently in the terminal state category for the summary",
		Distractors: []string{
			"Connections ended since boot",
			"Listen sockets with empty queues",
			"Connections blocked by firewall policy",
		},
	},
	{
		Column:  "orphaned",
		Correct: "TCP sockets no longer attached to a user file descriptor",
		Distractors: []string{
			"Connections without a configured route",
			"Listen sockets without a process bound",
			"Packets dropped because their process exited",
		},
	},
	{
		Column:  "timewait",
		Correct: "TCP connections in the post-close timeout state before the socket can be fully removed",
		Distractors: []string{
			"Connections queued until the application calls accept",
			"Connections blocked on DNS resolution",
			"Total duration of TCP send stalls",
		},
	},
	{
		Column:  "Transport",
		Correct: "The protocol or socket family name for each row in the summary table",
		Distractors: []string{
			"The network interface carrying the sockets",
			"The peer transport address",
			"The process command owning the sockets",
		},
	},
	{
		Column:  "Total",
		Correct: "The total socket count for that transport row",
		Distractors: []string{
			"The total bytes sent by that transport",
			"The total sockets opened since boot",
			"The total backlog capacity for listen sockets",
		},
	},
	{
		Column:  "IP",
		Correct: "The IPv4 socket count for that transport row",
		Distractors: []string{
			"The local IP address for the row",
			"The IP packet error count",
			"The number of interfaces with an IP address",
		},
	},
	{
		Column:  "IPv6",
		Correct: "The IPv6 socket count for that transport row",
		Distractors: []string{
			"The number of IPv6 routes on the host",
			"The remote IPv6 endpoint for the row",
			"The IPv6 packet retransmit count",
		},
	},
}

func availableSSSummaryQuestionPicks(output string) []columnQuestionPick {
	var picks []columnQuestionPick
	for _, pick := range ssSummaryQuestionPicks {
		if outputHasTokenName(output, pick.Column) {
			picks = append(picks, pick)
		}
	}
	return picks
}

func outputHasTokenName(output, name string) bool {
	for _, field := range strings.Fields(output) {
		if strings.Trim(field, "(),:") == name {
			return true
		}
	}
	return false
}

func ssInfoQuestions(si SystemInfo, c CapturedCommand) []Question {
	if ssInfoHeaderOnly(c.Output) {
		return []Question{{
			Stem:    "In `ss -tin`, seeing only the header row and no socket entries means:",
			Correct: "There were no established TCP sockets to inspect at capture time, so there was no live per-socket TCP info to show",
			Distractors: []string{
				"The kernel cleared all TCP statistics since boot",
				"TCP listeners were hidden because you forgot `sudo`",
				"The interface transmit queue was empty, so socket rows were suppressed",
			},
		}}
	}
	if !ssInfoOutputDetected(c.Output) {
		return nil
	}
	return []Question{
		{
			Stem:    "In `ss -tin` output for an established TCP socket, what does `Send-Q` indicate?",
			Correct: "Bytes queued locally or sent but not yet acknowledged by the peer",
			Distractors: []string{
				"The maximum listen backlog for that connection",
				"The interface transmit queue length in packets",
				"Bytes the remote process has not read from its socket",
			},
		},
		{
			Stem:    "In `ss -tin` TCP details, what does non-zero `retrans:` indicate?",
			Correct: "TCP has retransmitted data on that socket, usually because packets or ACKs were lost",
			Distractors: []string{
				"The application retried a failed connect call",
				"The socket was moved between network namespaces",
				"The interface compressed packets before transmission",
			},
		},
		{
			Stem:    "In `ss -tin`, what do `rwnd_limited` and `sndbuf_limited` time percentages point to?",
			Correct: "Time the TCP sender was limited by the peer receive window or local send buffer",
			Distractors: []string{
				"Percent of packets dropped by the NIC driver",
				"Percent of CPU time spent in softirq processing",
				"Share of link capacity currently used by multicast traffic",
			},
		},
	}
}

func ssInfoOutputDetected(output string) bool {
	low := strings.ToLower(output)
	return strings.Contains(low, "cwnd:") ||
		strings.Contains(low, "retrans:") ||
		strings.Contains(low, "rwnd_limited:") ||
		strings.Contains(low, "sndbuf_limited:")
}

func ssInfoHeaderOnly(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 1 {
		return false
	}
	line := lines[0]
	return strings.Contains(line, "State") &&
		strings.Contains(line, "Recv-Q") &&
		strings.Contains(line, "Send-Q") &&
		strings.Contains(line, "Local Address:Port") &&
		strings.Contains(line, "Peer Address:Port")
}

var snmpTcpHeaderRe = regexp.MustCompile(`(?m)^Tcp:\s+RtoAlgorithm`)
var netstatExtTcpExtRe = regexp.MustCompile(`(?m)^TcpExt:\s+\w`)

func procNetDevColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "Inter-") || !strings.Contains(c.Output, "Receive") {
		return nil
	}
	return []Question{
		{
			Stem:    "In `/proc/net/dev`, in the `Receive` section, what does the `bytes` column represent?",
			Correct: "Running total of bytes received by the interface",
			Distractors: []string{
				"Current receive throughput in bytes per second",
				"Bytes currently queued in socket buffers",
				"Bytes dropped by the interface",
			},
		},
		{
			Stem:    "In `/proc/net/dev`, in the `Receive` section, what does the `errs` column represent?",
			Correct: "Running total of receive errors for the interface",
			Distractors: []string{
				"TCP errors reported by applications",
				"Receive packets dropped by firewall rules only",
				"Current receive error rate per second",
			},
		},
		{
			Stem:    "In `/proc/net/dev`, in the `Receive` section, what does the `drop` column represent?",
			Correct: "Running total of received packets dropped before delivery",
			Distractors: []string{
				"TCP retransmitted packets",
				"Packets dropped by the remote peer",
				"Current receive queue length",
			},
		},
		{
			Stem:    "In `/proc/net/dev`, in the `Transmit` section, what does the `bytes` column represent?",
			Correct: "Running total of bytes transmitted by the interface",
			Distractors: []string{
				"Current transmit throughput in bytes per second",
				"Bytes waiting in the qdisc",
				"Bytes received and forwarded",
			},
		},
		{
			Stem:    "In `/proc/net/dev`, in the `Transmit` section, what does the `drop` column represent?",
			Correct: "Running total of outgoing packets dropped before transmission",
			Distractors: []string{
				"Incoming packets dropped by the peer",
				"TCP connections dropped by applications",
				"Packets transmitted successfully",
			},
		},
	}
}

// netstatSColumnQuestions returns the pool of phrase-keyed questions for
// `netstat -s` output. Unlike column-headed tables, netstat -s is a
// section-and-prose format, so we gate each question on a phrase appearing
// in the captured output rather than on a column name.
func netstatSColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if !strings.Contains(low, "tcp") || !strings.Contains(low, "segments") {
		return nil
	}
	var qs []Question
	for _, pick := range netstatSPicks {
		if !strings.Contains(low, pick.MarkerLower) {
			continue
		}
		qs = append(qs, Question{
			Stem:        pick.Stem,
			Correct:     pick.Correct,
			Distractors: pick.Distractors,
		})
	}
	return qs
}

type phraseQuestionPick struct {
	MarkerLower string
	Stem        string
	Correct     string
	Distractors []string
}

var netstatSPicks = []phraseQuestionPick{
	{
		MarkerLower: "segments retransmitted",
		Stem:        "In `netstat -s` output, a high count of `segments retransmitted` relative to `segments sent` means:",
		Correct:     "End-to-end packet loss — somewhere between this host and the peer, segments are being dropped",
		Distractors: []string{
			"The local TCP stack is misconfigured",
			"Too-frequent retransmit timeout firing caused by clock drift",
			"The application is closing connections without a FIN",
		},
	},
	{
		MarkerLower: "active connection openings",
		Stem:        "In `netstat -s`, what does the Tcp section's `active connection openings` count?",
		Correct:     "TCP connections started from this host (that is, local `connect()` calls that completed the handshake)",
		Distractors: []string{
			"TCP listeners currently bound on this host",
			"Inbound TCP connections accepted by local servers",
			"TCP connections that are currently in ESTABLISHED state",
		},
	},
	{
		MarkerLower: "passive connection openings",
		Stem:        "In `netstat -s`, what does the Tcp section's `passive connection openings` count?",
		Correct:     "Inbound TCP connections accepted by local listening sockets",
		Distractors: []string{
			"Outbound TCP connections initiated from this host",
			"TCP listeners closed without serving any client",
			"TCP connections opened in the background by retransmit timers",
		},
	},
	{
		MarkerLower: "failed connection attempts",
		Stem:        "In `netstat -s`, what does the Tcp section's `failed connection attempts` count?",
		Correct:     "TCP connection attempts that failed before reaching ESTABLISHED — for example, SYN with no SYN-ACK, or RST during handshake",
		Distractors: []string{
			"Connections that succeeded but failed authentication at the application layer",
			"Connections closed by the peer immediately after handshake",
			"DNS lookups that failed before a connection could be opened",
		},
	},
	{
		MarkerLower: "connection resets received",
		Stem:        "In `netstat -s`, what does the Tcp section's `connection resets received` count?",
		Correct:     "TCP connections this host received an RST for — that is, the peer ended the connection",
		Distractors: []string{
			"RSTs this host sent because the peer was unreachable",
			"Connections this host reset because the application crashed",
			"Connections that timed out while waiting for the peer to ACK",
		},
	},
	{
		MarkerLower: "times the listen queue",
		Stem:        "In `netstat -s` TcpExt, what does the line `times the listen queue of a socket overflowed` measure?",
		Correct:     "How many SYNs the kernel dropped because the application's accept queue (Send-Q in `ss -lnt`) was full — an application-side bottleneck, not network loss",
		Distractors: []string{
			"How many times the NIC ring buffer overflowed",
			"How many TCP receive windows overflowed on established connections",
			"How many listen sockets had to be re-bound after a port collision",
		},
	},
	{
		MarkerLower: "resets sent",
		Stem:        "In `netstat -s`, what does the Tcp section's `resets sent` count?",
		Correct:     "TCP RST segments this host transmitted, including kernel-generated RSTs to non-listening ports",
		Distractors: []string{
			"TCP connections reset by the peer and received here",
			"Application-level reconnect attempts",
			"Outbound packets dropped by qdisc",
		},
	},
}

// ----- Observations -----

var networkObservations = []Observation{
	{
		Name:      "net_peak_ifutil_pct",
		Title:     "Peak interface utilization (%ifutil)",
		Section:   "Utilization",
		Extract:   extractSarIfutilPeak,
		Verdict:   verdictNetIfutil,
		Heuristic: "%ifutil is sar's estimate of link utilization from throughput divided by interface-reported speed; on virtual links that speed is synthetic, so treat it as a useful estimate rather than a hard physical bottleneck",
	},
	{
		Name:      "net_peak_throughput_mbps",
		Title:     "Peak network throughput",
		Section:   "Utilization",
		Extract:   extractSarNetPeakThroughput,
		Verdict:   verdictNetThroughput,
		Heuristic: "absolute throughput is not link utilization, but sustained tens or hundreds of Mbit/s on a lab or virtual link is high offered network load even when %ifutil is unavailable or synthetic",
	},
	{
		Name:      "net_peak_rx_kbps",
		Title:     "Peak RX rate (across non-lo ifaces)",
		Section:   "Utilization",
		Extract:   extractSarNetPeak("rxkB/s"),
		Heuristic: "absolute RX throughput is context only — utilization depends on the interface link speed, which the tool doesn't know",
	},
	{
		Name:      "net_peak_tx_kbps",
		Title:     "Peak TX rate (across non-lo ifaces)",
		Section:   "Utilization",
		Extract:   extractSarNetPeak("txkB/s"),
		Heuristic: "absolute TX throughput is context only — utilization depends on the interface link speed, which the tool doesn't know",
	},
	{
		Name:      "tcp_estab_connections",
		Title:     "Established TCP connections",
		Section:   "Utilization",
		Extract:   extractTCPEstab,
		Heuristic: "connection count alone is context only — what counts as 'high' depends on the workload",
	},
	{
		Name:      "net_rx_drops_per_sec_max",
		Title:     "Max rxdrop/s (sar -n EDEV)",
		Section:   "Saturation",
		Extract:   extractSarEdevPeak("rxdrop/s"),
		Verdict:   verdictNetDrops,
		Heuristic: "rxdrop/s > 0 = NIC ring buffer or kernel queue is filling = receive-side saturation",
	},
	{
		Name:      "tcp_retransmit_ratio_pct",
		Title:     "TCP retransmit ratio",
		Section:   "Saturation",
		Extract:   extractTCPRetransmitRatio,
		Verdict:   verdictRetransRatio,
		Heuristic: "retransmit ratio > ~0.5% steady = end-to-end packet loss somewhere between this host and its peers",
	},
	{
		Name:      "tcp_ss_retrans_sockets",
		Title:     "TCP sockets retransmitting (ss -tin)",
		Section:   "Saturation",
		Extract:   extractSsTCPRetransSockets,
		Verdict:   verdictPositiveIsHigh,
		Heuristic: "non-zero `retrans:` in `ss -tin` = live TCP retransmission on an established socket",
	},
	{
		Name:      "tcp_ss_sendq_max",
		Title:     "Max TCP Send-Q (ss -tin)",
		Section:   "Saturation",
		Extract:   extractSsTCPMaxSendQ,
		Verdict:   verdictPositiveIsHigh,
		Heuristic: "non-zero Send-Q on an established TCP socket means local data is queued or unacknowledged; sustained non-zero values indicate sender-side backpressure",
	},
	{
		Name:      "tcp_ss_limited_pct_max",
		Title:     "Max TCP rwnd/sndbuf limited time (ss -tin)",
		Section:   "Saturation",
		Extract:   extractSsTCPLimitedPct,
		Verdict:   verdictPositiveIsHigh,
		Heuristic: "rwnd_limited or sndbuf_limited time in `ss -tin` means TCP spent time blocked by peer receive-window or local send-buffer limits",
	},
	{
		Name:      "tcp_listen_overflows",
		Title:     "Listen overflows (since boot)",
		Section:   "Saturation",
		Extract:   extractListenOverflows,
		Verdict:   verdictListenOverflows,
		Heuristic: "ListenOverflows > 0 = SYNs the kernel dropped because the app's accept queue was full = application-side saturation (counter is a running total since boot — context matters)",
	},
	{
		Name:      "net_iface_errors_total",
		Title:     "Interface errors (since boot)",
		Section:   "Errors",
		Extract:   extractInterfaceErrors,
		Verdict:   verdictNetIfaceErrors,
		Heuristic: "RX/TX errors on a NIC = link- or driver-level failures (counter is a running total since boot — a single old value isn't necessarily current)",
	},
	{
		Name:      "dmesg_net_keywords",
		Title:     "dmesg link/NIC events",
		Section:   "Errors",
		Extract:   extractDmesgNetKeywords,
		Verdict:   verdictDmesgNet,
		Heuristic: "repeated link up/down / carrier / NIC entries in the kernel log = flapping cable, peer port, or NIC driver issues",
	},
}

// ----- Diagnosis verdicts -----

func verdictNetIfutil(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number >= 80:
		return SignalHigh
	case v.Number >= 50:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictNetThroughput(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number >= networkHighThroughputMbit:
		return SignalHigh
	case v.Number >= networkModerateThroughputMbit:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictPositiveIsHigh(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

func verdictNetDrops(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

func verdictRetransRatio(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number > 0.5:
		return SignalHigh
	case v.Number > 0:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictListenOverflows(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

func verdictNetIfaceErrors(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

func verdictDmesgNet(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

// extractSarIfutilPeak finds the peak interface utilization percentage from
// `sar -n DEV`. It includes virtual interfaces because container-generated
// load commonly appears on veth pairs, but skips loopback.
func extractSarIfutilPeak(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	max := 0.0
	maxIface := ""
	seen := false
	for _, c := range caps {
		if baseCmd(c.Cmd) != "sar" {
			continue
		}
		rows := parseSarTable(c.Output, "%ifutil")
		for _, r := range rows {
			if r["IFACE"] == "lo" {
				continue
			}
			v, err := strconv.ParseFloat(r["%ifutil"], 64)
			if err != nil {
				continue
			}
			seen = true
			if v > max {
				max = v
				maxIface = r["IFACE"]
			}
		}
	}
	if !seen {
		return Value{}, false
	}
	note := "uses interface-reported speed"
	if maxIface != "" {
		note = "peak on " + maxIface + "; " + note
	}
	return Value{Number: max, Unit: "%", Note: note}, true
}

func extractSarNetPeakThroughput(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	maxKBps := 0.0
	maxIface := ""
	maxDirection := ""
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
			for _, col := range []string{"rxkB/s", "txkB/s"} {
				v, err := strconv.ParseFloat(r[col], 64)
				if err != nil {
					continue
				}
				seen = true
				if v > maxKBps {
					maxKBps = v
					maxIface = r["IFACE"]
					maxDirection = strings.TrimSuffix(col, "kB/s")
				}
			}
		}
	}
	if !seen {
		return Value{}, false
	}
	note := "from sar kB/s"
	if maxIface != "" && maxDirection != "" {
		note = fmt.Sprintf("peak %s on %s; %s", maxDirection, maxIface, note)
	}
	return Value{Number: maxKBps * 8 / 1000, Unit: " Mbit/s", Note: note}, true
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
		// Header rows begin with a timestamp and contain IFACE. Some locales
		// insert an AM/PM column between the timestamp and IFACE, so locate
		// IFACE by name instead of assuming a fixed position.
		if indexOfStr(fields, "IFACE") > 0 && isTimestamp(fields[0]) {
			headers = fields // includes the timestamp and any AM/PM column
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
		if !commandHasPath(caps[i].Cmd, "/proc/net/snmp") {
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
	if estab, ok := findSsEstab(caps); ok {
		return Value{Number: estab}, true
	}
	if stats, ok := findSsTCPInfoStats(caps); ok {
		return Value{Number: stats.EstablishedCount}, true
	}
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
	if sent, retrans, ok := findNetstatSRetransmits(caps); ok && sent > 0 {
		ratio := retrans / sent * 100
		return Value{Number: ratio, Unit: "%", Note: fmt.Sprintf("%.0f / %.0f", retrans, sent)}, true
	}
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

func findSsEstab(caps []CapturedCommand) (float64, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if commandBase(caps[i].Cmd) != "ss" || !commandHasOption(caps[i].Cmd, "-s", "--summary") {
			continue
		}
		if estab, ok := parseSsEstab(caps[i].Output); ok {
			return estab, true
		}
	}
	return 0, false
}

func parseSsEstab(output string) (float64, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TCP:") {
			continue
		}
		for _, field := range strings.Fields(line) {
			field = strings.Trim(field, "(),")
			if strings.HasPrefix(field, "estab") && field != "estab" {
				// Defensive for compact forms like "estab142", if any.
				if n, err := strconv.ParseFloat(strings.TrimPrefix(field, "estab"), 64); err == nil {
					return n, true
				}
			}
		}
		fields := strings.Fields(line)
		for i, field := range fields {
			if strings.Trim(field, "(),") != "estab" || i+1 >= len(fields) {
				continue
			}
			n, err := strconv.ParseFloat(strings.Trim(fields[i+1], "(),"), 64)
			if err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

type ssTCPInfoStats struct {
	Captured         bool
	HasEstablished   bool
	EstablishedCount float64
	MaxSendQ         float64
	RetransSockets   float64
	MaxLimitedPct    float64
}

func extractSsTCPMaxSendQ(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	stats, ok := findSsTCPInfoStats(caps)
	if !ok {
		return Value{}, false
	}
	if !stats.HasEstablished {
		return Value{Number: 0, Unit: " B", Note: "no established TCP sockets at capture time"}, true
	}
	return Value{Number: stats.MaxSendQ, Unit: " B", Note: "established TCP sockets"}, true
}

func extractSsTCPRetransSockets(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	stats, ok := findSsTCPInfoStats(caps)
	if !ok {
		return Value{}, false
	}
	if !stats.HasEstablished {
		return Value{Number: 0, Unit: " sockets", Note: "no established TCP sockets at capture time"}, true
	}
	return Value{Number: stats.RetransSockets, Unit: " sockets"}, true
}

func extractSsTCPLimitedPct(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	stats, ok := findSsTCPInfoStats(caps)
	if !ok {
		return Value{}, false
	}
	if !stats.HasEstablished {
		return Value{Number: 0, Unit: "%", Note: "no established TCP sockets at capture time"}, true
	}
	return Value{Number: stats.MaxLimitedPct, Unit: "%", Note: "max rwnd/sndbuf limited time"}, true
}

func findSsTCPInfoStats(caps []CapturedCommand) (ssTCPInfoStats, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if commandBase(caps[i].Cmd) != "ss" {
			continue
		}
		if stats := parseSsTCPInfo(caps[i].Output); stats.Captured {
			return stats, true
		}
	}
	return ssTCPInfoStats{}, false
}

func parseSsTCPInfo(output string) ssTCPInfoStats {
	var stats ssTCPInfoStats
	if ssInfoHeaderOnly(output) {
		stats.Captured = true
		return stats
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) >= 3 && isTCPSocketState(fields[0]) {
			stats.Captured = true
			if strings.EqualFold(fields[0], "ESTAB") {
				stats.HasEstablished = true
				stats.EstablishedCount++
			}
			if sendQ, err := strconv.ParseFloat(fields[2], 64); err == nil && sendQ > stats.MaxSendQ {
				stats.MaxSendQ = sendQ
			}
			continue
		}
		if strings.Contains(line, "cwnd:") || strings.Contains(line, "retrans:") {
			stats.Captured = true
		}
		if ssLineHasRetrans(line) {
			stats.RetransSockets++
		}
		if pct := ssLineMaxLimitedPct(line); pct > stats.MaxLimitedPct {
			stats.MaxLimitedPct = pct
		}
	}
	return stats
}

func isTCPSocketState(s string) bool {
	switch strings.ToUpper(s) {
	case "ESTAB", "SYN-SENT", "SYN-RECV", "FIN-WAIT-1", "FIN-WAIT-2",
		"TIME-WAIT", "CLOSE-WAIT", "LAST-ACK", "CLOSING":
		return true
	default:
		return false
	}
}

func ssLineHasRetrans(line string) bool {
	for _, field := range strings.Fields(line) {
		field = strings.Trim(field, ",")
		if !strings.HasPrefix(field, "retrans:") {
			continue
		}
		raw := strings.TrimPrefix(field, "retrans:")
		for _, part := range strings.Split(raw, "/") {
			n, err := strconv.ParseFloat(strings.Trim(part, ","), 64)
			if err == nil && n > 0 {
				return true
			}
		}
	}
	return false
}

func ssLineMaxLimitedPct(line string) float64 {
	max := 0.0
	for _, field := range strings.Fields(line) {
		if !strings.Contains(field, "rwnd_limited:") && !strings.Contains(field, "sndbuf_limited:") {
			continue
		}
		if pct, ok := percentInParens(field); ok && pct > max {
			max = pct
		}
	}
	return max
}

func percentInParens(s string) (float64, bool) {
	start := strings.LastIndexByte(s, '(')
	end := strings.LastIndexByte(s, '%')
	if start < 0 || end <= start {
		return 0, false
	}
	n, err := strconv.ParseFloat(s[start+1:end], 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func findNetstatSRetransmits(caps []CapturedCommand) (sent, retrans float64, ok bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if commandBase(caps[i].Cmd) != "netstat" || !commandHasOption(caps[i].Cmd, "-s", "--statistics") {
			continue
		}
		if sent, retrans, ok = parseNetstatSRetransmits(caps[i].Output); ok {
			return sent, retrans, true
		}
	}
	return 0, 0, false
}

func parseNetstatSRetransmits(output string) (sent, retrans float64, ok bool) {
	for _, line := range strings.Split(output, "\n") {
		low := strings.ToLower(strings.TrimSpace(line))
		switch {
		case strings.Contains(low, "segments sent out") || strings.Contains(low, "segments send out"):
			if n, ok := leadingNumber(low); ok {
				sent = n
			}
		case strings.Contains(low, "segments retransmitted"):
			if n, ok := leadingNumber(low); ok {
				retrans = n
			}
		}
	}
	return sent, retrans, sent > 0
}

// findNetstatExtOutput locates a /proc/net/netstat capture with a TcpExt: line.
func findNetstatExtOutput(caps []CapturedCommand) (string, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if !commandHasPath(caps[i].Cmd, "/proc/net/netstat") &&
			!commandHasPath(caps[i].Cmd, "/proc/net/snmp") {
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
	if overflows, drops, ok := findNetstatSListenOverflows(caps); ok {
		note := "since boot"
		if drops > 0 {
			note = fmt.Sprintf("since boot; ListenDrops=%.0f", drops)
		}
		return Value{Number: overflows, Note: note}, true
	}
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

func findNetstatSListenOverflows(caps []CapturedCommand) (overflows, drops float64, ok bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if commandBase(caps[i].Cmd) != "netstat" || !commandHasOption(caps[i].Cmd, "-s", "--statistics") {
			continue
		}
		if overflows, drops, ok = parseNetstatSListenOverflows(caps[i].Output); ok {
			return overflows, drops, true
		}
	}
	return 0, 0, false
}

func parseNetstatSListenOverflows(output string) (overflows, drops float64, ok bool) {
	for _, line := range strings.Split(output, "\n") {
		low := strings.ToLower(strings.TrimSpace(line))
		switch {
		case strings.Contains(low, "times the listen queue") && strings.Contains(low, "overflow"):
			if n, ok := leadingNumber(low); ok {
				overflows = n
			}
		case strings.Contains(low, "syns to listen sockets dropped"):
			if n, ok := leadingNumber(low); ok {
				drops = n
			}
		}
	}
	return overflows, drops, overflows > 0 || drops > 0
}

func leadingNumber(line string) (float64, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	return n, err == nil
}

// findProcNetDev locates a /proc/net/dev capture or an `ip -s link` capture
// for cumulative interface error totals.
func findProcNetDev(caps []CapturedCommand) (string, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if commandHasPath(caps[i].Cmd, "/proc/net/dev") &&
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
	return Value{Number: total, Note: "RX+TX errors, running total since boot"}, true
}

func extractDmesgNetKeywords(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	keywords := []string{"link is down", "link is up", "carrier", "nic link"}
	return extractKernelLogKeywords(caps, keywords, "link/NIC events")
}

// ----- Recall question generators -----

// ----- Synthesis rules -----

func networkDmesgQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if !strings.Contains(low, "link is") &&
		!strings.Contains(low, "carrier") &&
		!strings.Contains(low, "nic link") {
		return nil
	}
	tool := kernelLogQuestionTool(c.Cmd)
	return []Question{{
		Stem:    fmt.Sprintf("Repeated `Link is Down` followed by `Link is Up` for the same interface in `%s` output suggests:", tool),
		Correct: "A flapping cable, transceiver, or peer port — the physical layer is disconnecting off and on",
		Distractors: []string{
			"The kernel is rotating IP addresses for that interface",
			"DHCP is renewing the lease and briefly losing the link",
			"The NIC driver is being reloaded by udev",
		},
	}}
}

var networkCommands = []CommandRef{
	{
		Cmd:     "ip -s link",
		Section: "Utilization",
		Summary: "Per-interface RX/TX counters (running totals since boot).\nFastest first look; useful for 'has anything ever gone wrong here'.",
	},
	{
		Cmd:      "sar -n DEV 1 N",
		Section:  "Utilization",
		Summary:  "Per-interface throughput rate over N intervals.\nrxkB/s and txkB/s are the headline numbers. (sysstat package.)",
		Requires: []string{"sar"},
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
		Cmd:          "sar -n EDEV 1 N",
		Section:      "Saturation",
		Summary:      "Per-interface error/drop rates.\nrxdrop/s > 0 = NIC ring buffer or kernel queue is filling.\n(sysstat package.)",
		Requires:     []string{"sar"},
		DiagnoseRank: 1,
	},
	{
		Cmd:          "ss -tin",
		Section:      "Saturation",
		Summary:      "Live established TCP socket state.\nNon-zero Send-Q, retrans, rwnd_limited, or sndbuf_limited points at socket/path backpressure.\nMay show only the header row when there are no established TCP sockets.",
		DiagnoseRank: 2,
	},
	{
		Cmd:     "ss -lnt",
		Section: "Saturation",
		Summary: "TCP listen sockets with Recv-Q (current queue) vs Send-Q (max backlog).\nRecv-Q approaching Send-Q is the live signal that ListenOverflows is climbing.",
	},
	{
		Cmd:          "netstat -s",
		Section:      "Saturation",
		Summary:      "Human-readable summary of /proc/net/snmp + /proc/net/netstat.\nFound almost everywhere but being phased out in favour of `ss`.",
		DiagnoseRank: 3,
	},
	{
		Cmd:          "cat /proc/net/dev",
		Section:      "Errors",
		Summary:      "Per-interface counters since boot (bytes, packets, errs, drop, ...).\nField order is stable; raw source for many other tools.",
		DiagnoseRank: 1,
	},
	{
		Cmd:          "dmesg -T | grep -iE 'link is|carrier|nic|ethernet'",
		Section:      "Errors",
		Summary:      "Kernel link-state changes and NIC driver errors.\nRepeated up/down sequences mean a flapping cable or peer port.\n" + dmesgPermissionNote,
		DiagnoseRank: 2,
	},
	{
		Cmd:                 "journalctl -k -b --no-pager | grep -iE 'link is|carrier|nic|ethernet'",
		Section:             "Errors",
		Summary:             "Kernel link-state changes and NIC driver errors via journald.\nAlternative to dmesg on systemd systems.",
		Requires:            []string{"journalctl"},
		HideWhenUnavailable: true,
		DiagnoseRank:        3,
	},
}
