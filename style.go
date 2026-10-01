package main

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

// The visual system is deliberately small: the 16 basic ANSI colours (so the
// user's terminal theme picks the shades), three text levels (bold, normal,
// faint), one accent colour for the rail and prompts, and green/yellow/red
// only in feedback. Colour never carries meaning alone: every styled status
// also has a symbol or word, so output reads the same with colour off.
//
// Styles are applied only to use-tool's own text. Command output streams
// through sanitizingWriter untouched.

// uiConfig controls how use-tool decorates its own output. The zero value is
// plain output, which is what tests (and piped output) see.
type uiConfig struct {
	// color emits SGR styling (bold, faint, colours, reverse video).
	color bool
	// rich means stdout is an interactive terminal: tutor prose is wrapped to
	// the terminal and the window title may be set.
	rich bool
	// titles allows the window-title escape sequence.
	titles bool
	// gutter draws the accent rail beside tutor text. It is turned on by the
	// interactive modes (guide, practice) when rich is set.
	gutter bool
}

var ui uiConfig

const gutterRail = "│"

// configureUI detects terminal capabilities and removes the --no-color flag
// from args, returning the remaining arguments.
func configureUI(args []string) []string {
	noColor := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--no-color" || arg == "--no-colour" {
			noColor = true
			continue
		}
		rest = append(rest, arg)
	}
	ui = detectUI(os.Getenv, isTerminal(os.Stdout), noColor)
	return rest
}

// detectUI decides colour and layout from the environment. Colour is off for
// NO_COLOR, TERM=dumb, a non-terminal stdout, or --no-color, and
// USE_TOOL_COLOR=always|never overrides the automatic choice.
func detectUI(getenv func(string) string, stdoutTTY, noColorFlag bool) uiConfig {
	dumb := getenv("TERM") == "dumb"
	color := stdoutTTY && !dumb && getenv("NO_COLOR") == ""
	switch strings.ToLower(strings.TrimSpace(getenv("USE_TOOL_COLOR"))) {
	case "always", "on", "yes", "1":
		color = true
	case "never", "off", "no", "0":
		color = false
	}
	if noColorFlag {
		color = false
	}
	return uiConfig{
		color:  color,
		rich:   stdoutTTY,
		titles: stdoutTTY && !dumb,
	}
}

// enableGutter turns on the rail for an interactive session.
func enableGutter() {
	ui.gutter = ui.rich
}

func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&termios)),
	)
	return errno == 0
}

// ----- named styles -----

func sgr(code, s string) string {
	if !ui.color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string      { return sgr("1", s) }
func faint(s string) string     { return sgr("2", s) }
func underline(s string) string { return sgr("4", s) }
func reverse(s string) string   { return sgr("7", s) }
func accent(s string) string    { return sgr("36", s) }
func good(s string) string      { return sgr("32", s) }
func warn(s string) string      { return sgr("33", s) }
func bad(s string) string       { return sgr("31", s) }

// ----- width -----

// visibleWidth counts the terminal columns s occupies, skipping ANSI escape
// sequences (CSI and OSC) and other control bytes. Tabs count as 4 columns.
func visibleWidth(s string) int {
	cols := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\t':
			cols += 4
		case r < 0x20 || r == 0x7f:
		default:
			cols++
		}
	}
	return cols
}

// skipEscape returns the index just past the escape sequence starting at i.
func skipEscape(s string, i int) int {
	i++ // ESC
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		for i++; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return i
	case ']':
		for i++; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return i
	default:
		return i + 1
	}
}

