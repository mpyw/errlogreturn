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
internal/store/        the write index of a function, by root and path, and reaching writes
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

`walker` and `checkWalk` are types the other files never name. They are private by default, and so are their fields, so the fields carry no directive: `//declscope:private` on a field of a private type restates the default. Writing one on each field of `walker` was a mistake. declscope does not report it, since the default could be configured the other way.

## The analysis

It is a taint analysis. The source is an error value. It has two exits, a logger and a `return`, and a report is made when one value reaches both on one path.

### Summaries

Each function is reduced to a `summary` over its inputs: the parameters, receiver first, then free variables.

| Field | Meaning | Kind |
| --- | --- | --- |
| `flows[i]` | The results input `i` may carry into | May |
| `logs[i]` | Input `i` is logged on every path that returns | Must |
| `writes[i]` | The paths below input `i` that the function may write | May |
| `fields` | A result made from memory at a path below an input, where the input itself does not flow | May |

Named functions export their summary as a `Fact`, so a helper in another package is recognized with no configuration. A function literal is summarized in the pass that holds it.

A summary that says nothing is still exported when a caller without it would guess otherwise (`runGuessWrong`). With no fact, a caller falls back to `guessCalleeFlow`, which says an error result carries the error arguments. A translator such as `ToPublic(err) error { return ErrInternal }` in another package was reported for that. `Fact.String` renders such a fact as `carries nothing`.

- **`logs` is a must-analysis.** A helper that logs only under a condition is not a logger. The one exception is a condition that the input is `nil` (`summaryNilEdge`), since a nil error has nothing to log. This is what lets `func logIfErr(err error) { if err != nil { log(err) } }` count.
- **`logs` only covers inputs that can carry an error** (`typeutil.MayCarry`). A log call logs its receiver too, and without this rule every function that takes a `*zerolog.Logger` would be said to log it. An interface counts only when `error` implements it, so `Reporter` does not.
- **`MayCarry` ends on recursive types.** It follows slice and array elements in a loop. It stops at a type it has seen, and at a depth cap for generic instances. `type T []T` overflowed the stack before. That happened in any package the run loaded, including `encoding/json`'s tests.
- **Recursion reads an empty summary.** The entry is stored before it is filled. A recursive call can only make a function log or carry less. A fixpoint iteration was not added, since it would only find more reports in code that is rare.

### Origins and the path walk

`walker` traces a value backward. In the errors mode it collects every error-typed value on the way. A logged value and a returned value are reported when those sets meet.

**A φ-node is compared by identity unless the path has resolved it.** The forward walk in `checkReturned` resolves the φ-nodes of every block it enters to the edge it came in on. A φ-node defined before the log is not resolved, and stands for itself. Expanding it to all of its edges was the first version, and it reported a loop in memos (`store/attachment.go`) whose logged value might have been either edge. The inputs mode still expands every edge: a summary asks what a result *may* carry.

**A value defined again after the log is a new value.** SSA gives one name to a call in a loop body, but each iteration makes a new error. The forward walk records the blocks it entered, and a value defined in one of them does not match. Without this, woodpecker's `cli/pipeline/purge.go` was reported for logging one iteration's error and returning the next one's.

**A captured variable read after a store in the closure reads the store.** `store.Index` treats a `FreeVar` as a whole variable, like an `Alloc`. Before this, a deferred closure that assigns `err = file.Close()` and logs it was said to log the captured `err`. woodpecker's `tink_keyset.go` was reported for that.

**Memory is read by root and path, in order.** `store.Index` files each write under the value its address starts from, with the fields and elements below it (`store.Locate`). A read of `j.id` does not see a write to `j.err`. A later write to the same field replaces an earlier one, as a store to a variable does (`Index.Reaching`). An element step (`store.Elem`) matches every element and replaces none. A constant index (`store.constIndex`) is told apart from the others. A map element read by a lookup or a range is the map's memory at an element. A path is cut at `walkerMaxPath` steps: `p = p.next` otherwise grew one without end, and the first version of this rule hung on the analyzer's own source. Filing every write under the base value was the first version. Logging `j.ID` then picked up every error stored into `j`, which ordinary stateful methods do. A read through a store of the whole struct is narrowed to the field (`walker.project`). A read of the whole value, such as logging `j`, still sees every write below it (`Index.Parts`).

