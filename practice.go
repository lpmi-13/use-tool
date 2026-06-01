package main

import (
	"fmt"
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
	requireInteractive("practice")
	si := detectSystem()
	s := &Session{Investigation: inv, System: si}

	fmt.Printf("\n=== %s — practice mode ===\n", inv.Title)
	fmt.Println(inv.Description)
	fmt.Printf("\nDetected system: %d logical CPU%s.\n", si.NumCPU, plural(si.NumCPU))
	fmt.Println("Shell commands run on this live system.")
	fmt.Println("Builtins: `report` (snapshot of what you've gathered), `commands` (cheatsheet),")
	fmt.Println("          `diagnose` (check the system's USE state from what you saw), `help`, `exit`.")
	fmt.Println()

	practiceLoop(s)
}

func practiceLoop(s *Session) {
	for {
		line, status := readPrompt("[practice] $ ")
		if status != lineReadOK {
			return
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
		switch line {
		case "exit", "quit":
			return
		case "help":
			fmt.Println("Run any shell command on this live system. Builtins: report, commands, diagnose, help, exit.")
		case "report":
			s.Snapshot().Print()
		case "commands":
			printCommands(s.Investigation, s.System)
		case "diagnose":
			if practiceDiagnose(s) {
				return
			}
		default:
			s.runAndCapture(line)
		}
	}
}
