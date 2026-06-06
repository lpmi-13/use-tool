package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// diagnose is the practice-mode assessment of the USE method itself: the
// learner states a verdict for each USE dimension and cites supporting
// evidence from what they observed this session. Grading checks the cited
// evidence against the USE heuristics applied to the observed values — never
// against an absolute "this system is saturated" label — so the tool stays
// system-agnostic while still assessing diagnostic judgement.

// useDimensions is the order USE dimensions are assessed in.
var useDimensions = []string{"Utilization", "Saturation", "Errors"}

var selectorTerminalWidth = detectTerminalWidth

// claimOptions are the verdicts a learner may state per dimension. The empty
// string is the internal value for "not enough data".
func claimOptions(dim string) []string {
	if dim == "Utilization" {
		return []string{"high", "moderate", "low", "not enough data"}
	}
	return []string{"present", "absent", "not enough data"}
}

// claimRank ranks a verdict on a 1 (low/absent) .. 3 (high/present) scale.
// 0 means "not enough data" (no evidence step applies).
func claimRank(claim string) int {
	switch claim {
	case "low", "absent":
		return 1
	case "moderate":
		return 2
	case "high", "present":
		return 3
	default:
		return 0
	}
}

// signalRank ranks a Signal on the same 1..3 scale; 0 means no reading.
func signalRank(s Signal) int {
	switch s {
	case SignalLow:
		return 1
	case SignalModerate:
		return 2
	case SignalHigh:
		return 3
	default:
		return 0
	}
}

// labelForSignal renders what the data reads as, in the vocabulary of the
// given dimension ("high"/"moderate"/"low" for Utilization, "present"/"absent"
// otherwise).
func labelForSignal(dim string, s Signal) string {
	if dim == "Utilization" {
		switch s {
		case SignalHigh:
			return "high"
		case SignalModerate:
			return "moderate"
		case SignalLow:
			return "low"
		}
		return "no reading"
	}
	switch s {
	case SignalHigh:
		return "present"
	case SignalLow, SignalModerate:
		return "absent"
	}
	return "no reading"
}

type citationVerdict int

const (
	citeSupports citationVerdict = iota
	citeContradicts
	citeWrongDimension
	citeNoSignal
)

type citedEvidence struct {
	Name      string
	Title     string
	Verdict   citationVerdict
	Reads     Signal
	Heuristic string
	Value     Value
	HasValue  bool
}

type dimensionGrade struct {
	Resource     string // empty in single-resource diagnose; "CPU"/"Memory"/... in system mode
	Dimension    string
	Claim        string // verdict label, or "" for "not enough data"
	HasData      bool   // any captured verdict-bearing observation in this dimension
	DataReads    Signal // strongest reading among this dimension's captured signals
	Accurate     bool   // claim agrees with what the data reads
	Cited        []citedEvidence
	Uncited      []citedEvidence
	NextCommands []CommandRef
	Supports     int
	Contradicts  int
}

// classifyCitation grades a single cited observation against a claim.
func classifyCitation(si SystemInfo, snap Snapshot, dim, claim string, obs Observation, v Value) citationVerdict {
	if obs.Section != dim {
		return citeWrongDimension
	}
	if obs.Verdict == nil {
		return citeNoSignal
	}
	sr := signalRank(obs.Verdict(si, v, snap))
	if sr == 0 {
		return citeNoSignal
	}
	cr := claimRank(claim)
	switch {
	case cr >= 2 && sr == 1:
		// claim says elevated, signal reads low
		return citeContradicts
	case cr == 1 && sr >= 3:
		// claim says low/absent, signal reads high
		return citeContradicts
	default:
		return citeSupports
	}
}