**A read stops where its local is allocated** (`Reach.Zero`). The memory is zero there, and a local allocated again in a loop is a new one. Scanning past it was quadratic in a chain of `fmt.Errorf` calls, whose variadic arrays are locals, and it matched one iteration's `t := &T{}` with the next's.

**A call that writes memory ends the read** (`Index.clobbers`). What the memory holds after it is unknown. Two reads after the same call read the same thing, so the call is recorded as the origin (`walkerOrigin.clob`). `try := func() { err = do() }; try(); log(err); return err` is still reported, and a second `try()` after the log is not. What a call writes comes from the callee's `writes`, by path, so `c.bump()` writing `c.n` leaves `c.err` alone, and `fill(&c.err)` does not. Treating any call handed the root as a write was the first version. It lost every report with a pointer-receiver method call between the log and the return. A declared function with no fact writes nothing, since a fact is exported whenever a function writes. An interface method or a function value writes everything below what it is handed. A closure handed to any call writes what its own summary says. A `RunDefers` runs the deferred calls, and a deferred call counts only for what it writes on every path (`summary.must`). A deferred `Close` that sets `err` only when `err` is `nil` leaves a logged error alone. A deferred closure that clears a variable around every log of it hands nothing back (`checkUncleared`). The clear may be on every path after the log. It may also come before the log, in a block that runs whenever the log does, as `e := err; err = nil; log(e)`. A value computed from what the variable held, such as `fmt.Errorf("%w", err)`, is no clear, including through the array a variadic call is passed (`checkReadsVar`). A store through a loaded pointer may write a local variable whose address was stored in memory (`Index.escaped`). `clear`, `delete` and `copy` write their first argument. That covers a retry through `try := func() { err = do() }` and a deferred closure that logs `err` and then sets `err = nil`. A package-level variable the function assigns can be assigned by any call. One the function never assigns is read as it stands: it is usually a sentinel, and counting calls as clobbers lost woodpecker's `server/rpc/sanitize.go` reports of `ErrAgentIllegalRepo`.

**At a return, memory is read along the walked path** (`store.Along`). The path is the chain of blocks the walk entered, each from one block, not the set of them. With a set, a branch that skipped a store on the logging path was still read. `if err != nil { log(err); err = do() }; return err` stores a new error on the path from the log. The branch that skips the store never logged, so it is not read.

**Memory the function did not write, and a part of a value that is not memory, is recorded by where it is** (`walkerOrigin`). A struct received from a channel, or an array parameter copied into a local, is read by the path below it. Some values are not memory, such as one received or asserted out of an interface. Reading a part of one never traces it whole, since it would match, as an error, with any other part. A path through an element whose index is not a constant is never recorded (`walker.origin`), since `errs[i]` and `errs[j]` may be two errors. A map element is recorded by the lookup or range step that produced it, so each iteration's lookup is its own. Recording by the map was the version before, and it reported "log the non-fatal ones, return the fatal one" over a map. Two loads of `j.err` with no write between them read the same error, though SSA gives them two values. A field of what a call returned is not traced through the call (`walkerFromCall`): the call's summary says what its whole result carries, not which field. A getter carries only the field it reads (`fields`), so `r.ID()` does not carry `r.err`.

**A boolean or a number carries nothing** (`typeutil.Inert`), unless it is itself an error, as `syscall.Errno` is. The walk stops at one. Logging `err == nil`, `errors.Is(err, target)` or `len(err.Error())` says whether a call failed, not what the error was. A defer that logs `zap.Bool("ok", err == nil)` is a common middleware shape and was reported before.

