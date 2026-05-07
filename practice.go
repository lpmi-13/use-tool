package main

import (
	"fmt"
	"math/rand"
	"os"
)

func cmdPractice(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: use-tool practice <resource>")
		os.Exit(2)
	}
	inv, err := getInvestigation(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	si := detectSystem()
	s := &Session{Investigation: inv, System: si}

	fmt.Printf("\n=== %s — practice mode ===\n", inv.Title)
	fmt.Println(inv.Description)
	fmt.Printf("\nDetected system: %d logical CPU%s.\n", si.NumCPU, plural(si.NumCPU))
	fmt.Println("Builtins: `report` (snapshot of what you've gathered), `commands` (cheatsheet),")
	fmt.Println("          `evaluate` (comprehension check), `help`, `exit`.")
	fmt.Println()

	practiceLoop(s)
}

func practiceLoop(s *Session) {
	for {
		fmt.Print("[practice] $ ")
		line, ok := readLine()
		if !ok {
			return
		}
		if line == "" {
			continue
		}
		switch line {
		case "exit", "quit":
			return
		case "help":
			fmt.Println("Run any shell command. Builtins: report, commands, evaluate, help, exit.")
		case "report":
			s.Snapshot().Print()
		case "commands":
			printCommands(s.Investigation)
		case "evaluate":
			if practiceEvaluate(s) {
				return
			}
		default:
			s.Captured = append(s.Captured, runCommand(line))
		}
	}
}

func practiceEvaluate(s *Session) bool {
	snap := s.Snapshot()

	var qs []Question
	for _, c := range s.Captured {
		qs = append(qs, extractQuestions(s.Investigation, s.System, c)...)
	}
	qs = append(qs, recallQuestions(s.Investigation, snap)...)
	qs = append(qs, synthesisQuestions(s.Investigation, s.System, snap)...)

	if len(qs) == 0 {
		fmt.Println("\nNo questions could be generated from your captured commands.")
		fmt.Println("Try `commands` for a cheatsheet, or run `uptime`, `vmstat 1 3`, `mpstat -P ALL 1 3`.")
		return false
	}

	rand.Shuffle(len(qs), func(i, j int) { qs[i], qs[j] = qs[j], qs[i] })
	n := 4
	if len(qs) < n {
		n = len(qs)
	}
	score := 0
	for i := 0; i < n; i++ {
		if askQuestion(qs[i]) {
			score++
		}
	}
	fmt.Printf("\n=== Result: %d / %d ===\n", score, n)
	return true
}