// padRight pads s with spaces to width visible columns.
func padRight(s string, width int) string {
	if pad := width - visibleWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// ----- gutter -----

func gutterPrefix() string {
	if !ui.gutter {
		return ""
	}
	return accent(gutterRail) + " "
}

func gutterWidth() int {
	if !ui.gutter {
		return 0
	}
	return 2
}

// gutterize puts the rail in front of every non-empty line of s. Blank lines
// stay blank so they separate blocks.
func gutterize(s string) string {
	if !ui.gutter || s == "" {
		return s
	}
	prefix := gutterPrefix()
	var b strings.Builder
	for len(s) > 0 {
		line, rest, hasNewline := strings.Cut(s, "\n")
		if line != "" {
			b.WriteString(prefix)
			b.WriteString(line)
		}
		if hasNewline {
			b.WriteByte('\n')
		}
		s = rest
	}
	return b.String()
}

// tutorf prints use-tool's own text, on the rail when it is active. Each call
// must start at the beginning of a line.
func tutorf(format string, a ...any) {
	fmt.Fprint(os.Stdout, gutterize(fmt.Sprintf(format, a...)))
}

func tutorln(a ...any) {
	fmt.Fprint(os.Stdout, gutterize(fmt.Sprintln(a...)))
}

// railBreak prints a paragraph break inside a block: the bare rail when it is
// active, otherwise a blank line.
func railBreak() {
	if ui.gutter {
		fmt.Fprintln(os.Stdout, accent(gutterRail))
		return
	}
	fmt.Fprintln(os.Stdout)
}

// noteLine prints a one-line note about a command (its exit status, why it
// was not run) to w. On the rail it is faint; in plain output it is
// bracketed so it can't be mistaken for command output.
func noteLine(w *os.File, text string) {
	if ui.gutter {
		fmt.Fprintln(w, gutterPrefix()+faint(text))
		return
	}
	fmt.Fprintf(w, "[%s]\n", text)
}

// ----- prose -----

// proseWidth is the column budget for tutor prose to the right of the rail:
// 72 columns, or less on a narrow terminal.
func proseWidth() int {
	w := 72
	if tw := selectorTerminalWidth() - gutterWidth(); tw < w {
		w = tw
	}
	if w < 20 {
		w = 20
	}
	return w
}

// printProse prints a block of tutor prose. In rich mode the text is
// re-flowed to proseWidth (keeping paragraph breaks and bullet items);
// otherwise it is printed as written. style is applied after wrapping.
func printProse(text string, style func(string) string) {
	if style == nil {
		style = func(s string) string { return s }
	}
	if !ui.rich {
		for _, line := range strings.Split(text, "\n") {
			tutorln(style(line))
		}
		return
	}
	for _, line := range reflowProse(text, proseWidth()) {
		if line == "" {
			railBreak()
			continue
		}
		tutorln(style(line))
	}
}

// printHanging prints text after first, wrapping continuation lines under the
// text rather than under first (e.g. an option number or a ✓ mark). Wrapping
// happens only in rich mode.
func printHanging(first, text string, style func(string) string) {
	if style == nil {
		style = func(s string) string { return s }
	}
	if !ui.rich {
		tutorln(first + style(text))
		return
	}
	indent := strings.Repeat(" ", visibleWidth(first))
	for i, line := range wrapText(text, proseWidth()-visibleWidth(first)) {
		lead := indent
		if i == 0 {
			lead = first
		}
		tutorln(lead + style(line))
	}
}

// reflowProse re-wraps hand-broken prose. Blank lines separate paragraphs
// (returned as ""); single newlines are joined. Lines starting with a bullet
// ("• ", "- ", "* ") start a new item whose wrapped lines align under the
// item text, and indented lines continue the current item.
func reflowProse(text string, width int) []string {
	type item struct {
		first, rest string
		words       []string
	}
	var out []string
	for pi, para := range strings.Split(strings.TrimRight(text, "\n"), "\n\n") {
		if pi > 0 {
			out = append(out, "")
		}
		var items []item
		for _, line := range strings.Split(para, "\n") {
			trimmed := strings.TrimLeft(line, " ")
			indent := len(line) - len(trimmed)
			if marker := bulletMarker(trimmed); marker != "" {
				lead := strings.Repeat(" ", indent)
				items = append(items, item{
					first: lead + marker,
					rest:  strings.Repeat(" ", indent+visibleWidth(marker)),
					words: strings.Fields(trimmed[len(marker):]),
				})
				continue
			}
			if len(items) == 0 || (indent == 0 && items[len(items)-1].first != "") {
				items = append(items, item{})
			}
			cur := &items[len(items)-1]
			cur.words = append(cur.words, strings.Fields(trimmed)...)
		}
		for _, it := range items {
			if len(it.words) == 0 {
				continue
			}
			for i, line := range wrapText(strings.Join(it.words, " "), width-visibleWidth(it.first)) {
				lead := it.rest
				if i == 0 {
					lead = it.first
				}
				out = append(out, lead+line)
			}
		}
	}
	return out
}

func bulletMarker(s string) string {
	for _, marker := range []string{"• ", "- ", "* "} {
		if strings.HasPrefix(s, marker) {
			return marker
		}
	}
	return ""
}

// ----- window title -----

var windowTitlePushed atomic.Bool

// setWindowTitle sets the terminal window title, saving the previous one the
// first time so restoreWindowTitle can put it back.
func setWindowTitle(title string) {
	if !ui.titles {
		return
	}
	if windowTitlePushed.CompareAndSwap(false, true) {
		fmt.Fprint(os.Stdout, "\x1b[22;0t")
	}
	fmt.Fprintf(os.Stdout, "\x1b]0;%s\x07", sanitizeTerminalBytes([]byte(title)))
}

func restoreWindowTitle() {
	if windowTitlePushed.CompareAndSwap(true, false) {
		fmt.Fprint(os.Stdout, "\x1b[23;0t")
	}
}

// styledText is a run of text with its own style.
type styledText struct {
	Text  string
	Style func(string) string
}

// printHangingStyled is printHanging for text whose parts carry different
// styles. Wrapping is done on the plain text first, and each word is styled
// afterwards so styles survive line breaks.
func printHangingStyled(first string, parts ...styledText) {
	var plain []string
	var styles []func(string) string
	for _, part := range parts {
		for _, word := range strings.Fields(part.Text) {
			plain = append(plain, word)
			styles = append(styles, part.Style)
		}
	}
	text := strings.Join(plain, " ")
	var lines []string
	if ui.rich {
		lines = wrapText(text, proseWidth()-visibleWidth(first))
	} else {
		lines = []string{text}
	}
	indent := strings.Repeat(" ", visibleWidth(first))
	next := 0
	for i, line := range lines {
		words := strings.Fields(line)
		for j, word := range words {
			if next < len(styles) && styles[next] != nil {
				words[j] = styles[next](word)
			}
			next++
		}
		lead := indent
		if i == 0 {
			lead = first
		}
		tutorln(lead + strings.Join(words, " "))
	}
}
