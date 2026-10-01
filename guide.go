package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// isLikelyChoiceAnswer detects when a learner has typed a single small
// integer at the `[guide] $` shell prompt — almost always because they
// mistook it for the `Choice:` prompt. We catch this before passing the
// string to `sh -c`, which would otherwise error with `sh: 1: not found`.
func isLikelyChoiceAnswer(line string) bool {
	trimmed := strings.TrimSpace(line)
	n, err := strconv.Atoi(trimmed)
	return err == nil && n >= 1 && n <= 9
}

func cmdGuide(args []string) {
	if len(args) < 1 {
		resource, err := chooseResourceForCommand("guide")
		if err != nil {
			exitSelectionError(err)
		}
		args = []string{resource}
	}
	inv, err := getInvestigation(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if inv.StepsFn == nil || len(inv.StepsFn(SystemInfo{})) == 0 {
		fmt.Fprintf(os.Stderr, "%s has no guided walkthrough — run `use-tool practice %s` instead.\n", inv.Name, inv.Name)
		os.Exit(2)
	}
	requireInteractive("guide")
	enableGutter()
	si := detectSystem()
	s := &Session{Investigation: inv, System: si}

	fmt.Println()
	tutorln(bold(inv.Title + " — guided walkthrough"))
	printProse(inv.Description, nil)
	tutorf("Detected system: %d logical CPU%s.\n", si.NumCPU, plural(si.NumCPU))
	fmt.Println()
	printProse("At each step, run the suggested command (or an alternative if shown). Type `skip` to move on, `exit` to quit.\n"+
		"During a check, answer with a number; use `$ <command>` to inspect more data first.", faint)

	steps := inv.StepsFn(si)
	states := make([]guideStepState, len(steps))
	score, total := 0, 0
	for i, step := range steps {
		states[i] = stepCurrent
		s.guideStep, s.guideTotal = i+1, len(steps)
		setWindowTitle(fmt.Sprintf("%s: %s, step %d/%d", appName, inv.Name, i+1, len(steps)))
		printGuideStepHeader(i+1, len(steps), step, guideProgressLine(steps, states))
		correct, answered, ok := runGuideStep(s, step, i == len(steps)-1)
		score += correct
		total += answered
		states[i] = stepDone
		if s.guideStepSkipped {
			states[i] = stepSkipped
		}
		if !ok {
			restoreWindowTitle()
			return
		}
	}

	finishGuide(s, score, total)
	restoreWindowTitle()
}

// exitGuide leaves the walkthrough from deep inside a step.
func exitGuide() {
	tutorln("Exiting.")
	restoreWindowTitle()
	os.Exit(0)
}

type guideStepState int

const (
	stepPending guideStepState = iota
	stepCurrent
	stepDone
	stepSkipped
)

// guideDimensionOrder is the order step progress is grouped in.
var guideDimensionOrder = []string{"Orientation", "Utilization", "Saturation", "Errors"}

// guideProgressLine groups step progress by USE dimension, e.g.
// "Utilization ✓✓  Saturation ●○  Errors ○". ✓ is done, – skipped, ● the
// current step and ○ still to come.
func guideProgressLine(steps []GuideStep, states []guideStepState) string {
	marks := map[string]string{}
	order := append([]string(nil), guideDimensionOrder...)
	for i, step := range steps {
		dim := step.Dimension
		if dim == "" {
			dim = "Other"
		}
		if !containsString(order, dim) {
			order = append(order, dim)
		}
		var mark string
		switch states[i] {
		case stepDone:
			mark = "✓"
		case stepSkipped:
			mark = "–"
		case stepCurrent:
			mark = accent("●")
		default:
			mark = faint("○")
		}
		marks[dim] += mark
	}
	var groups []string
	for _, dim := range order {
		if marks[dim] == "" {
			continue
		}
		groups = append(groups, faint(dim)+" "+marks[dim])
	}
	return strings.Join(groups, "  ")
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// runGuideStep drives a single guided-walkthrough step: it captures the
// learner's command, asks the step's comprehension questions (which print
// their own feedback), then prints the teaching note and pauses so the learner
// can advance. The teaching note is printed before the pause so the learner
// reads the question feedback and the note together, then hits Enter once.
//
// isLast suppresses the per-step pause on the final step, whose pause is owned
// by finishGuide, so the learner never sees two "Press Enter" prompts in a row.
// It returns how many questions were answered correctly and answered in total,
// and ok=false when the learner quit or closed input (the caller should stop).
func runGuideStep(s *Session, step GuideStep, isLast bool) (correct, answered int, ok bool) {
	captured := guideStepCommand(s, step)
	s.guideStepSkipped = captured == nil
	hadQuestions := false
	if captured != nil {
		if s.guideQuestionKeys == nil {
			s.guideQuestionKeys = make(map[string]bool)
		}
		recognizedQuestions := guideQuestions(s.System, step, *captured)
		if len(recognizedQuestions) == 0 &&
			strings.TrimSpace(captured.Output) != "" &&
			step.NoRecognizedOutputMessage != "" {
			fmt.Println()
			printProse(step.NoRecognizedOutputMessage, nil)
		}
		questions := chooseUnseenGuideQuestions(
			recognizedQuestions,
			step.QuestionCount,
			s.guideQuestionKeys,
		)
		if len(questions) > 0 {
			hadQuestions = true
			for _, q := range questions {
				result := askQuestionWithFocus(q, s.runAndCapture, &s.guideAnswerPositions, columnFocusLines(q.Stem, *captured))
				if result.Quit {
					return correct, answered, false
				}
				if !result.Skipped {
					answered++
					if result.Correct {
						correct++
					}
				}
			}
		}
	}

	if step.Teaching != "" {
		fmt.Println()
		tutorln(bold("Teaching note"))
		printProse(step.Teaching, nil)
	}

	// Pause only when this step showed questions, matching the prior behaviour.
	if hadQuestions && !isLast {
		if !pauseGuide() {
			return correct, answered, false
		}
	}
	return correct, answered, true
}

func finishGuide(s *Session, score, total int) {
	if !pauseGuide() {
		return
	}

	tutorln(bold("Snapshot of what you observed"))
	snap := s.Snapshot()
	snap.Print()
	printSynopsis(s.Investigation, s.System, snap)

	tutorln(bold("Walkthrough complete"))
	if total > 0 {
		tutorf("Score: %d/%d checks correct.\n", score, total)
	} else {
		tutorln("Score: no checks answered.")
	}
	name := "<resource>"
	if s.Investigation != nil && s.Investigation.Name != "" {
		name = s.Investigation.Name
	}
	printHanging("Next: ", fmt.Sprintf("run `use-tool practice %s`, gather evidence your own way, then `diagnose` to test your judgement.", name), nil)
	fmt.Println()
}

func printGuideStepHeader(n, total int, step GuideStep, progress string) {
	title := step.Title
	if title == "" {
		title = step.Name
	}
	fmt.Println()
	tutorln(bold(fmt.Sprintf("Step %d/%d  %s", n, total, title)))
	if progress != "" {
		tutorln(progress)
	}
	if step.Intro != "" {
		printProse(step.Intro, nil)
	}
	tutorf("Suggested: %s\n", step.Suggested)
	for _, alt := range step.Alternatives {
		tutorln(faint("Alternative: " + alt))
	}
}

// guidePrompt is the shell prompt for the walkthrough, showing the position
// when it is known.
func guidePrompt(s *Session) string {
	label := "[guide]"
	if s != nil && s.guideTotal > 0 {
		label = fmt.Sprintf("[guide %d/%d]", s.guideStep, s.guideTotal)
	}
	return accent(label) + " $ "
}

func guideQuestions(si SystemInfo, step GuideStep, captured CapturedCommand) []Question {
	if step.QuestionsFn == nil {
		return nil
	}
	if !guideStepExpectsCommand(step, captured.Cmd) {
		return nil
	}
	return personalizeQuestions(step.QuestionsFn(si, captured), captured.Cmd)
}

func guideStepExpectsCommand(step GuideStep, actual string) bool {
	expected := step.ExpectedCommands
	if len(expected) == 0 {
		expected = append([]string{step.Suggested}, step.Alternatives...)
	}
	for _, command := range expected {
		if guideCommandMatches(actual, command) {
			return true
		}
	}
	return false
}

func chooseGuideQuestions(questions []Question, count int) []Question {
	return chooseUnseenGuideQuestions(questions, count, nil)
}

// chooseUnseenGuideQuestions chooses a random subset while excluding concepts
// that have already been presented. It also prevents two questions selected
// for the same step from covering the same concept. When seen is non-nil it is
// updated with every selected question, carrying the suppression across guide
// steps. Exact matching answers are a safety net for untagged question pools.
func chooseUnseenGuideQuestions(questions []Question, count int, seen map[string]bool) []Question {
	if len(questions) == 0 {
		return nil
	}
	if count <= 0 {
		count = 1
	}
	if seen == nil {
		seen = make(map[string]bool)
	}
	candidates := append([]Question(nil), questions...)
	appRand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	chosen := make([]Question, 0, min(count, len(candidates)))
	for _, q := range candidates {
		keys := guideQuestionKeys(q)
		duplicate := false
		for _, key := range keys {
			if seen[key] {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		chosen = append(chosen, q)
		for _, key := range keys {
			seen[key] = true
		}
		if len(chosen) == count {
			break
		}
	}
	return chosen
}

func guideQuestionKeys(q Question) []string {
	var keys []string
	for _, concept := range q.Concepts {
		if normalized := normalizeGuideQuestionKey(concept); normalized != "" {
			keys = append(keys, "concept:"+normalized)
		}
	}
	if normalized := normalizeGuideQuestionKey(q.Correct); normalized != "" {
		keys = append(keys, "answer:"+normalized)
	}
	if len(keys) == 0 {
		if normalized := normalizeGuideQuestionKey(q.Stem); normalized != "" {
			keys = append(keys, "stem:"+normalized)
		}
	}
	return keys
}

func normalizeGuideQuestionKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func pauseGuide() bool {
	tutorf("\n%s", faint("Press Enter to continue..."))
	line, ok := readLine()
	if !ok {
		fmt.Println()
		return false
	}
	if isExitCommand(line) {
		tutorln("Exiting.")
		return false
	}
	return true
}

func guideStepCommand(s *Session, step GuideStep) *CapturedCommand {
	for {
		line, status := readPrompt(guidePrompt(s))
		if status == lineReadClosed {
			return nil
		}
		if status == lineReadInterrupted {
			exitGuide()
		}
		if line == "" {
			continue
		}
		if cmd, ok := stripCopiedShellPrompt(line); ok {
			line = cmd
			if line == "" {
				continue
			}
		}
		if line == "skip" {
			return nil
		}
		if line == "exit" || line == "quit" {
			exitGuide()
		}
		if isLikelyChoiceAnswer(line) {
			printProse(fmt.Sprintf("(`%s` looks like an answer, but this is the shell prompt; the `Choice` prompt comes after a command runs. Run a command (try `%s`), or type `skip`.)", line, step.Suggested), nil)
			continue
		}
		if !confirmShellCommand(line) {
			continue
		}
		c := s.runAndCaptureFiltered(line, step.Filter)
		if c.Failed {
			tutorln("(Command failed; fix it and try again, or `skip`.)")
			continue
		}
		if step.Filter == nil && c.Output != "" && !strings.HasSuffix(c.Output, "\n") {
			fmt.Println()
		}
		if !guideStepExpectsCommand(step, c.Cmd) {
			printUnrecognizedGuideOutput(c, step)
			continue
		}
		if strings.TrimSpace(c.Output) == "" && step.EmptyOutputMessage != "" {
			tutorln(step.EmptyOutputMessage)
			if !pauseGuide() {
				restoreWindowTitle()
				os.Exit(0)
			}
		}
		if step.AcceptAny {
			return &c
		}
		if len(guideQuestions(s.System, step, c)) > 0 {
			return &c
		}
		printUnrecognizedGuideOutput(c, step)
	}
}

func printUnrecognizedGuideOutput(c CapturedCommand, step GuideStep) {
	// Leave one blank line between command output and guide feedback. Filtered
	// output without a trailing newline was already terminated when displayed
	// by runAndCaptureFiltered, even though c.Output retains its original form.
	switch {
	case c.Output == "":
		fmt.Println()
	case step.Filter != nil && !strings.HasSuffix(c.Output, "\n"):
		fmt.Println()
	case step.Filter == nil && !strings.HasSuffix(c.Output, "\n"):
		// guideStepCommand already ended the unterminated output line.
		fmt.Println()
	case strings.HasSuffix(c.Output, "\n\n"):
		// The command already left a blank line.
	case strings.HasSuffix(c.Output, "\n"):
		fmt.Println()
	default:
		fmt.Print("\n\n")
	}
	printProse(fmt.Sprintf("(That command didn't produce output this step recognizes — try `%s`, or `skip`.)", step.Suggested), nil)
	fmt.Println()
}

// columnFocusLines points at the table column a check asks about by repeating
// the header line from the learner's own captured output with that column
// underlined. Only the header is repeated, never a data row, so the focus
// shows where to look without showing the answer. It needs colour (underline
// is the whole point) and returns nil when no header line names a backticked
// column from the stem, or when the line would not fit beside the rail.
func columnFocusLines(stem string, c CapturedCommand) []string {
	if !ui.color || !ui.gutter {
		return nil
	}
	columns := map[string]bool{}
	for _, token := range backtickedTokens(stem) {
		if strings.ContainsAny(token, " \t") || token == baseCmd(c.Cmd) {
			continue
		}
		columns[token] = true
	}
	if len(columns) == 0 {
		return nil
	}
	output := string(sanitizeTerminalBytes([]byte(c.Output)))
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, " \r")
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		hit := false
		for _, f := range fields {
			if columns[f] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if visibleWidth(line)+2 > selectorTerminalWidth()-gutterWidth() {
			return nil
		}
		return []string{"  " + underlineFields(line, columns)}
	}
	return nil
}

// backtickedTokens returns the `quoted` spans of s.
func backtickedTokens(s string) []string {
	var out []string
	for {
		start := strings.IndexByte(s, '`')
		if start < 0 {
			return out
		}
		end := strings.IndexByte(s[start+1:], '`')
		if end < 0 {
			return out
		}
		out = append(out, s[start+1:start+1+end])
		s = s[start+1+end+1:]
	}
}

// underlineFields underlines whole whitespace-separated fields of line that
// are in want, keeping the line's original spacing.
func underlineFields(line string, want map[string]bool) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' {
			b.WriteByte(line[i])
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' && line[j] != '\t' {
			j++
		}
		field := line[i:j]
		if want[field] {
			field = underline(field)
		}
		b.WriteString(field)
		i = j
	}
	return b.String()
}
