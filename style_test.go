package main

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// withUI forces a UI configuration for one test and restores plain output
// afterwards. Width is the terminal width the selectors and prose see.
func withUI(t *testing.T, cfg uiConfig, width int) {
	t.Helper()
	oldUI, oldWidth := ui, selectorTerminalWidth
	ui = cfg
	selectorTerminalWidth = func() int { return width }
	t.Cleanup(func() {
		ui = oldUI
		selectorTerminalWidth = oldWidth
	})
}

var richColorUI = uiConfig{color: true, rich: true, gutter: true}

func TestDetectUI(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	for _, tc := range []struct {
		name      string
		vars      map[string]string
		tty       bool
		flag      bool
		wantColor bool
		wantRich  bool
	}{
		{name: "terminal", tty: true, wantColor: true, wantRich: true},
		{name: "pipe", tty: false, wantColor: false, wantRich: false},
		{name: "NO_COLOR", vars: map[string]string{"NO_COLOR": "1"}, tty: true, wantColor: false, wantRich: true},
		{name: "dumb terminal", vars: map[string]string{"TERM": "dumb"}, tty: true, wantColor: false, wantRich: true},
		{name: "flag", tty: true, flag: true, wantColor: false, wantRich: true},
		{name: "always on a pipe", vars: map[string]string{"USE_TOOL_COLOR": "always"}, tty: false, wantColor: true},
		{name: "always beats NO_COLOR", vars: map[string]string{"USE_TOOL_COLOR": "always", "NO_COLOR": "1"}, tty: true, wantColor: true, wantRich: true},
		{name: "never", vars: map[string]string{"USE_TOOL_COLOR": "never"}, tty: true, wantColor: false, wantRich: true},
		{name: "flag beats always", vars: map[string]string{"USE_TOOL_COLOR": "always"}, tty: true, flag: true, wantColor: false, wantRich: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detectUI(env(tc.vars), tc.tty, tc.flag)
			if got.color != tc.wantColor || got.rich != tc.wantRich {
				t.Fatalf("detectUI() = %+v, want color=%t rich=%t", got, tc.wantColor, tc.wantRich)
			}
			if got.gutter {
				t.Fatal("the gutter is only enabled by interactive modes")
			}
		})
	}
}

func TestConfigureUIStripsNoColorFlag(t *testing.T) {
	old := ui
	defer func() { ui = old }()
	got := configureUI([]string{"guide", "--no-color", "cpu"})
	if strings.Join(got, " ") != "guide cpu" {
		t.Fatalf("configureUI() args = %q", got)
	}
	if ui.color {
		t.Fatal("--no-color left colour on")
	}
}

func TestStylesArePlainWithColorOff(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	for _, style := range []func(string) string{bold, faint, underline, reverse, accent, good, warn, bad} {
		if got := style("text"); got != "text" {
			t.Fatalf("style with colour off = %q, want plain text", got)
		}
	}
}