**The walk takes a block once per state that can change what it sees** (`checkWalk.checkSeenKey`), and at most `checkMaxSteps` blocks from one log. A φ-node that reaches memory, by a store, a map update or a send, is live everywhere, since a load after it reads it back. The live φ-nodes come from `checkPhiDeps`, which shares one set among the values of a strongly connected component. A pass per step of a chain of loop-carried values was cubic. The state is what the walk from the block depends on, not the block it came from. It holds the decided conditions and facts, and the live φ-nodes with the edges the path resolved them to, the block's own included. It also holds the entered blocks that define what was logged. Then the entered blocks that write memory (`checkWriteHash`), since a read at a return looks back along the path. A call that writes through what it is handed counts, as `s.clear()` does. A store into a variadic call's array is no write that matters. Keying on the entering block was the version before: every diamond after a log doubled the states even with no φ-node at its merge. Leaving the φ-nodes out was the version before. The first path through a merge then hid the other: `if errors.Is(err, skip) { err = nil }` followed by any diamond lost the report. Only conditions a branch reachable from the block reads are kept (`checkReads`, `checkRelevant`). More than `checkMaxKnown` of them gives the path up: forgetting one lets the walk take the very edge it rules out, which reported falsely. The bound counts operands and facts, not cases.

**A walk that does not settle is run again with a key that leaves decisions out** (`checkWalk.frozen`). Independent conditions after a log, each read again later, make more paths than `checkMaxSteps`. The second walk takes each block once for what was logged, like the first version of the walk. Keeping the writes in its key let a config loader that writes on every branch multiply its states: 200 fields took 20s. It still decides the conditions on its path, so every return it reaches is on one consistent path. A log under `if verbose` is not matched with a return under `if !verbose`. Its key cannot multiply, so it carries every decision past `checkMaxKnown`, in one map it changes and changes back (`enterFrozen`). A pass over the map is paid only on entering a block on a cycle, where a condition can go stale (`checkCycles`). Natural loops were the version before, and a `goto` into the middle of a loop made a cycle none of them held: a stale condition decided the edge, and reported falsely. Two versions were rejected. Recording nothing after the log let the walk take edges a decision rules out. It reported a φ-correlated return that the first version missed only by the order it walked in. Giving up lost every log in a branchy function. Once a function has had one such walk, its other logs try `checkMaxSteps / 16` steps first. A per-function budget was tried and dropped: past it, the logs left lost their genuine reports.

**The walk follows the conditions it can decide.** A branch the log sits under decides its condition (`checkConditions`). A branch the walk takes decides it for the rest of the path. A constant, or a φ-node resolved to one, decides it too (`checkEval`). A comparison of an operand with an integer or nil narrows one fact per operand (`checkKnown.facts`). Two `switch` statements over one mode then carry one fact, not a condition per case. A load is not such an operand, since memory may change between two reads. Nor is the length of a map or a channel, which an update or a send changes. Other comparisons are decided from other comparisons of the same value (`checkEvalCmp`, `checkSet`). An integer comparison is an interval, so `code < 500` rules out `code >= 500`. A nil test is a two-value set. It matches a φ-node the path resolved to the tested value, so a loop head's `err != nil` follows from the body's. Two reads of one bound, such as `len(s)` or `x > 0` twice, are one operand (`checkSameOperand`). A φ-node resolved to a constant is that constant, and following a chain of resolutions spends no depth. A conversion of a concrete value to an interface is not stripped: a nil `*T` in an `error` is not a nil error. Entering a block drops every decided condition computed from a value the block defines (`checkDependsOn`). What held for the counter in one iteration does not hold after the head defines it again. Only the agreeing edge is taken, so `if verbose { log(err) }; if !verbose { return err }` is silent. A condition computed in a block is dropped when the walk enters that block again. Code under a false constant, such as `if debug`, is never a log site (`checkLive`). A logged error that a branch the log sits under compared with `nil` is dropped from the logged set.

