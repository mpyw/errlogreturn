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
| **[mise](https://mise.jdx.dev/)** *(recommended)* | `mise use "github:mpyw/errlogreturn@0.3.1"` | Nothing. Installs the prebuilt binary |
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
<summary>Pin a version</summary>

`mise use` pins the version in the project's `mise.toml`, so every checkout and CI run the same one. Add `-g` to install it for every project on your machine instead.

```toml
[tools]
"github:mpyw/errlogreturn" = "0.3.1"
```

> [!CAUTION]
> Pin a version tag instead of `@latest` in CI/CD pipelines, such as `@v0.3.1`. This protects the pipeline from supply chain attacks.

</details>

<details>
<summary>Run inside golangci-lint</summary>

errlogreturn is a [module plugin](https://golangci-lint.run/plugins/module-plugins/). It is not bundled with golangci-lint, so build a binary that holds it. Write `.custom-gcl.yml`:

```yaml
version: v2.13.1  # the golangci-lint release to build
plugins:
  - module: github.com/mpyw/errlogreturn
    import: github.com/mpyw/errlogreturn/plugin
    version: v0.3.1
```

Turn it on in `.golangci.yml`. The keys under `settings` are the flags. `sinks` takes the names of [`-sinks`](#declaring-your-own-logger) as a list, one per item, not comma-separated. An unknown key or a misspelled name stops the run:

```yaml
version: "2"
linters:
  enable:
    - errlogreturn
  settings:
    custom:
      errlogreturn:
        type: module
        description: Reports an error that is both logged and returned.
        settings:
          sinks:
            - example.com/telemetry.Send
            - (example.com/telemetry.Client).Capture
```

Then build and run it:

```bash
golangci-lint custom  # writes ./custom-gcl
./custom-gcl run ./...
```

From a Go program of your own, build an analyzer with `errlogreturn.New`. It takes the same settings as an `errlogreturn.Config`, and has no flags. `errlogreturn.Analyzer` is the one with flags:

```go
a, err := errlogreturn.New(errlogreturn.Config{
	Sinks: []string{"example.com/log.Report"},
})
```

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

The linter leans toward silence. A missed report costs one extra log line. A false report teaches people to ignore the linter.

| Mark | Meaning |
| --- | --- |
| ✅ | Valid code. It is not reported |
| ❌ | Invalid code. It is reported, although it looks close to a ✅ case |
| ⚠️ | Invalid code. It logs and returns one error, but is not reported |

<table>
<thead>
<tr><th>Code</th><th>Valid?</th><th>Reason</th></tr>
</thead>
<tbody>
<tr>
<td>

```go
err := load()
if fail {
	slog.Error("failed", "err", err)
	return nil
}
return err
```

</td>
<td>✅</td>
<td>No single path runs both the log and the <code>return</code></td>
</tr>
<tr>
<td>

```go
err := load()
if verbose {
	slog.Error("failed", "err", err)
}
if !verbose {
	return err
}
return nil
```

</td>
<td>✅</td>
<td>The path that logs never reaches that <code>return</code>. Two <code>switch mode</code> statements over one value work the same way</td>
</tr>
<tr>
<td>

```go
const debug = false

err := load()
if debug {
	slog.Error("failed", "err", err)
}
return err
```

</td>
<td>✅</td>
<td>Code under a false constant never runs</td>
</tr>
<tr>
<td>

```go
err := load()
slog.Error("failed", "err", err)
return ErrNotFound
```

</td>
<td>✅</td>
<td>The returned error is another one</td>
</tr>
<tr>
<td>

```go
func ToPublic(error) error {
	return ErrInternal
}

err := load()
slog.Error("failed", "err", err)
return ToPublic(err)
```

</td>
<td>✅</td>
<td><code>ToPublic</code> returns a sentinel, not its argument. This holds for a helper in any package</td>
</tr>
<tr>
<td>

```go
err := load()
slog.Info("done", "ok", err == nil)
return err
```

</td>
<td>✅</td>
<td>A boolean or a number says whether the call failed, not what the error was. <code>errors.Is(err, target)</code> is the same</td>
</tr>
<tr>
<td>

```go
e := errnoOf() // syscall.Errno
slog.Error("failed", "errno", e)
return e
```

</td>
<td>❌</td>
<td><code>syscall.Errno</code> is a number, but it is also an error. So it counts</td>
</tr>
<tr>
<td>

```go
j.Err = load()
slog.Error("failed", "id", j.ID)
return j.Err
```

</td>
<td>✅</td>
<td>Each field is read on its own, through calls and getters too</td>
</tr>
<tr>
<td>

```go
err := load()
slog.Error("failed", "err", err)
fill(&err)
return err
```

</td>
<td>✅</td>
<td><code>fill</code> writes a new error into <code>err</code>. A later assignment, or a closure that assigns <code>err</code>, works the same way</td>
</tr>
<tr>
<td>

```go
func Do() (err error) {
	defer func() {
		if err != nil {
			slog.Error("failed", "err", err)
			err = nil
		}
	}()
	return load()
}
```

</td>
<td>✅</td>
<td>The deferred closure clears <code>err</code> right after it logs it. A deferred closure that always sets <code>err</code> works the same way</td>
</tr>
<tr>
<td>

```go
c.err = load()
slog.Error("failed", "err", c.err)
c.bump() // writes c.n only
return c.err
```

</td>
<td>❌</td>
<td><code>bump</code> writes another field. <code>c.err</code> still holds the logged error</td>
</tr>
<tr>
<td>

```go
func Do(f *os.File) (err error) {
	defer func() {
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
	}()
	err = load()
	slog.Error("failed", "err", err)
	return err
}
```

</td>
<td>❌</td>
<td>The deferred <code>Close</code> sets <code>err</code> only when it is <code>nil</code>. The logged error is still returned</td>
</tr>
<tr>
<td>

```go
var last error
for _, id := range ids {
	if last != nil {
		slog.Warn("retrying", "err", last)
	}
	last = get(id)
}
return last
```

</td>
<td>✅</td>
<td>The warning logs the error of the attempt before. The <code>return</code> hands back the last one, which was never logged</td>
</tr>
<tr>
<td>

```go
var err error
for i := 0; ; i++ {
	err = load()
	if err == nil || i == n-1 {
		break
	}
	slog.Warn("retrying", "err", err)
}
return err
```

</td>
<td>✅</td>
<td>The log cannot run on the last iteration. So the loop never ends right after the log</td>
</tr>
<tr>
<td>

```go
err := load()
if err == nil {
	slog.Info("ok", "err", err)
	return err
}
return nil
```

</td>
<td>✅</td>
<td>The logged error is <code>nil</code> there. A <code>nil</code> error is not a failure</td>
</tr>
</tbody>
</table>

These log and return one error, but are not reported:

<table>
<thead>
<tr><th>Code</th><th>Valid?</th><th>Reason</th></tr>
</thead>
<tbody>
<tr>
<td>

```go
var err error
for i := range n {
	err = load()
	if i%2 == 0 {
		slog.Warn("failed", "err", err)
	}
}
return err
```

</td>
<td>⚠️</td>
<td><code>i%2 == 0</code> cannot be evaluated. It is taken as a test that skips the last iteration</td>
</tr>
<tr>
<td>

```go
type Reporter interface {
	Report(msg string, args ...any)
}

err := load()
r.Report("failed", "err", err)
return err
```

</td>
<td>⚠️</td>
<td>The callee of an interface method or a function value is not known. It is taken to log nothing, and to pass its error arguments on to its error result. <a href="#declaring-your-own-logger">Declare it</a> if it logs</td>
</tr>
<tr>
<td>

```go
lastErr = load() // a global
logLast()        // logs lastErr
return lastErr
```

</td>
<td>⚠️</td>
<td>An error stored in a global and read in another function is not followed. Neither is an error sent through a channel</td>
</tr>
<tr>
<td>

```go
e := err2
if a {
	e = err1
}
slog.Error("failed", "err", e)
return err1
```

</td>
<td>⚠️</td>
<td><code>e</code> may hold either error. It matches only a <code>return</code> of <code>e</code> itself</td>
</tr>
<tr>
<td>

```go
func logN(err error, n int) {
	if n > 0 {
		logN(err, n-1)
		return
	}
	slog.Error("failed", "err", err)
}

err := load()
logN(err, 3)
return err
```

</td>
<td>⚠️</td>
<td>A recursive call is taken to log nothing. So <code>logN</code> does not count as a logger</td>
</tr>
</tbody>
</table>

> [!NOTE]
> Nothing is reported in a generated file, since a report there cannot be acted on. Its helpers are still followed.

## Ignoring a report

Put `//errlogreturn:ignore` on the reported line, or on the line above it. Write the reason after `//`, and say why both are needed.

Write it with no space after the slashes, as with `//go:` directives. `// errlogreturn:ignore` is a plain comment.

A report is anchored on the first line of the statement that logs. A directive above a multi-line call therefore covers it.

```go
err := dial()
//errlogreturn:ignore // the caller only traces this error
slog.Error("open failed",
	"err", err)
return err
```

> [!NOTE]
> A directive that does nothing is reported: an unused ignore, a misplaced sink, an unknown name, or text after the name that is not behind `//`.

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

> [!TIP]
> The flag works the same through `go vet`.
>
> ```bash
> go vet -vettool=$(which errlogreturn) -sinks 'example.com/telemetry.Send' ./...
> ```

| Kind | Spelling |
| --- | --- |
| A function | `example.com/telemetry.Send` |
| A method | `(example.com/telemetry.Client).Capture` |
| A method with a pointer receiver | `(*example.com/telemetry.Client).Capture` |
| A method of a generic type | `(example.com/telemetry.Queue[T]).Push` |
| A generic method of a generic type | `(example.com/telemetry.Queue[T]).Map` |

The `*` may be written or left out, whatever the receiver is. A generic method is written without its own type parameters. A name that is not spelled this way stops the run with an error. A name that is spelled correctly but names nothing is not reported, so check the package path.

## AI usage

errlogreturn is developed with AI coding agents (Claude Code). The fixtures under `testdata/src/` define what it reports.

## License

MIT
