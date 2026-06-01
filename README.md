# Use Tool (Utilization, Saturation, Errors)

> some good background on this topic is [this article](https://netflixtechblog.com/linux-performance-analysis-in-60-000-milliseconds-accc10403c55), as well as [this very comprehensive intro](https://www.brendangregg.com/linuxperf.html)

A learning harness for practicing Brendan Gregg's USE method on a live Linux
system. The tool captures the commands you run, parses their output into a
structured snapshot, and asks specific questions about what you saw —
without making guesses about what state the system is "supposed" to be in.

Currently covers **CPU**, **memory**, **disk I/O**, and **network**, plus a
**system** practice target that diagnoses all four together.

## What it does

Three modes plus a reference cheatsheet:

- **`guide`** — a step-by-step walkthrough of the USE method. At each step,
  you run a suggested command on your live system; the harness asks an inline
  comprehension question and shows teaching notes, then advances. Ends
  with a snapshot of everything you saw.
- **`practice`** — a free-form REPL. Run any commands you want; type
  `report` to print the current snapshot, `diagnose` to check your USE
  verdicts against the signals you captured, `commands` for the cheatsheet,
  `help`, or `exit` to quit. At the pseudo-shell prompt, Control-C clears a
  partially typed line, Control-C on an empty line exits the activity, and
  Control-L clears the screen and redraws the current input.
- **`commands`** — a short-to-medium reference for the selected resource,
  grouped by Utilization / Saturation / Errors, with one or two lines on what
  each command shows and when to use it.

The snapshot reports observations extracted from the commands you captured:
load and run-queue signals, memory availability and pressure, disk queueing and
I/O errors, network drops/retransmits, and kernel-log findings. The tool keeps
the raw observations visible so you can judge the system from the evidence.

## Checks and diagnosis

Guided walkthroughs ask inline comprehension checks about the command output
you just captured: column meanings, units, and what a signal can or cannot
prove.

In practice mode, `diagnose` asks you to state a USE verdict for each relevant
dimension and cite the observations that support it. Grading compares your
claim and citations with the signals captured in your session, then suggests
useful next commands when evidence is thin. It does not assume a hidden
"correct" host state outside the data you gathered.

## Requirements

- Linux (uses `/proc`, kernel-specific commands).
- Go 1.21+ to build.
- Optional but recommended: `sysstat` for `mpstat`, `pidstat`, `sar`. The tool
  detects what's installed and adjusts (for example, the guided walkthrough skips the
  per-CPU step if `mpstat` is missing).

## Install

```sh
go install github.com/lpmi-13/use-tool@latest
```

This places a `use-tool` binary in `$(go env GOBIN)` (or `$(go env GOPATH)/bin`).
Pre-built binaries for tagged releases are also published on the
[Releases page](https://github.com/lpmi-13/use-tool/releases).

## Build from source

```sh
go build -o use-tool ./...
```

Produces a single static binary in the project root.

## Run

```sh
# Step-by-step USE walkthrough
use-tool guide cpu

# Free-form practice + assessment
use-tool practice cpu

# Whole-system practice across CPU, memory, disk, and network
use-tool practice system

# Command reference (no REPL)
use-tool commands cpu

# List available resources
use-tool list
```

Inside `practice` mode you can also run `report`, `commands`, `diagnose`,
`help`, and `exit` as REPL builtins.

## Project layout

```
main.go            entrypoint, dispatch, REPL primitives, system detection
investigation.go   generic types (Investigation, Question, GuideStep, ...)
                   plus the registry, askQuestion, printCommands
diagnose.go        practice-mode USE verdict and evidence checks
snapshot.go        Value, Observation, Snapshot, formatting
synopsis.go        end-of-guide synopsis from captured USE signals
version.go         version resolution for release tags and Go module builds
cpu.go             CPU-specific: observations, extractors, question fns,
                   diagnosis heuristics, command reference, guide steps
memory.go          Memory-specific: same shape as cpu.go
disk.go            Disk I/O-specific: same shape as cpu.go
network.go         Network-specific: same shape as cpu.go
guide.go           guided walkthrough flow
practice.go        free-form REPL flow
*_test.go          unit tests for parsers, extractors, diagnosis, prompts
```

To add another resource: create a `<name>.go` file mirroring the existing
shape (Investigation struct with StepsFn / Observations / Commands, plus
observation extractors and guide questions) and register it in
`investigations` in `investigation.go`. Nothing else changes.

## Scope intentionally left out

- **No load generation.** The tool runs on whatever system you're on. It does
  not (and will not) try to put the system into a known state. Pair it with a
  stress tool of your choice (`stress-ng`, `yes >/dev/null &`, etc.) in
  another terminal if you want to see non-idle states.
- **No hidden state oracle.** `diagnose` grades your claims against the
  observations captured in this session. If you did not capture the relevant
  signal, the right answer is usually "not enough data".
- **No PTY for captured commands.** Capture is via stdout/stderr tee. For
  commands that need a real terminal, use batch flags (`top -bn1`,
  `mpstat -P ALL 1 3`).
