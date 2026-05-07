# Use Tool (Utilization, Saturation, Errors)

A learning harness for practicing Brendan Gregg's USE method on a live Linux
system. The tool captures the commands you run, parses their output into a
structured snapshot, and asks targeted questions about what you observed —
without making assumptions about what state the system is "supposed" to be in.

Currently covers **CPU** and **memory**, with disk I/O and network designed
to slot in as sibling resources without changing the core code.

## What it does

Three modes plus a reference cheatsheet:

- **`guide`** — a step-by-step walkthrough of the USE method. At each step,
  you run a suggested command on your live system; the harness asks an inline
  comprehension question and shows teaching commentary, then advances. Ends
  with a snapshot of everything observed.
- **`practice`** — a free-form REPL. Run any commands you want; type
  `report` to print the current snapshot, `evaluate` to be asked a sample of
  comprehension, recall, and synthesis questions, `commands` for the
  cheatsheet, `exit` to quit.
- **`commands`** — a semi-verbose reference of CPU-related commands grouped
  by Utilization / Saturation / Errors, with one or two lines on what each
  shows and when to use it.

The snapshot reports raw observations (load averages with NumCPU ratio, mean
and per-CPU range of `%idle`, vmstat run-queue and `wa` samples, dmesg keyword
counts) without labelling the system as "saturated" or "healthy". The learner
forms that judgement; the tool only checks they read the data correctly.

## Question types

| Type | Tests | Answer source |
|---|---|---|
| Comprehension | "Can you read this output?" | Fixed (e.g. what does `%iowait` mean) |
| Recall | "Did you actually look at it?" | The learner's captured data |
| Synthesis | "Do these numbers cohere?" | Logical rules over observed values |

No question asks "what state is this system in" — that classification was
removed deliberately so the tool stays system-agnostic.

## Requirements

- Linux (uses `/proc`, kernel-specific commands).
- Go 1.21+ to build.
- Optional but recommended: `sysstat` for `mpstat`, `pidstat`, `sar`. The tool
  detects what's installed and adapts (e.g. the guided walkthrough skips the
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

# Command reference (no REPL)
use-tool commands cpu

# List available resources
use-tool list
```

Inside `practice` mode you can also run `commands` and `report` as REPL
builtins.

## Project layout

```
main.go            entrypoint, dispatch, REPL primitives, system detection
investigation.go   generic types (Investigation, Question, GuideStep, ...)
                   plus the registry, askQuestion, printCommands
snapshot.go        Value, Observation, Snapshot, formatting
cpu.go             CPU-specific: observations, extractors, recall fns,
                   synthesis rules, command reference, guide steps
memory.go          Memory-specific: same shape as cpu.go
guide.go           guided walkthrough flow
practice.go        free-form REPL flow
*_test.go          unit tests for parsers, extractors, synthesis rules
```

To add another resource (e.g. disk I/O): create `disk.go` mirroring
`cpu.go`/`memory.go`'s shape and register it in `investigations` in
`investigation.go`. Nothing else changes.

## Scope intentionally left out

- **No load generation.** The tool runs on whatever system you're on. It does
  not (and will not) try to put the system into a known state. Pair it with a
  stress tool of your choice (`stress-ng`, `yes >/dev/null &`, etc.) in
  another terminal if you want to see non-idle states.
- **No "what state is this system in" classification.** That construct
  hardcodes scenarios. Synthesis questions check relationships between
  observations instead.
- **No PTY.** Capture is via stdout/stderr tee. For commands that need a real
  terminal, use batch flags (`top -bn1`, `mpstat -P ALL 1 3`).