// gradeDimension grades one dimension's claim and cited evidence. byName and
// values come from the session snapshot; citedNames are the observation Names
// the learner selected as supporting evidence.
func gradeDimension(si SystemInfo, snap Snapshot, dimObs []Observation, dim, claim string, citedNames []string) dimensionGrade {
	g := dimensionGrade{Dimension: dim, Claim: claim}

	// What the captured signals for this dimension actually read.
	for _, obs := range dimObs {
		if obs.Section != dim || obs.Verdict == nil {
			continue
		}
		v, ok := snap.Values[obs.Name]
		if !ok {
			continue
		}
		g.HasData = true
		if s := obs.Verdict(si, v, snap); signalRank(s) > signalRank(g.DataReads) {
			g.DataReads = s
		}
	}

	byName := map[string]Observation{}
	for _, obs := range dimObs {
		byName[obs.Name] = obs
	}
	cited := map[string]bool{}
	for _, name := range citedNames {
		cited[name] = true
		obs, ok := byName[name]
		if !ok {
			continue
		}
		v, hasValue := snap.Values[obs.Name]
		cv := classifyCitation(si, snap, dim, claim, obs, v)
		var reads Signal
		if obs.Verdict != nil {
			reads = obs.Verdict(si, v, snap)
		}
		g.Cited = append(g.Cited, citedEvidence{
			Name: obs.Name, Title: obs.Title, Verdict: cv, Reads: reads, Heuristic: obs.Heuristic,
			Value: v, HasValue: hasValue,
		})
		switch cv {
		case citeSupports:
			g.Supports++
		case citeContradicts:
			g.Contradicts++
		}
	}
	if claim != "" {
		for _, obs := range dimObs {
			if cited[obs.Name] || obs.Section != dim || obs.Verdict == nil {
				continue
			}
			v, ok := snap.Values[obs.Name]
			if !ok {
				continue
			}
			reads := obs.Verdict(si, v, snap)
			if signalRank(reads) == 0 {
				continue
			}
			g.Uncited = append(g.Uncited, citedEvidence{
				Name: obs.Name, Title: obs.Title, Verdict: classifyCitation(si, snap, dim, claim, obs, v),
				Reads: reads, Heuristic: obs.Heuristic, Value: v, HasValue: true,
			})
		}
	}

	// Accuracy: does the stated verdict agree with what the data reads?
	if claim == "" { // not enough data
		g.Accurate = !g.HasData
	} else if g.HasData {
		g.Accurate = labelForSignal(dim, g.DataReads) == claim ||
			// treat moderate-vs-high as agreement on utilization (grey zone)
			(dim == "Utilization" && claimRank(claim) >= 2 && signalRank(g.DataReads) >= 2)
	}
	return g
}

// assessment summarises the evidence support for a real claim.
func (g dimensionGrade) assessment() string {
	switch {
	case g.Supports >= 2 && g.Contradicts == 0:
		return "well supported"
	case g.Supports == 1 && g.Contradicts == 0:
		return "supported, but thin — strong claims want a second, independent signal"
	case g.Supports >= 1 && g.Contradicts > 0:
		return "mixed — some cited evidence points the other way"
	case g.Supports == 0 && g.Contradicts > 0:
		return "not supported — your evidence reads the opposite way"
	default:
		return "no valid evidence cited"
	}
}

// ----- interactive flow -----

