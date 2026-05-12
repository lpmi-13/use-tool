package main

import (
	"strings"
	"testing"
)

const sampleSarDev = `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (4 CPU)

14:00:01        IFACE   rxpck/s   txpck/s    rxkB/s    txkB/s   rxcmp/s   txcmp/s  rxmcst/s   %ifutil
14:00:02           lo      0.00      0.00      0.00      0.00      0.00      0.00      0.00      0.00
14:00:02         eth0   1234.56    789.01   2048.56   1024.32      0.00      0.00      0.00      1.50
14:00:03           lo      0.00      0.00      0.00      0.00      0.00      0.00      0.00      0.00
14:00:03         eth0   2500.00   1500.00   4096.00   2048.00      0.00      0.00      0.00      3.00
Average:           lo      0.00      0.00      0.00      0.00      0.00      0.00      0.00      0.00
Average:         eth0   1867.28   1144.51   3072.28   1536.16      0.00      0.00      0.00      2.25`

const sampleSarEdev = `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (4 CPU)

14:00:01        IFACE   rxerr/s   txerr/s    coll/s  rxdrop/s  txdrop/s  txcarr/s  rxfram/s  rxfifo/s  txfifo/s
14:00:02           lo      0.00      0.00      0.00      0.00      0.00      0.00      0.00      0.00      0.00
14:00:02         eth0      0.00      0.00      0.00      8.00      0.00      0.00      0.00      0.00      0.00
14:00:03           lo      0.00      0.00      0.00      0.00      0.00      0.00      0.00      0.00      0.00
14:00:03         eth0      0.00      0.00      0.00     12.50      0.00      0.00      0.00      0.00      0.00`

const sampleSnmpTcp = `Ip: Forwarding DefaultTTL InReceives InHdrErrors
Ip: 1 64 12345 0
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 12345 6789 100 50 142 1234567 1000000 5000 0 100 0
Udp: InDatagrams NoPorts InErrors OutDatagrams
Udp: 100 0 0 200`

const sampleNetstatExt = `TcpExt: SyncookiesSent SyncookiesRecv SyncookiesFailed EmbryonicRsts PruneCalled RcvPruned OfoPruned ListenOverflows ListenDrops
TcpExt: 0 0 0 0 0 0 0 12 18
IpExt: InNoRoutes InTruncatedPkts
IpExt: 0 0`

const sampleProcNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 12345    6789    0    0    0    0    0          0         12345    6789    0    0    0    0    0       0
  eth0: 1234567  89012   5    2    0    0    0          100       9876543  21098   3    0    0    0    0       0
docker0: 50000   400     0    0    0    0    0          0         60000    500     0    0    0    0    0       0`

const sampleSsSummary = `Total: 318
TCP:   178 (estab 142, closed 0, orphaned 0, timewait 0)

Transport Total     IP        IPv6
RAW       0         0         0
UDP       9         8         1
TCP       178       128       50
INET      187       136       51
FRAG      0         0         0`

const sampleIpLink = `1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN mode DEFAULT
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
    RX:  bytes  packets  errors  dropped overrun mcast
        12345     6789      0       0       0       0
    TX:  bytes  packets  errors  dropped carrier collsns
        12345     6789      0       0       0       0
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP mode DEFAULT
    link/ether 00:11:22:33:44:55 brd ff:ff:ff:ff:ff:ff
    RX:  bytes  packets  errors  dropped overrun mcast
        1234567 89012     5       2       0       100
    TX:  bytes  packets  errors  dropped carrier collsns
        9876543 21098     0       0       0       0`

const sampleDmesgLinkFlap = `[Tue Oct  5 14:00:00 2024] e1000e 0000:00:1f.6 eth0: NIC Link is Down
[Tue Oct  5 14:00:01 2024] e1000e 0000:00:1f.6 eth0: NIC Link is Up 1000 Mbps Full Duplex
[Tue Oct  5 14:00:30 2024] e1000e 0000:00:1f.6 eth0: NIC Link is Down
[Tue Oct  5 14:00:31 2024] e1000e 0000:00:1f.6 eth0: NIC Link is Up 1000 Mbps Full Duplex
unrelated noise`

// ----- sar -n DEV parsing -----

func TestParseSarDevTable(t *testing.T) {
	rows := parseSarTable(sampleSarDev, "rxkB/s", "txkB/s")
	if len(rows) != 4 {
		t.Fatalf("expected 4 data rows (Average: skipped), got %d", len(rows))
	}
	for _, r := range rows {
		if r["IFACE"] != "lo" && r["IFACE"] != "eth0" {
			t.Errorf("unexpected iface in row: %v", r)
		}
	}
}

func TestParseSarTableRejectsWrongTable(t *testing.T) {
	// Asking for rxdrop/s when given a DEV table → no rows returned
	if rows := parseSarTable(sampleSarDev, "rxdrop/s"); len(rows) != 0 {
		t.Errorf("expected 0 rows when columns don't match table, got %d", len(rows))
	}
}

func TestExtractSarRxPeak(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "sar -n DEV 1 2", Output: sampleSarDev}}
	v, ok := extractSarNetPeak("rxkB/s")(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	// Peak across non-loopback rows: 4096.00 from second eth0 sample
	if v.Number != 4096.00 {
		t.Errorf("got %v, want 4096.00", v.Number)
	}
}

func TestExtractSarRxPeakSkipsLoopback(t *testing.T) {
	loopbackOnly := `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (4 CPU)