**The walk does not take a loop's exit that cannot follow the log** (`checkExits`). In a retry loop that breaks before logging its last attempt, the exit after a log cannot run, but the walk does not follow the counter. It would take the exit and report the last attempt's error, which was never logged. `checkCounter` finds the loop's tests of a counter. A test is a branch inside the loop with one edge out of it whose condition reads the counter. It may sit at the head, at the bottom of the body or in a `break`. A φ-node no such test reads, such as a count of failures, is not a counter. The counter must step by the same 1 on every back edge and be compared with one bound. Then `checkLastOf` gives the counter's value in the iteration whose log each exit follows. A test before the log follows the last value that passes it, and a test after the log the first value that fails. The least of these is the last value the log can run on. An exit is kept only when its value is that one and `checkLogsAt` finds every other branch that decides the log leading to it there. A test by `==` or `!=` is read as counting toward its bound. A guard may compare the counter with a constant against a last value over a bound, as `i > 0` does against `n-1`. It then leads to the log for some bound, and the exit is kept (`checkAt`, open). A guard against another bound, such as `i < *last`, or on another counter of the loop (`checkOtherCounter`), cannot be read, and drops the exits. Two reads of one bound, such as `len(s)` or `c.max` twice, are the same bound (`checkSame`). A loop with one test and no guard on the counter keeps its exit whatever its step. The walk still goes round the loop, so a logged error returned early in the next iteration is reported. A test or a branch the rule cannot read, such as `i%2 == 0` or a delay that doubles, drops the exits, which leans toward silence. `spec/loop_exit.fsl` proves the lemma the rule relies on: when the guard fails on the last value, the loop never exits right after a log.

### What a call carries

`calleeFlows` answers in this order, and the order matters:

1. A builtin: `append`, `min` and `max` carry their arguments.
2. `known.Carries`: every function of `fmt`, `errors`, `strings`, `strconv` and the supported loggers carries every input. Their bodies go through buffers and reflection that a summary cannot follow. `fmt.Errorf` with `%v` would carry nothing. The error results of `fmt` other than `Errorf` carry nothing: the error from `Fprintf` is the writer's.
3. `known.Accessor`: `Error`, `String` and `Unwrap` with no parameters carry their receiver, wherever they are declared.
4. The callee's summary, from its body or its fact.
5. `guessCalleeFlow`, for an interface method, a function value, or a body that is not available: an error result carries the error arguments. This is the one guess that leans toward reporting. It covers a wrapper called through an interface, and it is limited to error-typed arguments and results.

A call is judged by its arguments at the call site, before their conversion to `any`. `fmt.Errorf` has no `error` parameter.

### Reporting

- **A helper that logs and also returns is reported inside itself.** A caller that returns the helper's result is not reported again: the call is passed to the walker as `leaf`, and not traced through. A caller that returns its own copy of `err` is still reported, since that is a second handoff.
- **Directives are parsed by `ast.ParseDirective`**, following https://go.dev/doc/comment#directives. `// errlogreturn:ignore`, with a space, is prose, as `// go:generate` is. Accepting the space was the first version, and it reported a prose comment such as `// errlogreturn: see ...` as an unknown directive.
- **A method value is named by its method.** `f := l.Err; f(err)` calls a synthetic wrapper with no declared object. `callee.calleeDeclared` finds the method behind it for the message.
- **A report is anchored on the enclosing statement** (`checkStmt`), not on the call's position, which SSA puts at the last `(` of a chain. An ignore directive on the line above a multi-line zerolog chain would otherwise not reach it.
- **Levels.** Debug and trace are not logs. Fatal and panic never return. A level passed as a value counts only as a constant in range (`known.levelIn`). A variable level is not counted.
- **zerolog events are traced to their producer** (`known.eventLogs`). The level is decided where the event is created, not where it is sent. An event whose producer is not visible, such as one passed as a parameter, is not counted, and neither is one that went through `Discard`. `(*Event)` methods write into their receiver (`known.Mutates`), so a chain broken across statements still carries what an earlier call added.

## Rejected designs