func TestVisibleWidthSkipsEscapes(t *testing.T) {
	withUI(t, richColorUI, 80)
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"plain", 5},
		{bold("bold"), 4},
		{bad("✗") + " Not quite", 11},
		{"\x1b]0;title\x07after", 5},
		{"a\tb", 6},
		{"", 0},
	} {
		if got := visibleWidth(tc.in); got != tc.want {
			t.Errorf("visibleWidth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestVisualLineRowsIgnoresColorCodes(t *testing.T) {
	withUI(t, richColorUI, 80)
	line := strings.Repeat(bold("x"), 80)
	if got := visualLineRows(line, 80); got != 1 {
		t.Fatalf("visualLineRows() = %d for 80 visible columns, want 1", got)
	}
}

func TestPadRightUsesVisibleWidth(t *testing.T) {
	withUI(t, richColorUI, 80)
	got := padRight(bold("ab"), 5)
	if visibleWidth(got) != 5 || !strings.HasSuffix(got, "   ") {
		t.Fatalf("padRight() = %q, want 5 visible columns", got)
	}
}

func TestWrapTextCountsRunesNotBytes(t *testing.T) {
	text := strings.Repeat("— ", 15) // 15 three-byte dashes
	if lines := wrapText(text, 30); len(lines) != 1 {
		t.Fatalf("wrapText() split %d visible columns into %d lines: %q", visibleWidth(text), len(lines), lines)
	}
}

func TestGutterizeLeavesBlankLinesBare(t *testing.T) {
	withUI(t, uiConfig{rich: true, gutter: true}, 80)
	got := gutterize("one\n\ntwo\n")
	if want := "│ one\n\n│ two\n"; got != want {
		t.Fatalf("gutterize() = %q, want %q", got, want)
	}
}

func TestGutterOffIsUnchanged(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	if got := gutterize("one\ntwo"); got != "one\ntwo" {
		t.Fatalf("gutterize() with gutter off = %q", got)
	}
}

func TestReflowProseJoinsLinesKeepsParagraphsAndBullets(t *testing.T) {
	text := "First line\ncontinues here.\n\nKey columns:\n" +
		"  • aqu-sz — average queue depth. Steady > 1 means requests are\n" +
		"    queueing; that's saturation.\n" +
		"  • await — latency."
	got := reflowProse(text, 40)
	want := []string{
		"First line continues here.",
		"",
		"Key columns:",
		"  • aqu-sz — average queue depth. Steady",
		"    > 1 means requests are queueing;",
		"    that's saturation.",
		"  • await — latency.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reflowProse() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPrintProseWrapsBesideRail(t *testing.T) {
	withUI(t, uiConfig{rich: true, gutter: true}, 40)
	out := captureStdout(func() {
		printProse(strings.Repeat("word ", 20), nil)
	})
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "│ ") {
			t.Fatalf("prose line missing rail: %q", line)
		}
		if visibleWidth(line) > 40 {
			t.Fatalf("prose line wider than the terminal: %q", line)
		}
	}
}

func TestPrintProseCapsAt72Columns(t *testing.T) {
	withUI(t, uiConfig{rich: true, gutter: true}, 200)
	out := captureStdout(func() {
		printProse(strings.Repeat("word ", 40), nil)
	})
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if visibleWidth(line) > 72+gutterWidth() {
			t.Fatalf("prose line wider than 72 columns: %q", line)
		}
	}
}

// answerQuestion asks q with a fixed shuffle and answers with the option
// whose text is pick, returning what was printed.
func answerQuestion(t *testing.T, q Question, pick string, run questionCommandRunner, extraInput string) string {
	t.Helper()
	oldStdin, oldRand := stdin, appRand
	t.Cleanup(func() { stdin, appRand = oldStdin, oldRand })

	appRand = rand.New(rand.NewSource(7))
	options := randomizedQuestionOptions(q)
	choice := 0
	for i, o := range options {
		if o == pick {
			choice = i + 1
		}
	}
	appRand = rand.New(rand.NewSource(7))
	stdin = bufio.NewReader(strings.NewReader(fmt.Sprintf("%s%d\n", extraInput, choice)))
	return captureStdout(func() {
		askQuestionWithFocus(q, run, nil, nil)
	})
}

func TestQuestionFeedbackUsesMarksAndColor(t *testing.T) {
	withUI(t, richColorUI, 80)
	q := Question{Stem: "Which?", Correct: "the right one", Distractors: []string{"wrong", "also wrong"}}

	out := answerQuestion(t, q, "the right one", nil, "")
	if !strings.Contains(out, good("✓")+" Correct.\n") {
		t.Fatalf("correct feedback missing green mark:\n%q", out)
	}
	if !strings.Contains(out, bold("Which?")) {
		t.Fatalf("question stem is not bold:\n%q", out)
	}

	out = answerQuestion(t, q, "wrong", nil, "")
	want := bad("✗") + " Not quite. The answer is " + bold("the") + " " + bold("right") + " " + bold("one.")
	if !strings.Contains(out, want) {
		t.Fatalf("wrong-answer feedback = %q, want %q", out, want)
	}
}

