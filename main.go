package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	appName = "use-tool"
	version = "0.2.0"
)

const (
	maxCaptureBytes  = 1 << 20 // per-command output cap (1 MB)
	maxCapturedItems = 50      // per-session command-history cap
)

var stdin = bufio.NewReader(os.Stdin)

func main() {
	if len(os.Args) < 2 {
		usage(2)
	}
	exitOnSigint()

	switch os.Args[1] {
	case "guide":
		cmdGuide(os.Args[2:])
	case "practice":
		cmdPractice(os.Args[2:])
	case "commands":
		cmdCommands(os.Args[2:])
	case "list":
		cmdList()
	case "version", "--version", "-v":
		fmt.Printf("%s %s\n", appName, version)
	case "help", "--help", "-h":
		usage(0)
	default:
		unknownCommand(os.Args[1])
	}
}

var topLevelCommands = []string{"guide", "practice", "commands", "list", "version", "help"}

func unknownCommand(name string) {
	fmt.Fprintf(os.Stderr, "unknown command %q", name)
	if suggestion, ok := suggestCommand(name, topLevelCommands); ok {
		fmt.Fprintf(os.Stderr, "; did you mean %q?", suggestion)
	}
	fmt.Fprintln(os.Stderr)
	usage(2)
}

func usage(code int) {
	out := os.Stdout
	if code != 0 {
		out = os.Stderr
	}
	fmt.Fprintf(out, `%s %s — practice the USE method on a live Linux system

New here? Run: use-tool guide cpu

Usage:
  use-tool guide <resource>     Walk through the USE method as a guided checklist
  use-tool practice <resource>  Free-form investigation, then comprehension assessment
  use-tool commands <resource>  Print a reference of relevant commands and what they show
  use-tool list                 Show available resources
  use-tool version              Print version
  use-tool help                 This message

Available resources: %s
`, appName, version, strings.Join(resourceNames(), ", "))
	os.Exit(code)
}

func cmdList() {
	for _, name := range resourceNames() {
		fmt.Printf("  %-10s  %s\n", name, investigations[name].Title)
	}
}

func cmdCommands(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: use-tool commands <resource>")
		os.Exit(2)
	}
	inv, err := getInvestigation(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	printCommands(inv, detectSystem())
}

type CapturedCommand struct {
	Cmd    string
	Output string
}

type Session struct {
	Investigation *Investigation
	System        SystemInfo
	Captured      []CapturedCommand
}

type SystemInfo struct {
	NumCPU     int
	HasMpstat  bool
	HasPidstat bool
	HasSar     bool
	HasPSI     bool
}

func detectSystem() SystemInfo {
	return SystemInfo{
		NumCPU:     runtime.NumCPU(),
		HasMpstat:  haveCmd("mpstat"),
		HasPidstat: haveCmd("pidstat"),
		HasSar:     haveCmd("sar"),
		HasPSI:     fileExists("/proc/pressure/cpu"),
	}
}

func haveCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCommand(cmdStr string) CapturedCommand {
	cmd := exec.Command("sh", "-c", cmdStr)
	buf := &cappedBuffer{limit: maxCaptureBytes}
	cmd.Stdout = io.MultiWriter(os.Stdout, buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, buf)
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			fmt.Fprintln(os.Stderr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			fmt.Fprintf(os.Stderr, "[command exited with status %d]\n", exitErr.ExitCode())
		} else {
			fmt.Fprintf(os.Stderr, "[command failed: %v]\n", err)
		}
	}
	return CapturedCommand{Cmd: cmdStr, Output: buf.String()}
}

func (s *Session) runAndCapture(cmdStr string) CapturedCommand {
	c := runCommand(cmdStr)
	s.appendCaptured(c)
	return c
}

// cappedBuffer accepts unlimited writes (so the user still sees full output on
// stdout via MultiWriter) but only retains the first `limit` bytes for capture.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string {
	if c.truncated {
		return c.buf.String() + "\n[output truncated at 1 MB]\n"
	}
	return c.buf.String()
}

func (c *cappedBuffer) Len() int {
	return c.buf.Len()
}

func (c *cappedBuffer) Bytes() []byte {
	return c.buf.Bytes()
}

// appendCaptured records a command in the session, dropping the oldest entry
// (with a one-line note) once the history cap is reached.
func (s *Session) appendCaptured(c CapturedCommand) {
	if len(s.Captured) >= maxCapturedItems {
		dropped := s.Captured[0].Cmd
		s.Captured = append(s.Captured[:0], s.Captured[1:]...)
		fmt.Fprintf(os.Stderr, "(history cap reached; dropped oldest: %q)\n", dropped)
	}
	s.Captured = append(s.Captured, c)
}

func readLine() (string, bool) {
	line, err := stdin.ReadString('\n')
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(line), true
}

func stripCopiedShellPrompt(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "$" {
		return "", true
	}
	if strings.HasPrefix(line, "$ ") {
		return strings.TrimSpace(strings.TrimPrefix(line, "$")), true
	}
	return line, false
}

func exitOnSigint() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT)
	go func() {
		for range ch {
			fmt.Fprintln(os.Stderr, "\nInterrupted. Exiting.")
			os.Exit(130)
		}
	}()
}

func requireInteractive(mode string) {
	if stdinIsTerminal() {
		return
	}
	fmt.Fprintf(os.Stderr, "use-tool %s requires an interactive terminal on stdin.\n", mode)
	fmt.Fprintln(os.Stderr, "Run `use-tool commands cpu` for a non-interactive command reference.")
	os.Exit(2)
}

func stdinIsTerminal() bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		os.Stdin.Fd(),
		uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&termios)),
	)
	return errno == 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func suggestCommand(input string, candidates []string) (string, bool) {
	best := ""
	bestDistance := 0
	for _, candidate := range candidates {
		dist := levenshtein(input, candidate)
		if best == "" || dist < bestDistance {
			best = candidate
			bestDistance = dist
		}
	}
	if best == "" || bestDistance > 2 {
		return "", false
	}
	return best, true
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(
				cur[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev = cur
	}
	return prev[len(b)]
}

func minInt(xs ...int) int {
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