14:00:01        IFACE   rxpck/s   txpck/s    rxkB/s    txkB/s   rxcmp/s   txcmp/s  rxmcst/s   %ifutil
14:00:02           lo  10000.00  10000.00  99999.00  99999.00      0.00      0.00      0.00      0.00`
	caps := []CapturedCommand{{Cmd: "sar -n DEV 1 1", Output: loopbackOnly}}
	if _, ok := extractSarNetPeak("rxkB/s")(SystemInfo{}, caps); ok {
		t.Error("expected extraction to fail when only loopback rows are present")
	}
}

func TestExtractSarRxPeakRequiresSarBaseCmd(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "vim sar.txt", Output: sampleSarDev}}
	if _, ok := extractSarNetPeak("rxkB/s")(SystemInfo{}, caps); ok {
		t.Error("expected vim sar.txt to be rejected")
	}
}

// ----- sar -n EDEV parsing -----

func TestExtractSarRxDropsPeak(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "sar -n EDEV 1 2", Output: sampleSarEdev}}
	v, ok := extractSarEdevPeak("rxdrop/s")(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 12.50 {
		t.Errorf("got %v, want 12.50", v.Number)
	}
}

func TestSarDevQuestionsFires(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "sar -n DEV 1 2", Output: sampleSarDev}
	qs := sarDevQuestions(si, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for sar -n DEV output")
	}
}

func TestSarEdevQuestionsFires(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "sar -n EDEV 1 2", Output: sampleSarEdev}
	qs := sarEdevQuestions(si, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for sar -n EDEV output")
	}
}

// ----- /proc/net/snmp -----

func TestParseSnmpTcp(t *testing.T) {
	tcp := parseSnmpTcp(sampleSnmpTcp)
	want := map[string]float64{
		"CurrEstab":   142,
		"InSegs":      1234567,
		"OutSegs":     1000000,
		"RetransSegs": 5000,
	}
	for k, v := range want {
		if tcp[k] != v {
			t.Errorf("Tcp[%q]: got %v, want %v", k, tcp[k], v)
		}
	}
}

func TestExtractTCPEstab(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/net/snmp", Output: sampleSnmpTcp}}
	v, ok := extractTCPEstab(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 142 {
		t.Errorf("got %v, want 142", v.Number)
	}
}

func TestExtractTCPRetransmitRatio(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/net/snmp", Output: sampleSnmpTcp}}
	v, ok := extractTCPRetransmitRatio(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	// 5000 / 1000000 = 0.5%
	if v.Number != 0.5 {
		t.Errorf("got %v, want 0.5", v.Number)
	}
}

func TestExtractTCPEstabRequiresSnmpPath(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /etc/something", Output: sampleSnmpTcp}}
	if _, ok := extractTCPEstab(SystemInfo{}, caps); ok {
		t.Error("expected extraction to ignore non-/proc/net/snmp captures")
	}
}

// ----- /proc/net/netstat -----

func TestParseNetstatExtTcp(t *testing.T) {
	ext := parseNetstatExtTcp(sampleNetstatExt)
	if ext["ListenOverflows"] != 12 {
		t.Errorf("ListenOverflows: got %v, want 12", ext["ListenOverflows"])
	}
	if ext["ListenDrops"] != 18 {
		t.Errorf("ListenDrops: got %v, want 18", ext["ListenDrops"])
	}
}

func TestExtractListenOverflows(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/net/netstat", Output: sampleNetstatExt}}
	v, ok := extractListenOverflows(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 12 {
		t.Errorf("got %v, want 12", v.Number)
	}
	if !strings.Contains(v.Note, "ListenDrops=18") {
		t.Errorf("expected ListenDrops in note, got %q", v.Note)
	}
}

// ----- /proc/net/dev -----

func TestExtractInterfaceErrors(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/net/dev", Output: sampleProcNetDev}}
	v, ok := extractInterfaceErrors(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	// eth0: rxErr=5 + txErr=3 = 8; lo skipped; docker0: 0+0=0. Total = 8
	if v.Number != 8 {
		t.Errorf("got %v, want 8", v.Number)
	}
}

// ----- comprehension extractors -----

func TestIPLinkQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "ip -s link", Output: sampleIpLink}
	if qs := ipLinkQuestions(SystemInfo{}, c); len(qs) == 0 {
		t.Fatal("expected questions for ip -s link output")
	}
}

func TestSnmpQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/net/snmp", Output: sampleSnmpTcp}
	if qs := snmpQuestions(SystemInfo{}, c); len(qs) == 0 {
		t.Fatal("expected snmp questions")
	}
}

func TestNetstatExtQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/net/netstat", Output: sampleNetstatExt}
	if qs := netstatExtQuestions(SystemInfo{}, c); len(qs) == 0 {
		t.Fatal("expected netstat-ext questions")
	}
}

func TestSsSummaryQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "ss -s", Output: sampleSsSummary}
	if qs := ssSummaryQuestions(SystemInfo{}, c); len(qs) == 0 {
		t.Fatal("expected ss summary questions")
	}
}

func TestNetworkDmesgQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "dmesg", Output: sampleDmesgLinkFlap}
	if qs := networkDmesgQuestions(SystemInfo{}, c); len(qs) == 0 {
		t.Fatal("expected network dmesg questions for link-flap output")
	}
}

func TestNetworkDmesgQuestionsSkipsBenign(t *testing.T) {
	c := CapturedCommand{Cmd: "dmesg", Output: "[Tue Oct 5] kernel boot complete"}
	if qs := networkDmesgQuestions(SystemInfo{}, c); qs != nil {
		t.Error("expected no questions for benign dmesg output")
	}
}

func TestExtractDmesgNetKeywordsCounts(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "dmesg", Output: sampleDmesgLinkFlap}}
	v, ok := extractDmesgNetKeywords(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	// 4 of 5 lines mention NIC Link / Link is
	if !strings.Contains(v.Text, "4/5") {
		t.Errorf("expected 4/5 in text, got %q", v.Text)
	}
}

func TestExtractDmesgNetKeywordsRequiresDmesgBaseCmd(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "vim dmesg.txt", Output: sampleDmesgLinkFlap}}
	if _, ok := extractDmesgNetKeywords(SystemInfo{}, caps); ok {
		t.Error("expected vim dmesg.txt to be rejected")
	}
}

// ----- synthesis rules -----

func TestBandwidthLossHealthy(t *testing.T) {
	vs := map[string]Value{
		"net_rx_drops_per_sec_max": {Number: 0},
		"tcp_retransmit_ratio_pct": {Number: 0.1},
	}
	q, ok := bandwidthLossConsistency.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Healthy") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestBandwidthLossDownstream(t *testing.T) {
	vs := map[string]Value{
		"net_rx_drops_per_sec_max": {Number: 0},
		"tcp_retransmit_ratio_pct": {Number: 2.5},
	}
	q, ok := bandwidthLossConsistency.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "downstream") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestBandwidthLossLocalDrops(t *testing.T) {
	vs := map[string]Value{
		"net_rx_drops_per_sec_max": {Number: 12},
		"tcp_retransmit_ratio_pct": {Number: 1.5},
	}
	q, ok := bandwidthLossConsistency.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Local interface is dropping") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestBandwidthLossAmbiguousReturnsFalse(t *testing.T) {
	// Drops borderline (between 1 and 5), retransmits borderline (between 0.5 and 1)
	vs := map[string]Value{
		"net_rx_drops_per_sec_max": {Number: 2.0},
		"tcp_retransmit_ratio_pct": {Number: 0.7},
	}
	if _, ok := bandwidthLossConsistency.Generate(SystemInfo{}, vs); ok {
		t.Error("expected synthesis to skip ambiguous middle case")
	}
}

func TestListenQueueAttributionAppSide(t *testing.T) {
	vs := map[string]Value{
		"tcp_listen_overflows":     {Number: 50},
		"tcp_retransmit_ratio_pct": {Number: 0.2},
		"net_rx_drops_per_sec_max": {Number: 0},
	}
	q, ok := listenQueueAttribution.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Application-side") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestListenQueueAttributionBothLayers(t *testing.T) {
	vs := map[string]Value{
		"tcp_listen_overflows":     {Number: 50},
		"tcp_retransmit_ratio_pct": {Number: 2.0},
		"net_rx_drops_per_sec_max": {Number: 0},
	}
	q, ok := listenQueueAttribution.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Both layers") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestListenQueueAttributionNoOverflowsSkips(t *testing.T) {
	// No overflows at all → rule should skip (not interesting case)
	vs := map[string]Value{
		"tcp_listen_overflows":     {Number: 0},
		"tcp_retransmit_ratio_pct": {Number: 2.0},
		"net_rx_drops_per_sec_max": {Number: 0},
	}
	if _, ok := listenQueueAttribution.Generate(SystemInfo{}, vs); ok {
		t.Error("expected synthesis to skip when there are no listen overflows")
	}
}

func TestNetworkInvestigationRegistered(t *testing.T) {
	inv, err := getInvestigation("network")
	if err != nil {
		t.Fatalf("network investigation not registered: %v", err)
	}
	if inv.Name != "network" {
		t.Errorf("unexpected name %q", inv.Name)
	}
	if len(inv.Observations) == 0 || len(inv.Commands) == 0 || len(inv.Extractors) == 0 {
		t.Error("network investigation is missing observations/commands/extractors")
	}
}

// ----- recall edge cases & randomization coverage -----

func TestNetRxDropsRecallAtZero(t *testing.T) {
	// Quiet network — zero drops. Original implementation included literal
	// "0.0" in the distractor list, colliding with correct. Helper must dedupe.
	v := Value{Number: 0}
	qs := netRxDropsRecall(v)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question at zero, got %d", len(qs))
	}
	q := qs[0]
	if q.Correct != "0.0" {
		t.Errorf("correct: got %q, want 0.0", q.Correct)
	}
	for _, d := range q.Distractors {
		if d == q.Correct {
			t.Errorf("distractor matches correct: %v", q.Distractors)
		}
	}
}

func TestSarDevQuestionsCoversBothDirections(t *testing.T) {
	c := CapturedCommand{Cmd: "sar -n DEV 1 2", Output: sampleSarDev}
	seen := map[string]bool{}
	wantCorrect := map[string]string{
		"rxkB/s": "Receive throughput in kilobytes per second, averaged over the sample interval",
		"txkB/s": "Transmit throughput in kilobytes per second, averaged over the sample interval",
	}
	for i := 0; i < 100; i++ {
		qs := sarDevQuestions(SystemInfo{}, c)
		if len(qs) < 1 {
			t.Fatalf("iteration %d: expected at least 1 question", i)
		}
		column := ""
		for candidate := range wantCorrect {
			if strings.Contains(qs[0].Stem, candidate) {
				column = candidate
				break
			}
		}
		if column == "" {
			t.Fatalf("iteration %d: stem does not ask about rx/tx throughput: %q", i, qs[0].Stem)
		}
		if qs[0].Correct != wantCorrect[column] {
			t.Fatalf("iteration %d: column %q had correct answer %q, want %q", i, column, qs[0].Correct, wantCorrect[column])
		}
		seen[column] = true
	}
	for _, want := range []string{"rxkB/s", "txkB/s"} {
		if !seen[want] {
			t.Errorf("column %q never appeared across 100 iterations; saw %v", want, seen)
		}
	}
}

func TestSarEdevQuestionsCoversBothDirections(t *testing.T) {
	c := CapturedCommand{Cmd: "sar -n EDEV 1 2", Output: sampleSarEdev}
	seen := map[string]bool{}
	wantCorrect := map[string]string{
		"rxdrop/s": "The NIC ring buffer or kernel queue is filling faster than the system can drain incoming packets (can be tuned with `ethtool -G`)",
		"txdrop/s": "The kernel transmit queue (qdisc) is dropping outgoing packets, often because tx_queue_len is too small for offered load",
	}
	for i := 0; i < 100; i++ {
		qs := sarEdevQuestions(SystemInfo{}, c)
		if len(qs) != 1 {
			t.Fatalf("iteration %d: expected 1 question", i)
		}
		column := ""
		for candidate := range wantCorrect {
			if strings.Contains(qs[0].Stem, candidate) {
				column = candidate
				break
			}
		}
		if column == "" {
			t.Fatalf("iteration %d: stem does not ask about rx/tx drops: %q", i, qs[0].Stem)
		}
		if qs[0].Correct != wantCorrect[column] {
			t.Fatalf("iteration %d: column %q had correct answer %q, want %q", i, column, qs[0].Correct, wantCorrect[column])
		}
		seen[column] = true
	}
	for _, want := range []string{"rxdrop/s", "txdrop/s"} {
		if !seen[want] {
			t.Errorf("column %q never appeared across 100 iterations; saw %v", want, seen)
		}
	}
}
