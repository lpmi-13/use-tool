package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const version = "0.2.0"

var stdin = bufio.NewReader(os.Stdin)

func main() {
	if len(os.Args) < 2 {
		usage(2)
	}
	rand.Seed(time.Now().UnixNano())
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
		fmt.Printf("use-method %s\n", version)
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
	fmt.Fprintf(out, `use-method %s — practice the USE method on a live Linux system

Usage:
  cpu-use guide <resource>     Walk through the USE method as a guided checklist
  cpu-use practice <resource>  Free-form investigation, then comprehension assessment
  cpu-use commands <resource>  Print a reference of relevant commands and what they show
  cpu-use list                 Show available resources
  cpu-use version              Print version
  cpu-use help                 This message

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
		fmt.Fprintln(os.Stderr, "usage: cpu-use commands <resource>")
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
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
	return CapturedCommand{Cmd: cmdStr, Output: buf.String()}
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
