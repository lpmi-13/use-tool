package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

var errSelectorQuit = errors.New("selector quit")

type menuOption struct {
	Value   string
	Label   string
	Summary string
}

type menuSpec struct {
	Title    string
	Options  []menuOption
	Fallback string
}

type menuSelectorFunc func(menuSpec) (string, error)

var menuSelector menuSelectorFunc = terminalMenuSelector
var hasRenderedMenuSelector bool

func chooseSubcommand() (string, error) {
	return selectMenuValue(menuSpec{
		Title:    "use-tool - choose a command",
		Options:  subcommandMenuOptions(),
		Fallback: "help",
	})
}

func chooseResourceForCommand(command string) (string, error) {
	return selectMenuValue(menuSpec{
		Title:    fmt.Sprintf("use-tool %s - choose a resource", command),
		Options:  resourceMenuOptions(command),
		Fallback: "cpu",
	})
}

func selectMenuValue(spec menuSpec) (string, error) {
	if len(spec.Options) == 0 {
		return spec.Fallback, nil
	}
	if menuSelector == nil {
		return spec.Fallback, nil
	}
	if hasRenderedMenuSelector {
		fmt.Println()
	}
	hasRenderedMenuSelector = true
	return menuSelector(spec)
}

func exitSelectionError(err error) {
	if errors.Is(err, errSelectorQuit) {
		os.Exit(130)
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func subcommandMenuOptions() []menuOption {
	return []menuOption{
		{Value: "guide", Label: "guide", Summary: "Walk through the USE method as a guided checklist"},
		{Value: "practice", Label: "practice", Summary: "Free-form investigation, then understanding check"},
		{Value: "commands", Label: "commands", Summary: "Print relevant commands and what they show"},
		{Value: "list", Label: "list", Summary: "Show available resources"},
		{Value: "version", Label: "version", Summary: "Print version"},
		{Value: "help", Label: "help", Summary: "Show usage"},
	}
}

func resourceMenuOptions(command string) []menuOption {
	names := resourceNames()
	out := make([]menuOption, 0, len(names))
	for _, name := range names {
		inv := investigations[name]
		if command == "guide" && !hasGuidedWalkthrough(name, inv) {
			continue
		}
		out = append(out, menuOption{
			Value:   name,
			Label:   name,
			Summary: singleLine(inv.Title),
		})
	}
	return out
}

func hasGuidedWalkthrough(name string, inv *Investigation) bool {
	return inv != nil && name != "system" && inv.StepsFn != nil
}

func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func terminalMenuSelector(spec menuSpec) (string, error) {
	if !stdinIsTerminal() {
		return spec.Fallback, nil
	}
	restore, ok := enterRawInput()
	if !ok {
		return spec.Fallback, nil
	}
	defer restore()

	selected := 0
	showHelp := false
	renderedLines := renderMenuSelector(spec.Title, spec.Options, selected, showHelp)
	for {
		key, err := readTerminalKey()
		if err != nil {
			fmt.Println()
			return "", err
		}
		switch key.Key {
		case keyUp, keyDown:
			selected = moveSelection(selected, len(spec.Options), key.Key)
			clearRenderedBlock(renderedLines)
			renderedLines = renderMenuSelector(spec.Title, spec.Options, selected, showHelp)
		case keyDigit:
			if key.Digit >= 1 && key.Digit <= len(spec.Options) {
				selected = key.Digit - 1
				clearRenderedBlock(renderedLines)
				renderedLines = renderMenuSelector(spec.Title, spec.Options, selected, showHelp)
				continue
			}
			fmt.Print("\a")
		case keyHelp:
			showHelp = !showHelp
			clearRenderedBlock(renderedLines)
			renderedLines = renderMenuSelector(spec.Title, spec.Options, selected, showHelp)
		case keyRedraw:
			renderedLines = redrawFullScreen(func() int {
				return renderMenuSelector(spec.Title, spec.Options, selected, showHelp)
			})
		case keyEnter:
			fmt.Println()
			return spec.Options[selected].Value, nil
		case keyQuit:
			fmt.Println()
			return "", errSelectorQuit
		default:
			fmt.Print("\a")
		}
	}
}

func renderMenuSelector(title string, options []menuOption, selected int, showHelp bool) int {
	width := selectorTerminalWidth()
	lines := printSelectorLine(title, width, true)
	for i, option := range options {
		cursor := " "
		if i == selected {
			cursor = ">"
		}
		line := fmt.Sprintf("%s %d. %-10s %s", cursor, i+1, option.Label, option.Summary)
		lines += printSelectorLine(line, width, true)
	}
	return lines + printSelectorHelp(menuSelectorHelp(len(options), showHelp), width)
}

func menuSelectorHelp(optionCount int, showHelp bool) []string {
	if !showHelp {
		return []string{fmt.Sprintf("Up/k Down/j move | 1-%d jump | Enter choose | q quit | ? help", optionCount)}
	}
	return []string{
		"Keys:",
		"  Up/k, Down/j  move between options",
		fmt.Sprintf("  1-%d           jump to an option", optionCount),
		"  Enter         choose the highlighted option",
		"  q             quit",
		"  ?             hide help",
	}
}
