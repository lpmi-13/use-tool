package main

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
)

type Investigation struct {
	Name           string
	Title          string
	Description    string
	StepsFn        func(SystemInfo) []GuideStep
	Extractors     []Extractor
	Observations   []Observation
	SynthesisRules []SynthesisRule
	Commands       []CommandRef
}

type GuideStep struct {
	Name        string
	Intro       string
	Suggested   string
	QuestionsFn func(SystemInfo, CapturedCommand) []Question
	AcceptAny   bool
	Teaching    string
}

type Extractor struct {
	BaseCmd     string
	QuestionsFn func(SystemInfo, CapturedCommand) []Question
}

type Question struct {
	Stem        string
	Correct     string
	Distractors []string
}

type SynthesisRule struct {
	Requires []string
	Generate func(SystemInfo, map[string]Value) (Question, bool)
}

type CommandRef struct {
	Cmd     string
	Section string
	Summary string
}

var investigations = map[string]*Investigation{
	"cpu":     cpuInvestigation,
	"memory":  memoryInvestigation,
	"disk":    diskInvestigation,
	"network": networkInvestigation,
}

func resourceNames() []string {
	out := make([]string, 0, len(investigations))
	for k := range investigations {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func getInvestigation(name string) (*Investigation, error) {
	inv, ok := investigations[name]
	if !ok {
		return nil, fmt.Errorf("unknown resource %q (available: %s)", name, strings.Join(resourceNames(), ", "))
	}
	return inv, nil
}

func askQuestion(q Question) bool {
	options := append([]string{q.Correct}, q.Distractors...)
	rand.Shuffle(len(options), func(i, j int) { options[i], options[j] = options[j], options[i] })
	fmt.Println()
	fmt.Println(q.Stem)
	for i, o := range options {
		fmt.Printf("  %d. %s\n", i+1, o)
	}
	for {
		fmt.Print("Choice: ")
		line, ok := readLine()
		if !ok {
			return false
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			fmt.Printf("Pick a number 1-%d.\n", len(options))
			continue
		}
		chosen := options[n-1]
		if chosen == q.Correct {
			fmt.Println("Correct.")
			return true
		}
		fmt.Printf("Not quite. Correct answer: %s\n", q.Correct)
		return false
	}
}

func extractQuestions(inv *Investigation, si SystemInfo, c CapturedCommand) []Question {
	base := baseCmd(c.Cmd)
	if base == "" {
		return nil
	}
	var qs []Question
	for _, e := range inv.Extractors {
		if e.BaseCmd == base {
			qs = append(qs, e.QuestionsFn(si, c)...)
		}
	}
	return qs
}

// baseCmd returns the first whitespace-separated token of a command line,
// skipping a leading `sudo`. Returns "" for empty input. Used to match
// captured commands against an extractor without false positives like
// `vim dmesg.txt` matching dmesg.
func baseCmd(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	base := fields[0]
	if base == "sudo" && len(fields) > 1 {
		base = fields[1]
	}
	return base
}

func recallQuestions(inv *Investigation, snap Snapshot) []Question {
	var qs []Question
	for _, obs := range inv.Observations {
		if obs.Recall == nil {
			continue
		}
		v, ok := snap.Values[obs.Name]
		if !ok {
			continue
		}
		qs = append(qs, obs.Recall(v)...)
	}
	return qs
}

func synthesisQuestions(inv *Investigation, si SystemInfo, snap Snapshot) []Question {
	var qs []Question
	for _, rule := range inv.SynthesisRules {
		ready := true
		for _, req := range rule.Requires {
			if _, ok := snap.Values[req]; !ok {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		if q, ok := rule.Generate(si, snap.Values); ok {
			qs = append(qs, q)
		}
	}
	return qs
}

func printCommands(inv *Investigation) {
	fmt.Printf("\n%s — command reference\n", inv.Title)
	fmt.Println(strings.Repeat("=", 60))
	bySection := map[string][]CommandRef{}
	for _, c := range inv.Commands {
		bySection[c.Section] = append(bySection[c.Section], c)
	}
	for _, sec := range []string{"Utilization", "Saturation", "Errors"} {
		cmds, ok := bySection[sec]
		if !ok {
			continue
		}
		fmt.Printf("\n%s\n%s\n", sec, strings.Repeat("-", len(sec)))
		for _, c := range cmds {
			fmt.Printf("\n  %s\n", c.Cmd)
			for _, line := range strings.Split(c.Summary, "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
	}
	fmt.Println()
}