func TestQuestionFeedbackPlainCopy(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	q := Question{Stem: "Which?", Correct: "the right one", Distractors: []string{"wrong"}}
	out := answerQuestion(t, q, "wrong", nil, "")
	if !strings.Contains(out, "✗ Not quite. The answer is the right one.\n") {
		t.Fatalf("plain wrong-answer feedback missing:\n%s", out)
	}
	for _, old := range []string{"--- Check ---", "--- Feedback ---", "Your answer:", "Result:"} {
		if strings.Contains(out, old) {
			t.Fatalf("output still contains old label %q:\n%s", old, out)
		}
	}
}

func TestChoicePromptAndRetryShareWording(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	q := Question{Stem: "Which?", Correct: "a", Distractors: []string{"b", "c", "d"}}
	run := func(cmd string) CapturedCommand { return CapturedCommand{Cmd: cmd} }
	out := answerQuestion(t, q, "a", run, "9\n")
	if !strings.Contains(out, "Choice (1-4, skip, $ cmd): ") {
		t.Fatalf("prompt does not list the accepted inputs:\n%s", out)
	}
	if !strings.Contains(out, "Choose 1-4, skip, or $ cmd.\n") {
		t.Fatalf("retry message does not match the prompt:\n%s", out)
	}

	out = answerQuestion(t, q, "a", nil, "")
	if !strings.Contains(out, "Choice (1-4, skip): ") {
		t.Fatalf("prompt offers $ cmd without a runner:\n%s", out)
	}
}

func TestQuestionShownAgainAfterInlineCommand(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	q := Question{Stem: "Which column?", Correct: "a", Distractors: []string{"b"}}
	run := func(cmd string) CapturedCommand { return CapturedCommand{Cmd: cmd, Output: "out\n"} }
	out := answerQuestion(t, q, "a", run, "$ echo out\n")
	if strings.Count(out, "Which column?") != 2 {
		t.Fatalf("question not repeated after running a command:\n%s", out)
	}
}

