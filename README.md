# errlogreturn

[![Go Reference](https://pkg.go.dev/badge/github.com/mpyw/errlogreturn.svg)](https://pkg.go.dev/github.com/mpyw/errlogreturn)
[![CI](https://github.com/mpyw/errlogreturn/actions/workflows/ci.yml/badge.svg)](https://github.com/mpyw/errlogreturn/actions/workflows/ci.yml)
[![Codecov](https://codecov.io/gh/mpyw/errlogreturn/graph/badge.svg)](https://codecov.io/gh/mpyw/errlogreturn)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Go linter that reports an error that is both logged and returned.

## Overview

An error should be handled once. A function that logs an error and then returns it hands the same failure to its caller. The caller logs it again, and so does the next one. One failure turns into one log line per layer.

`errlogreturn` finds the places where one error reaches a logger and a `return` on the same path.

```go
package user

import (
	"fmt"
	"log/slog"
)

type Store struct{ log *slog.Logger }

func (s *Store) logFailure(err error) {
	s.log.Error("store failed", "err", err)
}

func (s *Store) Load(id string) (*User, error) {
	u, err := s.query(id)
	if err != nil {
		s.log.Error("query failed", "id", id, "err", err)
		return nil, fmt.Errorf("load %s: %w", id, err)
	}
	return u, nil
}

func (s *Store) Save(u *User) error {
	if err := s.insert(u); err != nil {
		s.logFailure(err)
		return err
	}
	return nil
}

type User struct{ ID string }

func (s *Store) query(id string) (*User, error) { return &User{ID: id}, nil }
func (s *Store) insert(u *User) error           { return nil }
```

```text
user.go:17:3: error is logged here and also returned at line 18; log it or return it, not both
user.go:18:3: 	returned here
user.go:25:3: error is logged by (*Store).logFailure and also returned at line 26; log it or return it, not both
user.go:26:3: 	returned here
```

## Requirements

Go 1.27 or later. The analyzed code may target any Go version.

## Installation & Usage

### <a href="https://mise.jdx.dev/"><img src="https://mise.jdx.dev/logo.svg" height="28" alt=""></a> Using [mise](https://mise.jdx.dev/) (macOS/Linux/Windows)

**Recommended.** mise installs errlogreturn from GitHub Releases through its `github` backend. The binaries are prebuilt, so no Go toolchain is needed.

```bash
mise use -g "github:mpyw/errlogreturn"
errlogreturn ./...
```

Or pin it per project in `mise.toml`:

```toml
[tools]
"github:mpyw/errlogreturn" = "latest"
```

> [!IMPORTANT]
> The `go`-based methods below build errlogreturn from source. They need **Go 1.27 or later**. `go tool` also needs Go 1.24+ on `PATH`.

### Using [`go tool`](https://pkg.go.dev/cmd/go#hdr-Run_specified_go_tool)

```bash
go get -tool github.com/mpyw/errlogreturn/cmd/errlogreturn@latest
go tool errlogreturn ./...
```

### Using [`go install`](https://pkg.go.dev/cmd/go#hdr-Compile_and_install_packages_and_dependencies)

```bash
go install github.com/mpyw/errlogreturn/cmd/errlogreturn@latest
errlogreturn ./...
```

### Using [`go vet`](https://pkg.go.dev/cmd/go#hdr-Report_likely_mistakes_in_packages)

```bash
go install github.com/mpyw/errlogreturn/cmd/errlogreturn@latest
go vet -vettool=$(which errlogreturn) ./...
```

> [!CAUTION]
> Pin a version tag instead of `@latest` in CI/CD pipelines, such as `@v0.1.0`. This protects the pipeline from supply chain attacks.

## What is reported

A report needs three things on one path through a function.

| Condition | Meaning |
| --- | --- |
| An error is logged | A logger receives the error, its message, or anything built from it |
| An error is returned | A `return` hands back the same error, or an error built from it |
| One path connects them | The `return` can run after the log call, without the error being produced again |

### What counts as the same error

"Built from it" covers the usual ways an error is passed on.

| Form | Example |
| --- | --- |
| Wrapping | `fmt.Errorf("load: %w", err)`, `errors.Join(err, other)` |
| Formatting | `fmt.Errorf("load: %v", err)`, `err.Error()` |
| A struct that holds it | `&QueryError{Cause: err}` |
| A helper that returns it | Any function whose result carries its error argument |
| A logger field | `zap.Error(err)`, `slog.Any("err", err)`, `logger.With("err", err)` |

Here the logger gets only the message, and the caller gets a new struct.

```go
package built

import (
	"errors"
	"log/slog"
)

type QueryError struct {
	Query string
	Cause error
}

func (e *QueryError) Error() string { return e.Query + ": " + e.Cause.Error() }

func Find(q string) error {
	if err := exec(q); err != nil {
		slog.Error("query failed", "msg", err.Error())
		return &QueryError{Query: q, Cause: err}
	}
	return nil
}

func exec(q string) error { return errors.New("syntax error") }
```

```console
$ errlogreturn ./...
built/built.go:17:3: error is logged here and also returned at line 18; log it or return it, not both
built/built.go:18:3: 	returned here
```

Both carry the same failure, so it is still reported.

> [!NOTE]
> `%v` counts as well as `%w`. The error does not have to unwrap. The caller still receives its message and will log it again.

### Helpers are followed

A function that logs its argument is recognized wherever it is called. That includes a function in another package, and a method on your own logger type.

| Case | Treated as |
| --- | --- |
| The helper logs its argument on every path | A log call |
| The helper logs only when the argument is not `nil` | A log call |
| The helper logs only under another condition | Not a log call |
| The helper logs and also returns the error | Reported inside the helper. A caller that returns the helper's result is not reported again |

A `logx` package holds three helpers.

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

Another package calls each of them.

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

| Function | Result |
| --- | --- |
| `Sync` | Reported. `LogIfErr` logs every error that is not `nil` |
| `Poll` | Not reported. `LogUnlessCanceled` logs only some errors |
| `Fetch` | Not reported. The report is on `LogAndReturn` itself |

> [!TIP]
> No configuration is needed for such helpers. Configure only a logger the analyzer cannot read, as described in [Configuration](#configuration).

### Loggers

| Logger | Counted | Not counted |
| --- | --- | --- |
| `log` | `Print`, `Printf`, `Println`, `(*Logger).Output` | `Fatal*`, `Panic*` |
| `fmt` | `Print*`, and `Fprint*` to `os.Stdout` or `os.Stderr` | `Fprint*` to any other writer |
| `log/slog` | `Info`, `Warn`, `Error` and their `Context` forms | `Debug`, and `Log` below `LevelInfo` |
| `github.com/rs/zerolog` | An event from `Info`, `Warn`, `Error`, `Err` or `Log`, sent with `Msg`, `Msgf`, `MsgFunc` or `Send` | `Debug`, `Trace`, `Fatal`, `Panic`, and an event never sent |
| `go.uber.org/zap` | `Info`, `Warn`, `Error`, `DPanic`, and the sugared forms | `Debug`, `Fatal`, `Panic` |
| `github.com/sirupsen/logrus` | `Info`, `Warn`, `Warning`, `Error`, `Print`, and their `f` and `ln` forms | `Debug`, `Trace`, `Fatal`, `Panic` |

- A debug line is a trace of what happened, not the handling of a failure. It is not counted.
- A fatal or panic call never returns, so no `return` follows it.
- A level passed as a value counts only when it is a constant in range.

```go
package levels

import (
	"context"
	"errors"
	"log/slog"
)

func Retry(ctx context.Context) error {
	err := dial()
	slog.Debug("dial failed, retrying", "err", err)
	return err
}

func Warn(ctx context.Context) error {
	err := dial()
	slog.Log(ctx, slog.LevelWarn, "dial failed", "err", err)
	return err
}

func Dynamic(ctx context.Context, lvl slog.Level) error {
	err := dial()
	slog.Log(ctx, lvl, "dial failed", "err", err)
	return err
}

func dial() error { return errors.New("refused") }
```

```console
$ errlogreturn ./...
levels/levels.go:17:2: error is logged here and also returned at line 18; log it or return it, not both
levels/levels.go:18:2: 	returned here
```

Only `Warn` is reported. `Retry` logs at debug level, and the level in `Dynamic` is not known.

## What is not reported

| Case | Why | Example below |
| --- | --- | --- |
| The log and the `return` are on different branches | No single path runs both | `Branches` |
| A different error is returned | `log(err); return ErrNotFound` hands over nothing that was logged | `Sentinel` |
| The error is translated | A helper that returns a sentinel instead of its argument does not carry it | |
| The error is logged in one loop iteration and returned in the next | The next iteration's error is a new value | `Loop` |
| Generated files | A report there cannot be acted on. Their helpers are still followed | |

```go
package silent

import (
	"errors"
	"log/slog"
)

var ErrNotFound = errors.New("not found")

func Branches(id string) error {
	err := load(id)
	if errors.Is(err, ErrNotFound) {
		slog.Warn("missing", "id", id, "err", err)
		return nil
	}
	return err
}

func Sentinel(id string) error {
	if err := load(id); err != nil {
		slog.Error("load failed", "err", err)
		return ErrNotFound
	}
	return nil
}

func Loop(ids []string) error {
	var last error
	for _, id := range ids {
		if last != nil {
			slog.Warn("retrying", "err", last)
		}
		last = load(id)
	}
	return last
}

func load(id string) error { return ErrNotFound }
```

`errlogreturn ./...` prints nothing for this package.

## Directives

| Directive | Where | Effect |
| --- | --- | --- |
| `//errlogreturn:ignore` | On the reported line, or the line above it | Silences that report |
| `//errlogreturn:sink` | In the doc comment of a function or an interface method | Declares that it logs every argument |

A report is anchored on the first line of the statement that logs. An ignore directive above a multi-line call therefore covers it.

A directive that does nothing is reported. That covers an ignore that silences nothing, a `sink` directive in the wrong place, and any directive the analyzer does not know.

```go
package directives

import (
	"errors"
	"log/slog"
)

func Open() error {
	err := dial()
	//errlogreturn:ignore the caller only traces this error
	slog.Error("open failed",
		"err", err)
	return err
}

func Close() error {
	//errlogreturn:ignore
	return dial()
}

//errlogreturn:typo
func dial() error { return errors.New("refused") }
```

```console
$ errlogreturn ./...
directives/directives.go:21:1: unknown directive errlogreturn:typo
directives/directives.go:17:2: unused errlogreturn:ignore directive
```

`Open` is silenced. The ignore in `Close` has no report to silence.

> [!TIP]
> Text after `//errlogreturn:ignore` is free. Use it to say why the log and the `return` are both needed.

## Configuration

Use `//errlogreturn:sink` for a logger you can annotate. It works on an interface method, which the analyzer cannot follow on its own.

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

Use the `-sinks` flag for a logger you cannot annotate, such as one in a third-party package. Here `telemetry.Send` is declared elsewhere.

```go
package worker

import (
	"errors"

	"example.com/app/telemetry"
)

func Upload() error {
	err := send()
	telemetry.Send("upload", err)
	return err
}

func send() error { return errors.New("reset") }
```

```console
$ errlogreturn ./...
$ errlogreturn -sinks 'example.com/app/telemetry.Send' ./...
worker/worker.go:11:2: error is logged here and also returned at line 12; log it or return it, not both
worker/worker.go:12:2: 	returned here
```

The flag takes a comma-separated list, spelled the way `go/types` names a function.

| Kind | Spelling |
| --- | --- |
| A function | `example.com/telemetry.Send` |
| A method | `(example.com/telemetry.Client).Capture` |
| A method with a pointer receiver | `(*example.com/telemetry.Client).Capture`. The `*` may be left out |

## Limitations

The analysis prefers silence to a false report. These cases are not reported.

| Case | Reason |
| --- | --- |
| A call through an interface or a function value | The callee is not known. Declare it with `//errlogreturn:sink` if it logs |
| An error passed through a channel, or stored in a global and read elsewhere | The path is not followed |
| A value that may come from either of two branches | It is compared by identity, not by guessing which branch ran |
| A recursive helper | Its summary is built before its own recursive call is understood |

## License

MIT