func practiceDiagnose(s *Session) bool {
	snap := s.Snapshot()
	byResource := candidatesByResource(s.Investigation, snap)
	resources := resourcesWithSignals(byResource)
	if len(resources) == 0 {
		fmt.Println("\nNo USE-relevant signals captured yet. Run some commands first")
		fmt.Println("(try `commands` for the cheatsheet), then `diagnose` again.")
		return false
	}

	fmt.Printf("\n=== Diagnose: %s ===\n", s.Investigation.Title)
	multiResource := len(resources) > 1

	var grades []dimensionGrade
	for _, resource := range resources {
		candidates := byResource[resource]
		// Per-resource header when iterating more than one; otherwise the
		// session title alone is the header (preserves single-resource UX).
		if multiResource {
			fmt.Printf("\n--- %s ---\n", resource)
			fmt.Printf("You've observed these %s signals this session:\n", resource)
		} else {
			fmt.Println("Signals you've observed this session:")
		}
		for i, obs := range candidates {
			fmt.Printf("  [%d] %s = %s\n", i+1, obs.Title, formatValue(snap.Values[obs.Name]))
		}
		if !multiResource {
			fmt.Println("\nFor each USE dimension, give your verdict, then cite the evidence")
			fmt.Println("that supports it (the bracketed numbers above, comma-separated; `none` if none).")
		}

		// Look up the per-resource investigation so DiagnoseNotes fire only at
		// the prompt for the resource they belong to (system mode).
		notesInv := s.Investigation
		if s.Investigation.Name == "system" {
			if inv := investigationForResource(resource); inv != nil {
				notesInv = inv
			}
		}

		for _, dim := range useDimensions {
			dimCands := filterBySection(candidates, dim)
			if len(dimCands) == 0 {
				// No captured signal for this (resource, dim) — auto-skip
				// rather than force the learner through a trivial
				// "not enough data" answer. Carry any DiagnoseNote forward
				// (e.g. Network Utilization's inferrability caveat) so the
				// learner still sees *why* the dimension is skipped.
				skipMsg := fmt.Sprintf("\n%s — no captured signal, skipping.", labelFor(resource, dim, multiResource))
				if note := notesInv.DiagnoseNotes[dim]; note != "" {
					skipMsg += " " + note
				}
				fmt.Println(skipMsg)
				continue
			}
			claim, quit := askClaim(labelFor(resource, dim, multiResource), dim, notesInv.DiagnoseNotes[dim])
			if quit {
				return true
			}
			var cited []string
			if claim != "" {
				names, quit := askEvidence(candidates, snap)
				if quit {
					return true
				}
				cited = names
			}
			g := gradeDimension(s.System, snap, candidates, dim, claim, cited)
			g.Resource = resource
			if claim != "" && g.Supports < 2 && supportingUncitedCount(g) == 0 {
				g.NextCommands = suggestNextCommands(notesInv, dim, s.Captured, s.System, 2)
			}
			grades = append(grades, g)
		}
	}

	printDiagnoseFeedback(grades, multiResource)
	return true
}

// candidatesByResource buckets the captured verdict-bearing observations by
// their Resource label. Within each bucket, ordering follows USE sections
// (Utilization → Saturation → Errors). Resource labels are deliberately
// omitted from the per-prompt candidate list so that citing the right *kind*
// of signal for a dimension remains part of the assessed skill.
func candidatesByResource(inv *Investigation, snap Snapshot) map[string][]Observation {
	bucket := map[string]map[string][]Observation{}
	for _, obs := range inv.Observations {
		if obs.Verdict == nil {
			continue
		}
		if _, ok := snap.Values[obs.Name]; !ok {
			continue
		}
		if bucket[obs.Resource] == nil {
			bucket[obs.Resource] = map[string][]Observation{}
		}
		bucket[obs.Resource][obs.Section] = append(bucket[obs.Resource][obs.Section], obs)
	}
	out := map[string][]Observation{}
	for resource, sections := range bucket {
		var flat []Observation
		for _, dim := range useDimensions {
			flat = append(flat, sections[dim]...)
		}
		out[resource] = flat
	}
	return out
}

// resourcesWithSignals returns the resources that have at least one captured
// verdict-bearing observation, in canonical USE walk order.
func resourcesWithSignals(byResource map[string][]Observation) []string {
	var out []string
	for _, r := range resourceOrder {
		if len(byResource[r]) > 0 {
			out = append(out, r)
		}
	}
	// Fall back to map iteration for unknown resource labels (defensive).
	if len(out) == 0 {
		for r, obs := range byResource {
			if len(obs) > 0 {
				out = append(out, r)
			}
		}
		sort.Strings(out)
	}
	return out
}

func filterBySection(obs []Observation, section string) []Observation {
	var out []Observation
	for _, o := range obs {
		if o.Section == section {
			out = append(out, o)
		}
	}
	return out
}

func labelFor(resource, dim string, multiResource bool) string {
	if multiResource {
		return resource + " " + dim
	}
	return dim
}