func TestOptionsWrapUnderOptionText(t *testing.T) {
	withUI(t, uiConfig{rich: true, gutter: true}, 40)
	out := captureStdout(func() {
		printQuestion("Stem", []string{strings.Repeat("long ", 12)}, nil)
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("option did not wrap:\n%s", out)
	}
	if !strings.HasPrefix(lines[1], "│   1. long") || !strings.HasPrefix(lines[2], "│      long") {
		t.Fatalf("wrapped option not aligned under its text:\n%s", out)
	}
}

func TestGuidePromptShowsPosition(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	if got := guidePrompt(&Session{guideStep: 3, guideTotal: 7}); got != "[guide 3/7] $ " {
		t.Fatalf("guidePrompt() = %q", got)
	}
	if got := guidePrompt(&Session{}); got != "[guide] $ " {
		t.Fatalf("guidePrompt() without a position = %q", got)
	}
	withUI(t, richColorUI, 80)
	if got := guidePrompt(&Session{guideStep: 1, guideTotal: 2}); got != accent("[guide 1/2]")+" $ " {
		t.Fatalf("guidePrompt() is not in the accent colour: %q", got)
	}
}

func TestGuideProgressLineGroupsByDimension(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	steps := []GuideStep{
		{Dimension: "Saturation"},
		{Dimension: "Utilization"},
		{Dimension: "Saturation"},
		{Dimension: "Errors"},
		{Dimension: "Utilization"},
	}
	states := []guideStepState{stepDone, stepSkipped, stepCurrent, stepPending, stepPending}
	got := guideProgressLine(steps, states)
	if want := "Utilization –○  Saturation ✓●  Errors ○"; got != want {
		t.Fatalf("guideProgressLine() = %q, want %q", got, want)
	}
}

func TestGuideStepHeaderUsesTitleAndProgress(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	out := captureStdout(func() {
		printGuideStepHeader(3, 7, GuideStep{Name: "runqueue", Title: "Run queue", Intro: "Intro.", Suggested: "vmstat 1 5"}, "Saturation ●")
	})
	if want := "\n\nStep 3/7  Run queue\nSaturation ●\nIntro.\n\nSuggested: vmstat 1 5\n\n"; out != want {
		t.Fatalf("header = %q, want %q", out, want)
	}
}

func TestGuideStepsHaveTitlesAndDimensions(t *testing.T) {
	si := SystemInfo{HasMpstat: true, HasPSI: true, HasMemoryPSI: true, HasIOPSI: true, HasJournalctl: true}
	for _, name := range resourceNames() {
		inv := investigations[name]
		if !hasGuidedWalkthrough(name, inv) {
			continue
		}
		for _, step := range inv.StepsFn(si) {
			if step.Title == "" {
				t.Errorf("%s step %q has no title", name, step.Name)
			}
			if !containsString(guideDimensionOrder, step.Dimension) {
				t.Errorf("%s step %q has dimension %q", name, step.Name, step.Dimension)
			}
			if strings.HasPrefix(step.Intro, "Step ") {
				t.Errorf("%s step %q intro repeats the step number: %q", name, step.Name, step.Intro)
			}
		}
	}
}

func TestColumnFocusUnderlinesHeaderOnly(t *testing.T) {
	withUI(t, richColorUI, 120)
	c := CapturedCommand{Cmd: "vmstat 1 2", Output: sampleVmstat}
	got := columnFocusLines("In the `vmstat 1 2` output, what does the `r` column count?", c)
	if len(got) != 1 {
		t.Fatalf("columnFocusLines() = %q, want one header line", got)
	}
	if !strings.Contains(got[0], underline("r")+"  b") {
		t.Fatalf("focus line does not underline the r column: %q", got[0])
	}
	if strings.Contains(got[0], "123456") {
		t.Fatalf("focus line repeats a data row: %q", got[0])
	}
}

func TestColumnFocusNeedsColorAndAColumn(t *testing.T) {
	c := CapturedCommand{Cmd: "vmstat 1 2", Output: sampleVmstat}
	withUI(t, uiConfig{rich: true, gutter: true}, 120)
	if got := columnFocusLines("What does `r` count?", c); got != nil {
		t.Fatalf("focus shown without colour: %q", got)
	}
	withUI(t, richColorUI, 120)
	if got := columnFocusLines("What does the run queue count?", c); got != nil {
		t.Fatalf("focus shown without a column in the stem: %q", got)
	}
	withUI(t, richColorUI, 30)
	if got := columnFocusLines("What does `r` count?", c); got != nil {
		t.Fatalf("focus shown although the header is wider than the terminal: %q", got)
	}
}

func TestSelectorRowsUseReverseVideo(t *testing.T) {
	withUI(t, richColorUI, 80)
	out := captureStdout(func() {
		renderClaimSelector("Saturation", "", []string{"present", "absent"}, 1, false)
	})
	if !strings.Contains(out, "> "+reverse("2. absent")) {
		t.Fatalf("highlighted claim is not reversed:\n%q", out)
	}
	if !strings.Contains(out, "↑/k ↓/j "+faint("move")) {
		t.Fatalf("help descriptions are not faint:\n%q", out)
	}
}

func TestEvidenceSelectorMarksCheckedItems(t *testing.T) {
	withUI(t, richColorUI, 120)
	cands := []Observation{{Name: "a", Title: "Signal A"}, {Name: "b", Title: "Signal B"}}
	snap := Snapshot{Values: map[string]Value{"a": {Number: 1}, "b": {Number: 2}}}
	out := captureStdout(func() {
		renderEvidenceSelector(cands, snap, 1, []bool{true, false}, false)
	})
	if !strings.Contains(out, good("[✓]")+" [1] Signal A = "+faint("1.00")) {
		t.Fatalf("checked item not marked green with faint value:\n%q", out)
	}
	if !strings.Contains(out, "> [ ] "+reverse("[2] Signal B")) {
		t.Fatalf("highlighted item not reversed:\n%q", out)
	}
}

func TestSelectorRowCountIncludesRail(t *testing.T) {
	withUI(t, uiConfig{rich: true, gutter: true, color: true}, 20)
	var rows int
	captureStdout(func() {
		rows = printSelectorLine(strings.Repeat("x", 19), 20, true)
	})
	if rows != 2 {
		t.Fatalf("printSelectorLine() rows = %d, want 2 once the rail is added", rows)
	}
}

func TestAssessmentColors(t *testing.T) {
	withUI(t, richColorUI, 80)
	for _, tc := range []struct {
		g    dimensionGrade
		want func(string) string
	}{
		{dimensionGrade{Supports: 2, SupportSources: 2}, good},
		{dimensionGrade{Supports: 3, SupportSources: 1}, warn},
		{dimensionGrade{Supports: 1}, warn},
		{dimensionGrade{Supports: 1, Contradicts: 1}, warn},
		{dimensionGrade{Contradicts: 1}, bad},
		{dimensionGrade{}, bad},
	} {
		if got := assessmentStyle(tc.g)("x"); got != tc.want("x") {
			t.Errorf("assessmentStyle(%+v) = %q, want %q (%s)", tc.g, got, tc.want("x"), tc.g.assessment())
		}
	}
}

func TestDiagnoseSummaryGrid(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	grades := []dimensionGrade{
		{Resource: "CPU", Dimension: "Utilization", Claim: "low", HasData: true, Accurate: true, Supports: 2, SupportSources: 2},
		{Resource: "CPU", Dimension: "Saturation", Claim: "present", HasData: true, Accurate: false},
		{Resource: "Memory", Dimension: "Errors", Claim: "", Accurate: true},
	}
	out := captureStdout(func() {
		printDiagnoseSummary(investigations["system"], []string{"CPU", "Memory"}, grades)
	})
	for _, want := range []string{
		"Summary\n",
		"        U  S  E\n",
		"CPU     ✓  ✗  ·\n",
		"Memory  ·  ·  ✓\n",
		"2 of 3 verdicts matched what you captured.\n",
		"Next: run `use-tool practice system` again and look harder at CPU Saturation",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestDiagnoseNextStepWidensScopeWhenAllMatch(t *testing.T) {
	grades := []dimensionGrade{
		{Resource: "CPU", Dimension: "Utilization", Claim: "low", Accurate: true, Supports: 2, SupportSources: 2},
		{Resource: "CPU", Dimension: "Saturation", Claim: "absent", Accurate: true, Supports: 2, SupportSources: 2},
		{Resource: "CPU", Dimension: "Errors", Claim: "absent", Accurate: true, Supports: 2, SupportSources: 2},
	}
	byCell := map[string]dimensionGrade{}
	for _, g := range grades {
		byCell[g.Resource+"/"+g.Dimension] = g
	}
	got := diagnoseNextStep(cpuInvestigation, []string{"CPU"}, grades, byCell)
	if !strings.Contains(got, "use-tool practice system") {
		t.Fatalf("diagnoseNextStep() = %q, want a pointer to practice system", got)
	}
}

func TestNoteLineFormats(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	out := captureStderr(func() { noteLine(os.Stderr, "exit 1, 0.1s") })
	if out != "[exit 1, 0.1s]\n" {
		t.Fatalf("plain note = %q", out)
	}
	withUI(t, richColorUI, 80)
	out = captureStderr(func() { noteLine(os.Stderr, "exit 1, 0.1s") })
	if out != accent("│")+" "+faint("exit 1, 0.1s")+"\n" {
		t.Fatalf("rich note = %q", out)
	}
}

func TestSnapshotPaddingIgnoresMultibyteTitles(t *testing.T) {
	withUI(t, uiConfig{}, 80)
	snap := Snapshot{
		Sources: []string{"x"},
		Sections: []SnapshotSection{{Title: "Utilization", Items: []SnapshotItem{
			{Title: "a — b", Value: Value{Number: 1}},
			{Title: "abcde", Value: Value{Number: 2}},
		}}},
	}
	out := captureStdout(snap.Print)
	var cols []int
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  a") {
			cols = append(cols, visibleWidth(line))
		}
	}
	if len(cols) != 2 || cols[0] != cols[1] {
		t.Fatalf("value column misaligned (%v):\n%s", cols, out)
	}
}
