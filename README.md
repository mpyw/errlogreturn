# errlogreturn

[![Go Reference](https://pkg.go.dev/badge/github.com/mpyw/errlogreturn.svg)](https://pkg.go.dev/github.com/mpyw/errlogreturn)
[![CI](https://github.com/mpyw/errlogreturn/actions/workflows/ci.yml/badge.svg)](https://github.com/mpyw/errlogreturn/actions/workflows/ci.yml)
[![Codecov](https://codecov.io/gh/mpyw/errlogreturn/graph/badge.svg)](https://codecov.io/gh/mpyw/errlogreturn)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

> [!NOTE]
> This project was written by AI (Claude Code).

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

"Built from it" covers the usual ways an error is passed on.

| Form | Example |
| --- | --- |
| Wrapping | `fmt.Errorf("load: %w", err)`, `errors.Join(err, other)` |
| Formatting | `fmt.Errorf("load: %v", err)`, `err.Error()` |
| A struct that holds it | `&QueryError{Cause: err}` |
| A helper that returns it | Any function whose result carries its error argument |
| A logger field | `zap.Error(err)`, `slog.Any("err", err)`, `logger.With("err", err)` |

`%v` counts as well as `%w`. The error does not have to unwrap. The caller still receives its message and will log it again.

### Helpers are followed

A function that logs its argument is recognized wherever it is called. That includes a function in another package, and a method on your own logger type.

| Case | Treated as |
| --- | --- |
| The helper logs its argument on every path | A log call |
| The helper logs only when the argument is not `nil` | A log call |
| The helper logs only under another condition | Not a log call |
| The helper logs and also returns the error | Reported inside the helper. A caller that returns the helper's result is not reported again |

No configuration is needed for such helpers. Configure only a logger the analyzer cannot read, as described in [Configuration](#configuration).

### Loggers

| Logger | Counted | Not counted |
| --- | --- | --- |
| `log` | `Print`, `Printf`, `Println`, `(*Logger).Output` | `Fatal*`, `Panic*` |
| `fmt` | `Print*`, and `Fprint*` to `os.Stdout` or `os.Stderr` | `Fprint*` to any other writer |
| `log/slog` | `Info`, `Warn`, `Error` and their `Context` forms | `Debug`, and `Log` below `LevelInfo` |
| `github.com/rs/zerolog` | An event from `Info`, `Warn`, `Error`, `Err` or `Log`, sent with `Msg`, `Msgf`, `MsgFunc` or `Send` | `Debug`, `Trace`, `Fatal`, `Panic`, and an event never sent |
| `go.uber.org/zap` | `Info`, `Warn`, `Error`, `DPanic`, and the sugared forms | `Debug`, `Fatal`, `Panic` |
| `github.com/sirupsen/logrus` | `Info`, `Warn`, `Warning`, `Error`, `Print`, and their `f` and `ln` forms | `Debug`, `Trace`, `Fatal`, `Panic` |

A debug line is a trace of what happened, not the handling of a failure, so it is not counted. A fatal or panic call never returns, so no `return` follows it.

A level passed as a value counts only when it is a constant in range. `logger.Log(ctx, lvl, ...)` with a variable `lvl` is not counted.

## What is not reported

| Case | Why |
| --- | --- |
| The log and the `return` are on different branches | No single path runs both |
| A different error is returned | `log(err); return ErrNotFound` hands over nothing that was logged |
| The error is translated | A helper that returns a sentinel instead of its argument does not carry it |
| The error is logged in one loop iteration and returned in the next | The next iteration's error is a new value |
| Generated files | A report there cannot be acted on. Their helpers are still followed |

## Directives

| Directive | Where | Effect |
| --- | --- | --- |
| `//errlogreturn:ignore` | On the reported line, or the line above it | Silences that report |
| `//errlogreturn:sink` | In the doc comment of a function or an interface method | Declares that it logs every argument |

A report is anchored on the first line of the statement that logs. An ignore directive above a multi-line call therefore covers it.

```go
//errlogreturn:ignore the caller only traces this error
log.Error().
	Err(err).
	Msg("request failed")
return err
```

An ignore directive that silences nothing is reported. So is a `sink` directive in the wrong place, and any directive the analyzer does not know.

## Configuration

Use `//errlogreturn:sink` for a logger you can annotate. It works on an interface method, which the analyzer cannot follow on its own.

```go
type Reporter interface {
	//errlogreturn:sink
	Report(msg string, args ...any)
}
```

Use the `-sinks` flag for a logger you cannot annotate. It takes a comma-separated list, spelled the way `go/types` names a function.

```bash
errlogreturn -sinks 'example.com/telemetry.Send,(example.com/telemetry.Client).Capture' ./...
```

A pointer receiver may be written with or without the `*`.

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
