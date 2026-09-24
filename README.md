# errlogreturn

[![Go Reference](https://pkg.go.dev/badge/github.com/mpyw/errlogreturn.svg)](https://pkg.go.dev/github.com/mpyw/errlogreturn)
[![CI](https://github.com/mpyw/errlogreturn/actions/workflows/ci.yml/badge.svg)](https://github.com/mpyw/errlogreturn/actions/workflows/ci.yml)
[![Codecov](https://codecov.io/gh/mpyw/errlogreturn/graph/badge.svg)](https://codecov.io/gh/mpyw/errlogreturn)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Go linter that reports an error that is both logged and returned.

## Overview

An error should be handled once. A function that logs an error and then returns it hands the same failure to its caller. The caller logs it again, and so does the next one. One failure turns into one log line per layer.

`errlogreturn` reports a function where one error reaches a logger and a `return` on the same path.

```go
package user

import (
	"fmt"
	"log/slog"
)

func Load(id string) (*User, error) {
	u, err := query(id)
	if err != nil {
		slog.Error("query failed", "id", id, "err", err)
		return nil, fmt.Errorf("load %s: %w", id, err)
	}
	return u, nil
}

type User struct{ ID string }

func query(id string) (*User, error) { return &User{ID: id}, nil }
```

```console
$ errlogreturn ./...
user/user.go:11:3: error is logged here and also returned at line 12; log it or return it, not both
user/user.go:12:3: 	returned here
```

> [!NOTE]
> The paths in these examples are shortened. The tool prints absolute paths.

## Install

| Method | Command | Needs |
| --- | --- | --- |
| **[mise](https://mise.jdx.dev/)** *(recommended)* | `mise use "github:mpyw/errlogreturn@0.1.0"` | Nothing. Installs the prebuilt binary |
| `go tool` | `go get -tool github.com/mpyw/errlogreturn/cmd/errlogreturn@latest` | Go 1.27+ |
| `go install` | `go install github.com/mpyw/errlogreturn/cmd/errlogreturn@latest` | Go 1.27+ |

```bash
errlogreturn ./...          # or: go tool errlogreturn ./...
```

The analyzed code may target any Go version.

> [!TIP]
> On a large module, run it through `go vet`. Helpers are recognized across packages, so every dependency is analyzed too. Run on its own, the tool holds all of that in one process, which peaked at 19GB on traefik. Through `go vet` each package is its own process and is cached: the same run peaked at 2.7GB, and at 0.2GB on the next run.
>
> ```bash
> go vet -vettool=$(which errlogreturn) ./...
> ```

<details>
<summary>Pin a version, or run through <code>go vet</code></summary>

`mise use` pins the version in the project's `mise.toml`, so every checkout and CI run the same one. Add `-g` to install it for every project on your machine instead.

```toml
[tools]
"github:mpyw/errlogreturn" = "0.1.0"
```

Through `go vet`:

```bash
go vet -vettool=$(which errlogreturn) ./...
```

> [!CAUTION]
> Pin a version tag instead of `@latest` in CI/CD pipelines, such as `@v0.1.0`. This protects the pipeline from supply chain attacks.

</details>

## What counts as the same error

The logged value and the returned value do not have to be the same variable. Anything built from the error counts.

| Form | Example |
| --- | --- |
| Wrapping | `fmt.Errorf("load: %w", err)`, `errors.Join(err, other)` |
| Formatting | `fmt.Errorf("load: %v", err)`, `err.Error()`. The error does not unwrap, but the caller still gets its message |
| A struct that holds it | `&QueryError{Cause: err}` |
| A helper that returns it | Any function whose result carries its error argument |
| A logger field | `zap.Error(err)`, `slog.Any("err", err)`, `logger.With("err", err)` |

## Helpers are followed

A function that logs its argument counts as a log call. This holds for a function in another package, and for a method on your own logger type. No configuration is needed.

| The helper | Example body | A caller that calls it and returns `err` |
| --- | --- | --- |
| Logs its argument | `slog.Error("failed", "err", err)` | Reported |
| Logs it unless it is `nil` | `if err != nil { slog.Error(...) }` | Reported |
| Logs it only under another condition | `if !errors.Is(err, ErrCanceled) { slog.Error(...) }` | Not reported |
| Logs it and returns it | `slog.Error(...); return err` | The helper is reported. A caller that returns its result is not |

<details>
<summary>Full example</summary>

```go
package logx

import (
	"errors"
	"log/slog"
)

var ErrCanceled = errors.New("canceled")

// LogIfErr logs err unless it is nil.
func LogIfErr(err error) {
	if err != nil {
		slog.Error("failed", "err", err)
	}
}

// LogUnlessCanceled logs err unless it is ErrCanceled.
func LogUnlessCanceled(err error) {
	if !errors.Is(err, ErrCanceled) {
		slog.Error("failed", "err", err)
	}
}

// LogAndReturn logs err and hands it back.
func LogAndReturn(err error) error {
	slog.Error("failed", "err", err)
	return err
}
```

```go
package helpers

import (
	"errors"

	"example.com/app/logx"
)

func Sync() error {
	err := pull()
	logx.LogIfErr(err)
	return err
}

func Poll() error {
	err := pull()
	logx.LogUnlessCanceled(err)
	return err
}

func Fetch() error {
	if err := pull(); err != nil {
		return logx.LogAndReturn(err)
	}
	return nil
}

func pull() error { return errors.New("timeout") }
```

```console
$ errlogreturn ./...
logx/logx.go:26:2: error is logged here and also returned at line 27; log it or return it, not both
logx/logx.go:27:2: 	returned here
helpers/helpers.go:11:2: error is logged by LogIfErr and also returned at line 12; log it or return it, not both
helpers/helpers.go:12:2: 	returned here
```

</details>

## Loggers

| Logger | Counted | Not counted |
| --- | --- | --- |
| `log` | `Print`, `Printf`, `Println`, `(*Logger).Output` | `Fatal*`, `Panic*` |
| `fmt` | `Print*`, and `Fprint*` to `os.Stdout` or `os.Stderr` | `Fprint*` to any other writer |
| `log/slog` | `Info`, `Warn`, `Error` and their `Context` forms | `Debug`, and `Log` below `LevelInfo` |
| [`github.com/rs/zerolog`](https://github.com/rs/zerolog) | An event from `Info`, `Warn`, `Error`, `Err` or `Log`, sent with `Msg`, `Msgf`, `MsgFunc` or `Send` | `Debug`, `Trace`, `Fatal`, `Panic`, and an event never sent |
| [`go.uber.org/zap`](https://github.com/uber-go/zap) | `Info`, `Warn`, `Error`, `DPanic`, and the sugared forms | `Debug`, `Fatal`, `Panic` |
| [`github.com/sirupsen/logrus`](https://github.com/sirupsen/logrus) | `Info`, `Warn`, `Warning`, `Error`, `Print`, and their `f` and `ln` forms | `Debug`, `Trace`, `Fatal`, `Panic` |

> [!NOTE]
> A debug line traces what happened. It does not handle the failure. A fatal or panic call never returns, so no `return` follows it.
>
> A level passed as a value counts only when it is a constant. `slog.Log(ctx, slog.LevelWarn, ...)` counts, and `slog.Log(ctx, lvl, ...)` does not.
>
> A log call in a `defer` or a `go` statement counts too.

## What is not reported

Every judgement leans toward silence. A missed report costs a duplicated log line, and a false one teaches people to ignore the linter.

| Case | Why |
| --- | --- |
| The log and the `return` are on different branches | No single path runs both |
| A branch the path already decided, such as `if verbose { log(err) }` then `if !verbose { return err }` | The path that logs never reaches that `return`. The same holds for a value compared with constants, as two `switch mode` statements do. Code under a false constant, such as `if debug`, never runs at all |
| A different error is returned | `log(err); return ErrNotFound` hands over nothing that was logged |
| The error is translated | A helper that returns a sentinel instead of its argument does not carry it, in any package |
| Only a check on the error is logged, such as `err != nil` or `errors.Is(err, target)` | A boolean or a number says whether the call failed, not what the error was. An error type that is a number, such as `syscall.Errno`, still counts |
| Another field is logged, such as `j.ID` while the error goes to `j.Err` | Each field is read on its own, through calls and getters too |
| The error is replaced after the log | A later assignment, a closure that assigns it, or a call that writes it, such as `fill(&err)`, leaves a new error. So does a deferred closure that always sets `err`, or sets `err = nil` right after logging it. A call that writes another field does not count, and neither does a deferred `Close` that sets `err` only when it is `nil` |
| The error is logged in one loop iteration and returned in the next | The next iteration's error is a new value |
| The log cannot run on the loop's last iteration, as in a retry loop that breaks before logging its last attempt | Then the loop cannot end right after the log, so its exit is not followed. A test of the loop counter that the analyzer cannot evaluate, such as `i%2 == 0`, counts as one |
| The logged error is known to be `nil` there | A `nil` error is not a failure |
| Generated files | A report there cannot be acted on. Their helpers are still followed |
| A call through an interface or a function value | The callee is not known. It is taken to log nothing, and to pass its error arguments on to its error result. [Declare it](#declaring-your-own-logger) if it logs |
| An error sent through a channel, or stored in a global and read in another function | The path is not followed |
| A variable that may hold one of several errors when it is logged | It matches only a `return` of that same variable |
| A helper that calls itself | Its own recursive call is taken to log nothing |

<details>
<summary>The loop case</summary>

```go
package retry

import "log/slog"

func Each(ids []string, load func(string) error) error {
	var last error
	for _, id := range ids {
		if last != nil {
			slog.Warn("retrying", "err", last)
		}
		last = load(id)
	}
	return last
}
```

The warning logs the previous attempt's error. The `return` hands back the last attempt's, which was never logged. `errlogreturn ./...` prints nothing here.

</details>

## Ignoring a report

Put `//errlogreturn:ignore` on the reported line, or on the line above it. Text after the directive is free, so say why both are needed.

Write it with no space after the slashes, as with `//go:` directives. `// errlogreturn:ignore` is a plain comment.

A report is anchored on the first line of the statement that logs. A directive above a multi-line call therefore covers it.

```go
err := dial()
//errlogreturn:ignore the caller only traces this error
slog.Error("open failed",
	"err", err)
return err
```

> [!NOTE]
> A directive that does nothing is reported.
>
> | Written | Report |
> | --- | --- |
> | An ignore that silences nothing | `unused errlogreturn:ignore directive` |
> | `//errlogreturn:sink` outside a doc comment | `errlogreturn:sink belongs in the doc comment of a function or an interface method` |
> | An unknown directive, such as `//errlogreturn:typo` | `unknown directive errlogreturn:typo` |

## Declaring your own logger

A logger the analyzer cannot read has to be declared. That is usually an interface method, or a function whose body only sends the error elsewhere.

Put `//errlogreturn:sink` in the doc comment of a logger you can edit. It declares that the function logs every argument.

```go
package billing

import "errors"

type Reporter interface {
	//errlogreturn:sink
	Report(msg string, args ...any)
}

func Upload(r Reporter) error {
	err := send()
	r.Report("upload failed", "err", err)
	return err
}

func send() error { return errors.New("reset") }
```

```console
$ errlogreturn ./...
billing/billing.go:12:2: error is logged here and also returned at line 13; log it or return it, not both
billing/billing.go:13:2: 	returned here
```

Pass `-sinks` for a logger you cannot edit, such as one in a third-party package. It takes a comma-separated list.

```bash
errlogreturn -sinks 'example.com/telemetry.Send,(example.com/telemetry.Client).Capture' ./...
```

| Kind | Spelling |
| --- | --- |
| A function | `example.com/telemetry.Send` |
| A method | `(example.com/telemetry.Client).Capture` |
| A method with a pointer receiver | `(*example.com/telemetry.Client).Capture` |
| A method of a generic type | `(example.com/telemetry.Queue[T]).Push` |
| A generic method of a generic type | `(example.com/telemetry.Queue[T]).Map` |

The `*` may be written or left out, whatever the receiver is. A generic method is written without its own type parameters. A name that is not spelled this way stops the run with an error. A name that is spelled correctly but names nothing is not reported, so check the package path.

## License

MIT