func askClaim(label, dim, note string) (claim string, quit bool) {
	opts := claimOptions(dim)
	if n, quit, ok := chooseClaimWithKeys(label, note, opts); ok {
		if quit {
			return "", true
		}
		chosen := opts[n]
		if chosen == "not enough data" {
			return "", false
		}
		return chosen, false
	}
	for {
		fmt.Printf("\n%s — your verdict?\n", label)
		if note != "" {
			fmt.Printf("  Note: %s\n", note)
		}
		for i, o := range opts {
			fmt.Printf("  %d. %s\n", i+1, o)
		}
		fmt.Print("Choice: ")
		line, ok := readLine()
		if !ok || isExitCommand(line) {
			return "", true
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(opts) {
			fmt.Printf("Pick a number 1-%d.\n", len(opts))
			continue
		}
		chosen := opts[n-1]
		if chosen == "not enough data" {
			return "", false
		}
		return chosen, false
	}
}

func askEvidence(candidates []Observation, snap Snapshot) (names []string, quit bool) {
	if idxs, quit, ok := chooseEvidenceWithKeys(candidates, snap); ok {
		if quit {
			return nil, true
		}
		for _, i := range idxs {
			names = append(names, candidates[i].Name)
		}
		return names, false
	}
	for {
		fmt.Print("Evidence: ")
		line, ok := readLine()
		if !ok || isExitCommand(line) {
			return nil, true
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "none") {
			return nil, false
		}
		idxs, err := parseIndexList(line, len(candidates))
		if err != nil {
			fmt.Printf("Enter numbers 1-%d separated by commas, or `none`.\n", len(candidates))
			continue
		}
		for _, i := range idxs {
			names = append(names, candidates[i-1].Name)
		}
		return names, false
	}
}

func chooseClaimWithKeys(label, note string, opts []string) (selected int, quit bool, ok bool) {
	restore, ok := enterRawInput()
	if !ok {
		return 0, false, false
	}
	defer restore()

	selected = 0
	showHelp := false
	fmt.Println()
	renderedLines := renderClaimSelector(label, note, opts, selected, showHelp)
	for {
		key, err := readTerminalKey()
		if err != nil {
			fmt.Println()
			return 0, true, true
		}
		switch key.Key {
		case keyUp, keyDown:
			selected = moveSelection(selected, len(opts), key.Key)
			clearRenderedBlock(renderedLines)
			renderedLines = renderClaimSelector(label, note, opts, selected, showHelp)
		case keyDigit:
			if key.Digit >= 1 && key.Digit <= len(opts) {
				selected = key.Digit - 1
				clearRenderedBlock(renderedLines)
				renderedLines = renderClaimSelector(label, note, opts, selected, showHelp)
				continue
			}
			fmt.Print("\a")
		case keyHelp:
			showHelp = !showHelp
			clearRenderedBlock(renderedLines)
			renderedLines = renderClaimSelector(label, note, opts, selected, showHelp)
		case keyRedraw:
			renderedLines = redrawFullScreen(func() int {
				return renderClaimSelector(label, note, opts, selected, showHelp)
			})
		case keyEnter:
			fmt.Println()
			return selected, false, true
		case keyQuit:
			fmt.Println()
			return 0, true, true
		default:
			fmt.Print("\a")
		}
	}
}

func chooseEvidenceWithKeys(candidates []Observation, snap Snapshot) (idxs []int, quit bool, ok bool) {
	restore, ok := enterRawInput()
	if !ok {
		return nil, false, false
	}
	defer restore()

	selected := 0
	checked := make([]bool, len(candidates))
	typedListMode := false
	showHelp := false
	fmt.Println()
	renderedLines := renderEvidenceSelector(candidates, snap, selected, checked, showHelp)
	for {
		key, err := readTerminalKey()
		if err != nil {
			fmt.Println()
			return nil, true, true
		}
		switch key.Key {
		case keyUp, keyDown:
			typedListMode = false
			selected = moveSelection(selected, len(candidates), key.Key)
			clearRenderedBlock(renderedLines)
			renderedLines = renderEvidenceSelector(candidates, snap, selected, checked, showHelp)
		case keySpace:
			if typedListMode {
				continue
			}
			checked[selected] = !checked[selected]
			clearRenderedBlock(renderedLines)
			renderedLines = renderEvidenceSelector(candidates, snap, selected, checked, showHelp)
		case keyDigit:
			if key.Digit >= 1 && key.Digit <= len(candidates) {
				typedListMode = true
				checked[key.Digit-1] = !checked[key.Digit-1]
				selected = key.Digit - 1
				clearRenderedBlock(renderedLines)
				renderedLines = renderEvidenceSelector(candidates, snap, selected, checked, showHelp)
				continue
			}
			fmt.Print("\a")
		case keyClear:
			typedListMode = false
			for i := range checked {
				checked[i] = false
			}
			clearRenderedBlock(renderedLines)
			renderedLines = renderEvidenceSelector(candidates, snap, selected, checked, showHelp)
		case keyHelp:
			typedListMode = false
			showHelp = !showHelp
			clearRenderedBlock(renderedLines)
			renderedLines = renderEvidenceSelector(candidates, snap, selected, checked, showHelp)
		case keyRedraw:
			typedListMode = false
			renderedLines = redrawFullScreen(func() int {
				return renderEvidenceSelector(candidates, snap, selected, checked, showHelp)
			})
		case keySeparator:
			typedListMode = true
		case keyEnter:
			for i, on := range checked {
				if on {
					idxs = append(idxs, i)
				}
			}
			fmt.Println()
			return idxs, false, true
		case keyQuit:
			fmt.Println()
			return nil, true, true
		default:
			fmt.Print("\a")
		}
	}
}

func redrawFullScreen(render func() int) int {
	fmt.Print("\x1b[H\x1b[2J")
	return render()
}

func clearRenderedBlock(lines int) {
	if lines <= 0 {
		return
	}
	fmt.Print("\r\x1b[2K")
	for i := 1; i < lines; i++ {
		fmt.Print("\x1b[1A\x1b[2K")
	}
}

func moveSelection(selected, count int, key terminalKey) int {
	if count <= 0 {
		return 0
	}
	switch key {
	case keyUp:
		return (selected + count - 1) % count
	case keyDown:
		return (selected + 1) % count
	default:
		return selected
	}
}

func renderClaimSelector(label, note string, opts []string, selected int, showHelp bool) int {
	width := selectorTerminalWidth()
	lines := printSelectorLine(fmt.Sprintf("%s — your verdict?", label), width, true)
	if note != "" {
		lines += printSelectorLine("  Note: "+note, width, true)
	}
	for i, o := range opts {
		cursor := " "
		if i == selected {
			cursor = ">"
		}
		lines += printSelectorLine(fmt.Sprintf("%s %d. %s", cursor, i+1, o), width, true)
	}
	return lines + printSelectorHelp(claimSelectorHelp(len(opts), showHelp), width)
}

func renderEvidenceSelector(candidates []Observation, snap Snapshot, selected int, checked []bool, showHelp bool) int {
	width := selectorTerminalWidth()
	lines := printSelectorLine("Evidence — choose supporting signals", width, true)
	for i, obs := range candidates {
		cursor := " "
		if i == selected {
			cursor = ">"
		}
		box := " "
		if i < len(checked) && checked[i] {
			box = "x"
		}
		line := fmt.Sprintf("%s [%s] [%d] %s = %s", cursor, box, i+1, obs.Title, formatValue(snap.Values[obs.Name]))
		lines += printSelectorLine(line, width, true)
	}
	return lines + printSelectorHelp(evidenceSelectorHelp(len(candidates), showHelp), width)
}

func claimSelectorHelp(optionCount int, showHelp bool) []string {
	if !showHelp {
		return []string{fmt.Sprintf("↑/k ↓/j move | 1-%d jump | Enter choose | q quit | ? help", optionCount)}
	}
	return []string{
		"Keys:",
		"  ↑/k, ↓/j  move between verdicts",
		fmt.Sprintf("  1-%d       jump to a verdict", optionCount),
		"  Enter     choose the highlighted verdict",
		"  q         quit diagnose",
		"  ?         hide help",
	}
}

func evidenceSelectorHelp(candidateCount int, showHelp bool) []string {
	if !showHelp {
		return []string{fmt.Sprintf("↑/k ↓/j move | Space/1-%d toggle | n clear | Enter submit | q quit | ? help", candidateCount)}
	}
	return []string{
		"Keys:",
		"  ↑/k, ↓/j  move between signals",
		fmt.Sprintf("  Space     toggle highlighted signal; 1-%d toggles by number", candidateCount),
		"  n         clear all selected signals",
		"  Enter     submit selected signals; submit none selected for `none`",
		"  q         quit diagnose",
		"  ?         hide help",
	}
}

func printSelectorHelp(lines []string, width int) int {
	rows := 0
	for i, line := range lines {
		rows += printSelectorLine(line, width, i != len(lines)-1)
	}
	return rows
}

func printSelectorLine(line string, width int, newline bool) int {
	if newline {
		fmt.Println(line)
	} else {
		fmt.Print(line)
	}
	return visualLineRows(line, width)
}

func visualLineRows(line string, width int) int {
	if width <= 0 {
		width = 80
	}
	cols := displayColumns(line)
	if cols == 0 {
		return 1
	}
	return (cols + width - 1) / width
}

func displayColumns(s string) int {
	cols := 0
	for _, r := range s {
		if r == '\t' {
			cols += 4
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		cols++
	}
	return cols
}

func detectTerminalWidth() int {
	type winsize struct {
		row    uint16
		col    uint16
		xpixel uint16
		ypixel uint16
	}
	var ws winsize
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 || ws.col == 0 {
		return 80
	}
	return int(ws.col)
}

// parseIndexList parses "1, 3,4" into a sorted, de-duplicated []int, validating
// each is within 1..max.
func parseIndexList(s string, max int) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > max {
			return nil, fmt.Errorf("bad index %q", f)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no indices")
	}
	sort.Ints(out)
	return out, nil
}

func printDiagnoseFeedback(grades []dimensionGrade, multiResource bool) {
	fmt.Println("\n--- Diagnosis feedback ---")
	var prevResource string
	for _, g := range grades {
		// In multi-resource mode, restart the section per resource so the
		// learner sees a clear CPU / Memory / Disk / Network breakdown.
		if multiResource && g.Resource != prevResource {
			fmt.Printf("\n--- %s ---\n", g.Resource)
			prevResource = g.Resource
		}
		fmt.Println()
		header := g.Dimension
		if multiResource {
			header = g.Resource + " " + g.Dimension
		}
		if g.Claim == "" {
			fmt.Println(header)
			fmt.Println("  Verdict:    not enough data")
			fmt.Println()
			fmt.Println("  Note:")
			if g.HasData {
				printWrappedFeedback("    ", "Actually, you did capture a signal here. Re-run `report` and look again.")
			} else {
				printWrappedFeedback("    ", "Correct. No command this session produced data for this dimension.")
			}
			continue
		}

		fmt.Println(header)
		fmt.Printf("  Verdict:    %s\n", g.Claim)
		fmt.Printf("  Assessment: %s\n", g.assessment())
		fmt.Println()
		fmt.Println("  Evidence:")
		if len(g.Cited) == 0 {
			fmt.Println("    (none cited)")
		}
		for _, c := range g.Cited {
			fmt.Println()
			switch c.Verdict {
			case citeSupports:
				fmt.Printf("    ✓ %s\n", c.Title)
				printWrappedFeedback("      ", "Supports this verdict.")
				printEvidenceValue(c)
				printEvidenceHeuristic(c.Heuristic)
			case citeContradicts:
				fmt.Printf("    ✗ %s\n", c.Title)
				printWrappedFeedback("      ", fmt.Sprintf("Reads %q, which points the other way.", labelForSignal(g.Dimension, c.Reads)))
				printEvidenceValue(c)
				printEvidenceHeuristic(c.Heuristic)
			case citeWrongDimension:
				fmt.Printf("    ✗ %s\n", c.Title)
				printWrappedFeedback("      ", fmt.Sprintf("Wrong USE dimension: this is not a %s signal.", strings.ToLower(g.Dimension)))
				printEvidenceValue(c)
			case citeNoSignal:
				fmt.Printf("    – %s\n", c.Title)
				printWrappedFeedback("      ", "Carries no diagnostic reading.")
				printEvidenceValue(c)
				if c.Heuristic != "" {
					printEvidenceHeuristic(c.Heuristic)
				}
			}
		}
		if g.Supports < 2 {
			if printUncitedEvidenceHints(g) {
				fmt.Println()
			}
			if len(g.NextCommands) > 0 {
				printNextCommandHints(g.NextCommands)
			}
		}
		if g.HasData && !g.Accurate {
			fmt.Println()
			fmt.Println("  Note:")
			printWrappedFeedback("    ", fmt.Sprintf("Strongest %s signal reads %q.", strings.ToLower(g.Dimension), labelForSignal(g.Dimension, g.DataReads)))
		} else if !g.HasData {
			fmt.Println()
			fmt.Println("  Note:")
			printWrappedFeedback("    ", fmt.Sprintf("You claimed %q but captured no %s signal. This is closer to \"not enough data\".", g.Claim, strings.ToLower(g.Dimension)))
		}
	}
	fmt.Println()
}

func printUncitedEvidenceHints(g dimensionGrade) bool {
	var relevant []citedEvidence
	for _, c := range g.Uncited {
		if c.Verdict == citeSupports || c.Verdict == citeContradicts {
			relevant = append(relevant, c)
		}
	}
	if len(relevant) == 0 {
		return false
	}
	fmt.Println()
	fmt.Println("  Other relevant evidence you captured:")
	for _, c := range relevant {
		fmt.Printf("    • %s\n", c.Title)
		switch c.Verdict {
		case citeSupports:
			printWrappedFeedback("      ", fmt.Sprintf("Reads %q and would support this verdict.", labelForSignal(g.Dimension, c.Reads)))
		case citeContradicts:
			printWrappedFeedback("      ", fmt.Sprintf("Reads %q and would point against this verdict.", labelForSignal(g.Dimension, c.Reads)))
		}
	}
	return true
}

func printNextCommandHints(cmds []CommandRef) {
	fmt.Println()
	fmt.Println("  To gather more supporting evidence:")
	for _, c := range cmds {
		fmt.Printf("    • %s\n", c.Cmd)
		if summary := firstSummaryLine(c.Summary); summary != "" {
			printWrappedFeedback("      ", summary)
		}
	}
}

func supportingUncitedCount(g dimensionGrade) int {
	n := 0
	for _, c := range g.Uncited {
		if c.Verdict == citeSupports {
			n++
		}
	}
	return n
}

func suggestNextCommands(inv *Investigation, dim string, caps []CapturedCommand, si SystemInfo, limit int) []CommandRef {
	if inv == nil || limit <= 0 {
		return nil
	}
	refs := commandSuggestionOrder(inv, dim)
	var out []CommandRef
	for _, ref := range refs {
		if ref.Section != dim || commandStatus(ref, si) != "" || commandWasCaptured(ref, caps) {
			continue
		}
		out = append(out, ref)
		if len(out) >= limit {
			return out
		}
	}
	return out
}

func commandSuggestionOrder(inv *Investigation, dim string) []CommandRef {
	var ranked []CommandRef
	for _, ref := range inv.Commands {
		if ref.Section != dim {
			continue
		}
		if ref.DiagnoseRank > 0 {
			ranked = append(ranked, ref)
		}
	}
	if len(ranked) > 0 {
		sort.SliceStable(ranked, func(i, j int) bool {
			return ranked[i].DiagnoseRank < ranked[j].DiagnoseRank
		})
		return ranked
	}
	var out []CommandRef
	for _, ref := range inv.Commands {
		if ref.Section == dim {
			out = append(out, ref)
		}
	}
	return out
}

func commandWasCaptured(ref CommandRef, caps []CapturedCommand) bool {
	refKey := commandFamilyKey(ref.Cmd)
	for _, c := range caps {
		if commandFamilyKey(c.Cmd) == refKey {
			return true
		}
	}
	return false
}

func commandFamilyKey(cmd string) string {
	fields := commandFields(commandBeforePipe(cmd))
	if len(fields) == 0 {
		return ""
	}
	base := fields[0]
	if base == "sar" && len(fields) > 2 && fields[1] == "-n" {
		return base + " " + fields[1] + " " + fields[2]
	}
	if base == "cat" && len(fields) > 1 {
		return base + " " + fields[1]
	}
	if len(fields) > 1 && strings.HasPrefix(fields[1], "-") {
		return base + " " + fields[1]
	}
	return base
}

func commandBeforePipe(cmd string) string {
	return firstPipelineSegment(cmd)
}

func firstSummaryLine(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func printEvidenceValue(c citedEvidence) {
	if !c.HasValue {
		return
	}
	printWrappedFeedback("      ", "Observed: "+formatValue(c.Value))
}

func printEvidenceHeuristic(heuristic string) {
	if heuristic == "" {
		return
	}
	printWrappedFeedback("      ", "Why: "+heuristic)
}

func printWrappedFeedback(indent, text string) {
	for _, line := range wrapText(text, 86-len(indent)) {
		fmt.Println(indent + line)
	}
}

func wrapText(text string, width int) []string {
	if width < 20 {
		width = 20
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	lines = append(lines, line)
	return lines
}
