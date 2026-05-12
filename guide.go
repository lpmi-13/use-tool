package main

import (
	"fmt"
	"os"
)

func cmdGuide(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: use-tool guide <resource>")
		os.Exit(2)
	}
	inv, err := getInvestigation(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	requireInteractive("guide")
	si := detectSystem()
	s := &Session{Investigation: inv, System: si}

	fmt.Printf("\n=== %s — guided walkthrough ===\n", inv.Title)
	fmt.Println(inv.Description)
	fmt.Printf("\nDetected system: %d logical CPU%s.\n", si.NumCPU, plural(si.NumCPU))
	fmt.Println("At each step, run the suggested command. Type `skip` to move on, `exit` to quit.")

	steps := inv.StepsFn(si)
	score, total := 0, 0
	for i, step := range steps {
		fmt.Printf("\n--- Step %d/%d: %s ---\n", i+1, len(steps), step.Name)
		fmt.Println(step.Intro)
		fmt.Printf("Suggested: %s\n", step.Suggested)

		captured := guideStepCommand(s, step)
		if captured != nil {
			for _, q := range step.QuestionsFn(si, *captured) {
				total++
				result := askQuestion(q)
				if result.Quit {
					return
				}
				if result.Correct {
					score++
				}
				if !pauseGuide() {
					return
				}
			}
		}

		if step.Teaching != "" {
			fmt.Println()
			fmt.Println("--- Teaching note ---")
			fmt.Println(step.Teaching)
		}
	}

	fmt.Println("\n--- Snapshot of what you observed ---")
	s.Snapshot().Print()

	if total > 0 {
		fmt.Printf("=== Walkthrough complete: %d / %d on the inline questions ===\n", score, total)
	} else {
		fmt.Println("=== Walkthrough complete ===")
	}
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
		fmt.Print("[guide] $ ")
		line, ok := readLine()
		if !ok {
			return nil
		}
		if line == "" {
			continue
		}
		if line == "skip" {
			return nil
		}
		if line == "exit" || line == "quit" {
			fmt.Println("Exiting.")
			os.Exit(0)
		}
		c := runCommand(line)
		s.appendCaptured(c)
		if step.AcceptAny {
			return &c
		}
		if len(step.QuestionsFn(s.System, c)) > 0 {
			return &c
		}
		fmt.Printf("(That command didn't produce output this step recognizes — try `%s`, or `skip`.)\n", step.Suggested)
	}
}
