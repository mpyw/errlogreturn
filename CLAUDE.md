# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## Project overview

**errlogreturn** is a Go linter that reports an error that is both logged and returned on one path. It is built on [`go/analysis`](https://pkg.go.dev/golang.org/x/tools/go/analysis) and [`buildssa`](https://pkg.go.dev/golang.org/x/tools/go/analysis/passes/buildssa).

**Every judgement leans toward silence.** A false report teaches people to ignore the linter. A missed one costs a duplicated log line. When a choice has to be made under uncertainty, the rule here is to say nothing.

## Architecture

```text
analyzer.go            Analyzer, the -sinks flag, ErrNoSSA
cmd/errlogreturn/      singlechecker entry point
internal/              the engine: one flat package, one namespace per file
  run.go               Run: summarize every function, export facts, check
  checker.go           the per-pass state, with each stage's book embedded
  summary.go           function summaries: what each input carries and logs
  callee.go            call resolution, and what a call carries
  walker.go            the backward trace from a value to its origins
  sink.go              which instructions log, and what
  check.go             the forward path walk from a log call to a return
  config.go, fact.go   Config and the exported Fact
internal/typeutil/     error-type questions, the types.Func behind an SSA function
internal/known/        what is known about specific libraries
internal/store/        the write index of a function, and reaching stores
internal/directive/    //errlogreturn: comments
```

The engine is flat because its parts are mutually recursive. A summary asks which calls log, which asks for the callee's summary, which runs the walker, which asks what a call carries. Splitting that cycle across packages would only add exports. Everything that stands alone has its own package. **Keep new code out of the flat package unless it joins that cycle.**

### declscope

The repository is checked by [declscope](https://github.com/mpyw/declscope) with `qualify: ondemand` and `exported: true` (`.declscope.yaml`). In `internal`, every file is its own namespace, so every package-level name carries its file's namespace, and a method written outside its receiver's file does too.

| Rule | Why |
| --- | --- |
| No `//declscope:core` | A core file hides its names from the naming rule. The model is split by type instead, so that names read naturally |
| A struct shared across files states `//declscope:package` on the type | Its fields inherit it |
| A field not read from another file carries `//declscope:private` | Each field takes the smallest scope it needs |
| A crossing field carries no directive | A field restating its type's scope is inert, and declscope reports it |
| Private fields sit at the bottom of the struct | The fields another stage reads are the struct's interface, and come first |
| Each stage's state is a `...Book` struct embedded in `checker` | Call sites read `c.summaries`, and reaching another stage's state is a boundary crossing |

`walker` is the one type the other files never name. They reach it through `c.walkerErrs` and `c.walkerInputs`, so the type and all of its fields stay private to walker.go.

## The analysis

It is a taint analysis. The source is an error value. It has two exits, a logger and a `return`, and a report is made when one value reaches both on one path.

### Summaries

Each function is reduced to a `summary` over its inputs: the parameters, receiver first, then free variables.

| Field | Meaning | Kind |
| --- | --- | --- |
| `flows[i]` | The results input `i` may carry into | May |
| `logs[i]` | Input `i` is logged on every path that returns | Must |

Named functions export their summary as a `Fact`, so a helper in another package is recognized with no configuration. A function literal is summarized in the pass that holds it.

- **`logs` is a must-analysis.** A helper that logs only under a condition is not a logger. The one exception is a condition that the input is `nil` (`summaryNilEdge`), since a nil error has nothing to log. This is what lets `func logIfErr(err error) { if err != nil { log(err) } }` count.
- **`logs` only covers inputs that can carry an error** (`typeutil.MayCarry`). A log call logs its receiver too, and without this rule every function that takes a `*zerolog.Logger` would be said to log it. An interface counts only when `error` implements it, so `Reporter` does not.
- **Recursion reads an empty summary.** The entry is stored before it is filled. A recursive call can only make a function log or carry less. A fixpoint iteration was not added, since it would only find more reports in code that is rare.

### Origins and the path walk

`walker` traces a value backward. In the errors mode it collects every error-typed value on the way. A logged value and a returned value are reported when those sets meet.

**A φ-node is compared by identity unless the path has resolved it.** The forward walk in `checkReturned` resolves the φ-nodes of every block it enters to the edge it came in on. A φ-node defined before the log is not resolved, and stands for itself. Expanding it to all of its edges was the first version, and it reported a loop in memos (`store/attachment.go`) whose logged value might have been either edge. The inputs mode still expands every edge: a summary asks what a result *may* carry.

**A value defined again after the log is a new value.** SSA gives one name to a call in a loop body, but each iteration makes a new error. The forward walk records the blocks it entered, and a value defined in one of them does not match. Without this, woodpecker's `cli/pipeline/purge.go` was reported for logging one iteration's error and returning the next one's.

**A captured variable read after a store in the closure reads the store.** `store.Index` treats a `FreeVar` as a whole variable, like an `Alloc`. Before this, a deferred closure that assigns `err = file.Close()` and logs it was said to log the captured `err`. woodpecker's `tink_keyset.go` was reported for that.

A local variable read after a store reads the stores that reach it (`Index.Reaching`). A store through a field or an element is read flow-insensitively (`Index.Parts`).

### What a call carries

`calleeFlows` answers in this order, and the order matters:

1. A builtin: `append`, `min` and `max` carry their arguments.
2. `known.Carries`: every function of `fmt`, `errors`, `strings`, `strconv` and the supported loggers carries every input. Their bodies go through buffers and reflection that a summary cannot follow. `fmt.Errorf` with `%v` would carry nothing.
3. `known.Accessor`: `Error`, `String` and `Unwrap` with no parameters carry their receiver, wherever they are declared.
4. The callee's summary, from its body or its fact.
5. `guessCalleeFlow`, for an interface method, a function value, or a body that is not available: an error result carries the error arguments. This is the one guess that leans toward reporting. It covers a wrapper called through an interface, and it is limited to error-typed arguments and results.

A call is judged by its arguments at the call site, before their conversion to `any`. `fmt.Errorf` has no `error` parameter.

### Reporting

- **A helper that logs and also returns is reported inside itself.** A caller that returns the helper's result is not reported again: the call is passed to the walker as `leaf`, and not traced through. A caller that returns its own copy of `err` is still reported, since that is a second handoff.
- **A report is anchored on the enclosing statement** (`checkStmt`), not on the call's position, which SSA puts at the last `(` of a chain. An ignore directive on the line above a multi-line zerolog chain would otherwise not reach it.
- **Levels.** Debug and trace are not logs. Fatal and panic never return. A level passed as a value counts only as a constant in range (`known.levelIn`). A variable level is not counted.
- **zerolog events are traced to their producer** (`known.eventLogs`). The level is decided where the event is created, not where it is sent. An event whose producer is not visible, such as one passed as a parameter, is not counted. `(*Event)` methods write into their receiver (`known.Mutates`), so a chain broken across statements still carries what an earlier call added.

## Rejected designs

| Design | Why not |
| --- | --- |
| Matching by variable name, as error-log-or-return does | A reassigned `err` is a different error. Only SSA values tell them apart |
| "Takes an error and returns an error" as the whole wrap test | A translating helper such as `toPublic(err) error` returns a sentinel. It is the fallback only for callees with no body |
| Counting `Debug` logs | A retry loop traces each attempt at debug level. Reporting it teaches people to ignore the linter |
| May-logging helpers | A helper that logs only some errors would make every caller a report |
| Reporting unmatched loggers as a config problem | An unknown callee is silence, not an error |

## Testing

```bash
go test ./...    # analysistest over testdata/src
./test_all.sh    # tests, golangci-lint, declscope, and errlogreturn on itself
```

- `testdata/src/*` are analysistest packages. Stubs for zerolog, zap and logrus live under their import paths there. They are **not** read by the analysis, which describes those packages in `internal/known`, so their bodies can be empty.
- **Summaries are pinned as facts.** `Fact.String` renders `carries p0→r0, logs p1, sink`, and a fixture states what it expects with `// want name:"..."`. Every function with a meaningful summary needs one, or analysistest fails with "unexpected fact".
- A report is on the statement's first line, so a `// want` for a multi-line call or a `defer func() {` goes on that line.
- `paths/paths.go` holds the regressions from the corpus runs: `nextIteration*` and `deferClosureOwnError`. Keep them when touching the path walk.

Before a release, run the binary over a few real applications with heavy logging, and read every report. The corpus used so far: usememos/memos, pocketbase/pocketbase, gotify/server, woodpecker-ci/woodpecker. Every report there that remained after the fixes above was a genuine log-and-return.
