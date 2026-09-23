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
	si := detectSystem()
	s := &Session{Investigation: inv, System: si}

	fmt.Printf("\n=== %s — guided walkthrough ===\n", inv.Title)
	fmt.Println(inv.Description)
	fmt.Printf("\nDetected system: %d logical CPU%s.\n", si.NumCPU, plural(si.NumCPU))
	fmt.Println("At each step, run the suggested command (or an alternative if shown). Type `skip` to move on, `exit` to quit.")
	fmt.Println("During a check, answer with a number; use `$ <command>` to inspect more data first.")

	steps := inv.StepsFn(si)
	score, total := 0, 0
	for i, step := range steps {
		printGuideStepHeader(i+1, len(steps), step)
		correct, answered, ok := runGuideStep(s, step, i == len(steps)-1)
		score += correct
		total += answered
		if !ok {
			return
		}
	}

	finishGuide(s, score, total)
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
	hadQuestions := false
	if captured != nil {
		if s.guideQuestionKeys == nil {
			s.guideQuestionKeys = make(map[string]bool)
		}
		questions := chooseUnseenGuideQuestions(
			guideQuestions(s.System, step, *captured),
			step.QuestionCount,
			s.guideQuestionKeys,
		)
		if len(questions) > 0 {
			hadQuestions = true
			for _, q := range questions {
				result := askQuestionWithCommandRunner(q, s.runAndCapture)
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
		fmt.Println("--- Teaching note ---")
		fmt.Println(step.Teaching)
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

	fmt.Println("\n--- Snapshot of what you observed ---")
	snap := s.Snapshot()
	snap.Print()
	printSynopsis(s.Investigation, s.System, snap)

	if total > 0 {
		fmt.Printf("=== Walkthrough complete: %d / %d on the inline questions ===\n", score, total)
	} else {
		fmt.Println("=== Walkthrough complete ===")
	}
}

func printGuideStepHeader(n, total int, step GuideStep) {
	fmt.Printf("\n--- Step %d/%d: %s ---\n", n, total, step.Name)
	fmt.Println(step.Intro)
	fmt.Printf("Suggested: %s\n", step.Suggested)
	for _, alt := range step.Alternatives {
		fmt.Printf("Alternative: %s\n", alt)
	}
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
	fmt.Print("\nPress Enter to continue...")
	line, ok := readLine()
	fmt.Println()
	if !ok {
		return false
	}
	if isExitCommand(line) {
		fmt.Println("Exiting.")
		return false
	}
	return true
}

func guideStepCommand(s *Session, step GuideStep) *CapturedCommand {
	for {
		line, status := readPrompt("[guide] $ ")
		if status == lineReadClosed {
			return nil
		}
		if status == lineReadInterrupted {
			fmt.Println("Exiting.")
			os.Exit(0)
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
			fmt.Println("Exiting.")
			os.Exit(0)
		}
		if isLikelyChoiceAnswer(line) {
			fmt.Printf("(That looks like a multiple-choice answer (`%s`), but we're at a shell prompt — not a `Choice:` prompt yet.\n  Run a command (try `%s`), or type `skip`.)\n", line, step.Suggested)
			continue
		}
		if !confirmShellCommand(line) {
			continue
		}
		c := s.runAndCaptureFiltered(line, step.Filter)
		if c.Failed {
			fmt.Println("(Command failed; fix it and try again, or `skip`.)")
			continue
		}
		if !guideStepExpectsCommand(step, c.Cmd) {
			printUnrecognizedGuideOutput(c, step)
			continue
		}
		if strings.TrimSpace(c.Output) == "" && step.EmptyOutputMessage != "" {
			fmt.Println(step.EmptyOutputMessage)
			if !pauseGuide() {
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
	case strings.HasSuffix(c.Output, "\n\n"):
		// The command already left a blank line.
	case strings.HasSuffix(c.Output, "\n"):
		fmt.Println()
	default:
		fmt.Print("\n\n")
	}
	fmt.Printf("(That command didn't produce output this step recognizes — try `%s`, or `skip`.)\n\n", step.Suggested)
}
