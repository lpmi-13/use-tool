package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestSuggestCommand(t *testing.T) {
	got, ok := suggestCommand("practce", topLevelCommands)
	if !ok {
		t.Fatal("expected a suggestion")
	}
	if got != "practice" {
		t.Fatalf("suggestCommand() = %q, want %q", got, "practice")
	}
}

func TestSuggestCommandTooDistant(t *testing.T) {
	if got, ok := suggestCommand("xyz", topLevelCommands); ok {
		t.Fatalf("suggestCommand() = %q, want no suggestion", got)
	}
}

func TestCommandStatusMarksUnavailableRequirements(t *testing.T) {
	ref := CommandRef{
		Cmd:      "mpstat -P ALL 1 N",
		Requires: []string{"mpstat"},
	}
	got := commandStatus(ref, SystemInfo{})
	want := "unavailable: mpstat not found; install sysstat"
	if got != want {
		t.Fatalf("commandStatus() = %q, want %q", got, want)
	}
}

func TestCommandStatusEmptyWhenRequirementsAvailable(t *testing.T) {
	ref := CommandRef{
		Cmd:      "mpstat -P ALL 1 N",
		Requires: []string{"mpstat", "psi"},
	}
	si := SystemInfo{HasMpstat: true, HasPSI: true}
	if got := commandStatus(ref, si); got != "" {
		t.Fatalf("commandStatus() = %q, want empty status", got)
	}
}

func TestIsExitCommand(t *testing.T) {
	for _, input := range []string{"exit", "quit", " EXIT ", "Quit"} {
		if !isExitCommand(input) {
			t.Fatalf("isExitCommand(%q) = false, want true", input)
		}
	}
	for _, input := range []string{"", "1", "exiting", "q"} {
		if isExitCommand(input) {
			t.Fatalf("isExitCommand(%q) = true, want false", input)
		}
	}
}

func TestStripCopiedShellPrompt(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"$ cat /proc/pressure/memory", "cat /proc/pressure/memory", true},
		{"  $   ps -eo pid,rss,comm  ", "ps -eo pid,rss,comm", true},
		{"$", "", true},
		{"cat /proc/meminfo", "cat /proc/meminfo", false},
		{"$HOME/bin/tool", "$HOME/bin/tool", false},
	}
	for _, tc := range cases {
		got, ok := stripCopiedShellPrompt(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Errorf("stripCopiedShellPrompt(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAskQuestionRunsPromptCommandThenAcceptsAnswer(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("$ cat /proc/pressure/memory\n1\n"))

	var ran []string
	result := askQuestionWithCommandRunner(Question{
		Stem:    "Which answer?",
		Correct: "the only option",
	}, func(cmd string) CapturedCommand {
		ran = append(ran, cmd)
		return CapturedCommand{Cmd: cmd, Output: "some avg10=0.00\n"}
	})

	if !result.Correct || result.Quit {
		t.Fatalf("askQuestionWithCommandRunner() = %+v, want correct non-quit result", result)
	}
	if len(ran) != 1 || ran[0] != "cat /proc/pressure/memory" {
		t.Fatalf("runner commands = %v, want [cat /proc/pressure/memory]", ran)
	}
}