| Design | Why not |
| --- | --- |
| Matching by variable name, as error-log-or-return does | A reassigned `err` is a different error. Only SSA values tell them apart |
| "Takes an error and returns an error" as the whole wrap test | A translating helper such as `toPublic(err) error` returns a sentinel. It is the fallback only for callees with no body |
| Counting `Debug` logs | A retry loop traces each attempt at debug level. Reporting it teaches people to ignore the linter |
| May-logging helpers | A helper that logs only some errors would make every caller a report |
| Reporting unmatched loggers as a config problem | An unknown callee is silence, not an error |
| Never going back round a loop after a log | It silenced the retry loop that breaks before its last log. It also silenced a loop that logs each error and returns them all joined, and a last attempt that is logged and falls out of the loop. Both are genuine |
| Stopping at any test of the loop counter | `if i > 0 { log(err) }` logs the last attempt too, and was silenced. The guard is evaluated at the counter's last value instead |
| Not going back round the loop at all | A logged error returned early in the next iteration was lost. Only the loop's exits are not taken |
| Keying the walk's visited blocks on every decided condition | Each independent `if` after a log doubled the keys. A function of 100 of them took 22s. Only conditions a later branch reads are kept, at most `checkMaxKnown` of them |
| Copying the path at every step of the walk | A function of 1600 logs took 24s. The walk extends the path and takes it back (`checkWalk.enter`) |
| Counting every call as a write to every package-level variable | A sentinel error logged and returned across a call was lost. Only a variable the function assigns is read in order |
| Accepting `-sinks` names without checking them | A typo switched its sink off in silence. A name not spelled like a function is refused when the flag is parsed. Type arguments are part of the spelling, as in `(pkg.List[T]).Add` |
| A write summary of one bit per input | Any pointer-receiver method became a write to every field of the receiver |

## Testing

```bash
go test ./...    # analysistest over testdata/src
./test_all.sh    # tests, golangci-lint, declscope, and errlogreturn on itself
```

- `testdata/src/*` are analysistest packages. Stubs for zerolog, zap and logrus live under their import paths there. They are **not** read by the analysis, which describes those packages in `internal/known`, so their bodies can be empty.
- **Summaries are pinned as facts.** `Fact.String` renders `carries p0→r0, carries p0.1→r0, logs p1, writes p0.1, sink`, and a fixture states what it expects with `// want name:"..."`. A path renders a field by its index, an element as `[]` and a loaded pointer as `*`. Every function with a meaningful summary needs one, or analysistest fails with "unexpected fact". The pattern is a regular expression, so anchor it, as in ``// want f:`^writes p0\.1$` ``, when a longer fact would also match.
- A report is on the statement's first line, so a `// want` for a multi-line call or a `defer func() {` goes on that line.
- `paths/paths.go` holds the regressions from the corpus runs: `nextIteration*` and `deferClosureOwnError`. Keep them when touching the path walk.
- `regress/regress.go` holds what a review found reported wrongly, each beside a case the fix must keep reporting. `recursive/recursive.go` holds the types that overflowed `MayCarry`.
- `coverage/coverage.go` reaches the less common branches: every logger form, the walk's bounds, conversions. The engine's pure functions have unit tests over SSA built from source (`internal/check_test.go`).
- **Coverage is held at 99.5% of statements** (`coverage.sh`, run by `test_all.sh`). The statements left are guards no input reaches. They are a sink call with no position (`checkReportable`), and a statement outside the pass or around no call (`checkStmt`). Then a branch with both edges to one block (`checkConditions`), a method of an unnamed interface named by `sinkName`, and `Fact.AFact`. Code no input reaches otherwise is deleted, not excluded.

### Formal specs

`spec/*.fsl` model the four path rules that came out of the corpus runs and the review, each beside the rule it replaced. `spec/README.md` lists what each proves, and `spec/verify.sh` is the gate. **When one of those rules changes, change its spec first.** A proved spec that no longer matches the code is worse than none.

Run standalone, the binary holds every analyzed package, dependencies included, in one process. That is what go/analysis does for an analyzer with facts, and it peaked at 19GB on traefik, against 17GB on the first version. Through `go vet -vettool` each package is its own process, and the same run peaked at 2.7GB. The README recommends it for large modules. Filtering facts to exported functions saved almost nothing, since the facts are not what is large.

Before a release, run the binary over a few real applications with heavy logging, and read every report. The corpus used so far: usememos/memos, pocketbase/pocketbase, gotify/server, woodpecker-ci/woodpecker, caddyserver/caddy, traefik/traefik, grafana/tempo, jaegertracing/jaeger, harness/harness, and `std` with its tests. Every report there that remained after the fixes above was a genuine log-and-return, or a deliberate tracing wrapper in `std`.
