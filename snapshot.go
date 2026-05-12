package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type Value struct {
	Number  float64
	Samples []float64
	Unit    string
	Text    string
	Note    string
}

func (v Value) Min() float64 {
	if len(v.Samples) == 0 {
		return math.NaN()
	}
	m := v.Samples[0]
	for _, x := range v.Samples[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func (v Value) Max() float64 {
	if len(v.Samples) == 0 {
		return math.NaN()
	}
	m := v.Samples[0]
	for _, x := range v.Samples[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func (v Value) Mean() float64 {
	if len(v.Samples) == 0 {
		return math.NaN()
	}
	sum := 0.0
	for _, x := range v.Samples {
		sum += x
	}
	return sum / float64(len(v.Samples))
}

type Observation struct {
	Name    string
	Title   string
	Section string
	Extract func(SystemInfo, []CapturedCommand) (Value, bool)
	Recall  func(Value) []Question
}

type Snapshot struct {
	Sections    []SnapshotSection
	NotCaptured []Observation
	Sources     []string
	Values      map[string]Value
}

type SnapshotSection struct {
	Title string
	Items []SnapshotItem
}

type SnapshotItem struct {
	Title string
	Value Value
}

func (s *Session) Snapshot() Snapshot {
	grouped := map[string][]SnapshotItem{}
	values := map[string]Value{}
	var notCaptured []Observation
	for _, obs := range s.Investigation.Observations {
		v, ok := obs.Extract(s.System, s.Captured)
		if ok {
			grouped[obs.Section] = append(grouped[obs.Section], SnapshotItem{Title: obs.Title, Value: v})
			values[obs.Name] = v
		} else {
			notCaptured = append(notCaptured, obs)
		}
	}
	var sections []SnapshotSection
	for _, name := range []string{"Utilization", "Saturation", "Errors"} {
		if items, ok := grouped[name]; ok {
			sections = append(sections, SnapshotSection{Title: name, Items: items})
		}
	}
	srcSet := map[string]struct{}{}
	for _, c := range s.Captured {
		srcSet[c.Cmd] = struct{}{}
	}
	srcs := make([]string, 0, len(srcSet))
	for c := range srcSet {
		srcs = append(srcs, c)
	}
	sort.Strings(srcs)
	return Snapshot{Sections: sections, NotCaptured: notCaptured, Sources: srcs, Values: values}
}

func (s Snapshot) Print() {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	if len(s.Sources) == 0 {
		fmt.Println("No commands captured yet.")
		fmt.Println()
		return
	}
	fmt.Printf("Captured from: %s\n", strings.Join(s.Sources, "; "))
	fmt.Println()
	titleWidth := s.itemTitleWidth()
	for _, sec := range s.Sections {
		fmt.Println(sec.Title)
		for _, it := range sec.Items {
			fmt.Printf("  %-*s %s\n", titleWidth, it.Title, formatValue(it.Value))
		}
		fmt.Println()
	}
	if len(s.NotCaptured) > 0 {
		fmt.Println("Not captured (no data yet):")
		for _, o := range s.NotCaptured {
			fmt.Printf("  %s\n", o.Title)
		}
		fmt.Println()
	}
}

func (s Snapshot) itemTitleWidth() int {
	width := 0
	for _, sec := range s.Sections {
		for _, it := range sec.Items {
			if len(it.Title) > width {
				width = len(it.Title)
			}
		}
	}
	if width < 30 {
		return 30
	}
	return width
}

func formatValue(v Value) string {
	var s string
	switch {
	case v.Text != "":
		s = v.Text
	case len(v.Samples) > 0:
		parts := make([]string, len(v.Samples))
		for i, x := range v.Samples {
			parts[i] = formatNumber(x, v.Unit)
		}
		s = strings.Join(parts, ", ")
		s += fmt.Sprintf("  (max %s, mean %s)",
			formatNumber(v.Max(), v.Unit),
			formatNumber(v.Mean(), v.Unit))
	default:
		s = formatNumber(v.Number, v.Unit)
	}
	if v.Note != "" {
		s += "  (" + v.Note + ")"
	}
	return s
}

func formatNumber(n float64, unit string) string {
	if math.IsNaN(n) {
		return "—"
	}
	abs := n
	if abs < 0 {
		abs = -abs
	}
	var formatted string
	switch {
	case abs >= 100 || abs == 0:
		formatted = fmt.Sprintf("%.0f", n)
	case abs >= 10:
		formatted = fmt.Sprintf("%.1f", n)
	default:
		formatted = fmt.Sprintf("%.2f", n)
	}
	return formatted + unit
}
