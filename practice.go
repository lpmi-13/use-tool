package main

import (
	"fmt"
	"os"
)

func cmdPractice(args []string) {
	if len(args) < 1 {
		resource, err := chooseResourceForCommand("practice")
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
	requireInteractive("practice")
	enableGutter()
	si := detectSystem()
	s := &Session{Investigation: inv, System: si}

	setWindowTitle(fmt.Sprintf("%s: practice %s", appName, inv.Name))
	fmt.Println()
	tutorln(bold(inv.Title + " — practice mode"))
	printProse(inv.Description, nil)
	tutorf("Detected system: %d logical CPU%s.\n", si.NumCPU, plural(si.NumCPU))
	fmt.Println()
	tutorln("Shell commands run on this live system.")
	printPracticeHelp()
	fmt.Println()

	practiceLoop(s)
	restoreWindowTitle()
}

func printPracticeHelp() {
	tutorln("Builtins:")
	for _, b := range []helpKey{
		{"report", "snapshot of what you've gathered"},
		{"commands", "cheatsheet of relevant commands"},
		{"diagnose", "check the system's USE state from what you saw"},
		{"help", "show this list"},
		{"exit", "quit"},
	} {
		tutorln("  " + padRight(b.Key, 9) + faint(b.Desc))
	}
}

func practiceLoop(s *Session) {
	for {
		line, status := readPrompt(accent("[practice]") + " $ ")
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
			tutorln("Run any shell command on this live system.")
			printPracticeHelp()
		case "report":
			s.Snapshot().Print()
		case "commands":
			printCommands(s.Investigation, s.System)
		case "diagnose":
			if practiceDiagnose(s) {
				return
			}
		default:
			if !confirmShellCommand(line) {
				continue
			}
			s.runAndCapture(line)
		}
	}
}
