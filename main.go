package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

const version = "0.2.0"

const (
	maxCaptureBytes  = 1 << 20 // per-command output cap (1 MB)
	maxCapturedItems = 50      // per-session command-history cap
)

var stdin = bufio.NewReader(os.Stdin)

func main() {
	if len(os.Args) < 2 {
		usage(2)
	}
	swallowSigint()

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
		fmt.Printf("use-tool %s\n", version)
	case "help", "--help", "-h":
		usage(0)
	default:
		usage(2)
	}
}

func usage(code int) {
	out := os.Stdout
	if code != 0 {
		out = os.Stderr
	}
	fmt.Fprintf(out, `use-tool %s — practice the USE method on a live Linux system

Usage:
  use-tool guide <resource>     Walk through the USE method as a guided checklist
  use-tool practice <resource>  Free-form investigation, then comprehension assessment
  use-tool commands <resource>  Print a reference of relevant commands and what they show
  use-tool list                 Show available resources
  use-tool version              Print version
  use-tool help                 This message

Available resources: %s
`, version, strings.Join(resourceNames(), ", "))
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
	printCommands(inv)
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
	_ = cmd.Run()
	return CapturedCommand{Cmd: cmdStr, Output: buf.String()}
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

func swallowSigint() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT)
	go func() {
		for range ch {
		}
	}()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
