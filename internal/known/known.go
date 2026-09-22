// Package known describes the libraries the analysis does not read from their
// bodies.
//
// Their loggers write through buffers and writers that a summary cannot follow,
// and formatting goes through reflection, so a summary of their code would say
// that nothing carries anything and nothing logs.
package known

import (
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/typeutil"
)

const (
	pkgSlog       = "log/slog"
	pkgZerolog    = "github.com/rs/zerolog"
	pkgZerologLog = "github.com/rs/zerolog/log"
	pkgZap        = "go.uber.org/zap"
	pkgLogrus     = "github.com/sirupsen/logrus"
)

// carriers are the packages whose every function and method is taken to carry
// each input into each result.
var carriers = map[string]bool{
	"errors":      true,
	"fmt":         true,
	"strconv":     true,
	"strings":     true,
	pkgSlog:       true,
	pkgZerolog:    true,
	pkgZerologLog: true,
	pkgZap:        true,
	pkgLogrus:     true,
}

// Package reports whether obj belongs to a package described here, so that its
// body is never summarized.
func Package(obj *types.Func) bool {
	pkg, _, _ := parts(obj)
	return pkg == "log" || carriers[pkg]
}

// Carries reports whether obj carries each input into each result.
func Carries(obj *types.Func) bool {
	pkg, _, _ := parts(obj)
	return carriers[pkg]
}

// Accessor reports whether obj is a method that renders or unwraps its
// receiver: Error, String or Unwrap with no parameters. It carries its
// receiver into its result, wherever it is declared.
func Accessor(obj *types.Func) bool {
	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || sig.Params().Len() != 0 {
		return false
	}
	switch obj.Name() {
	case "Error", "String", "Unwrap":
		return true
	}
	return false
}

// Mutates reports whether obj writes its arguments into its receiver. A zerolog
// event is built in place, so an event whose chain is broken across statements
// still carries what an earlier call added.
func Mutates(obj *types.Func) bool {
	pkg, recv, _ := parts(obj)
	return pkg == pkgZerolog && recv == "Event"
}

// Logs reports whether a call to obj is a log call of a known logger, and which
// of its inputs it logs. in holds the receiver, if any, then the arguments.
//
// Debug and trace levels are not counted: a debug line is a trace of what
// happened, not the handling of a failure. Fatal and panic levels are not
// counted either, since the function never returns after them. A level passed
// as a value counts only when it is a constant in range.
func Logs(obj *types.Func, in []ssa.Value) ([]ssa.Value, bool) {
	pkg, recv, name := parts(obj)
	method := recv != ""
	arg := func(i int) ssa.Value {
		if method {
			i++
		}
		if i < len(in) {
			return in[i]
		}
		return nil
	}

	switch pkg {
	case "log":
		switch name {
		case "Print", "Printf", "Println":
			return in, true
		case "Output":
			return in, recv == "Logger"
		}
	case "fmt":
		switch name {
		case "Print", "Printf", "Println":
			return in, true
		case "Fprint", "Fprintf", "Fprintln":
			if len(in) > 0 && stdStream(in[0]) {
				return in[1:], true
			}
		}
	case pkgSlog:
		if recv != "" && recv != "Logger" {
			return nil, false
		}
		switch name {
		case "Info", "InfoContext", "Warn", "WarnContext", "Error", "ErrorContext":
			return in, true
		case "Log", "LogAttrs":
			// slog.LevelInfo is 0; everything above it is a warning or
			// an error, and slog has no fatal level.
			return in, levelIn(arg(1), 0, 1<<31)
		}
	case pkgZerolog:
		if recv == "Event" {
			switch name {
			case "Msg", "Msgf", "MsgFunc", "Send":
				return in, len(in) > 0 && eventLogs(in[0], make(map[ssa.Value]bool))
			}
		}
	case pkgZap:
		switch recv {
		case "Logger":
			switch name {
			case "Info", "Warn", "Error", "DPanic":
				return in, true
			case "Log":
				// zapcore levels: Info 0, Warn 1, Error 2, DPanic 3.
				return in, levelIn(arg(0), 0, 3)
			}
		case "SugaredLogger":
			base, _ := splitSuffix(name, "w", "f", "ln")
			switch base {
			case "Info", "Warn", "Error", "DPanic":
				return in, true
			case "Log":
				return in, levelIn(arg(0), 0, 3)
			}
		}
	case pkgLogrus:
		if recv != "" && recv != "Logger" && recv != "Entry" {
			return nil, false
		}
		base, _ := splitSuffix(name, "f", "ln")
		switch base {
		case "Info", "Warn", "Warning", "Error", "Print":
			return in, true
		case "Log":
			// logrus levels: Error 2, Warn 3, Info 4.
			return in, levelIn(arg(0), 2, 4)
		}
	}
	return nil, false
}

// eventLogs reports whether a zerolog event was started at a level that
// counts. The receiver is traced back through the builder methods to the call
// that created the event; an event from anywhere else does not count.
func eventLogs(v ssa.Value, seen map[ssa.Value]bool) bool {
	if seen[v] {
		return true
	}
	seen[v] = true
	switch v := v.(type) {
	case *ssa.Phi:
		for _, e := range v.Edges {
			if !eventLogs(e, seen) {
				return false
			}
		}
		return len(v.Edges) > 0
	case *ssa.Call:
		cc := v.Common()
		obj := typeutil.StaticFunc(cc)
		if obj == nil {
			return false
		}
		pkg, recv, name := parts(obj)
		if pkg == pkgZerolog && recv == "Event" {
			return len(cc.Args) > 0 && eventLogs(cc.Args[0], seen)
		}
		if (pkg == pkgZerolog && recv == "Logger") || (pkg == pkgZerologLog && recv == "") {
			switch name {
			case "Info", "Warn", "Error", "Err", "Log":
				return true
			case "WithLevel":
				// zerolog levels: Info 1, Warn 2, Error 3.
				lvl := cc.Args[len(cc.Args)-1]
				return levelIn(lvl, 1, 3)
			}
		}
	}
	return false
}

// levelIn reports whether v is a constant level in [lo, hi].
func levelIn(v ssa.Value, lo, hi int64) bool {
	k, ok := typeutil.Unbox(v).(*ssa.Const)
	if !ok || k.Value == nil || k.Value.Kind() != constant.Int {
		return false
	}
	n, exact := constant.Int64Val(k.Value)
	return exact && n >= lo && n <= hi
}

// splitSuffix splits one of the given suffixes off name. The suffix is
// "" when none matched.
func splitSuffix(name string, suffixes ...string) (string, string) {
	for _, s := range suffixes {
		if base, ok := strings.CutSuffix(name, s); ok && base != "" {
			return base, s
		}
	}
	return name, ""
}

// stdStream reports whether v is os.Stdout or os.Stderr.
func stdStream(v ssa.Value) bool {
	u, ok := typeutil.Unbox(v).(*ssa.UnOp)
	if !ok {
		return false
	}
	g, ok := u.X.(*ssa.Global)
	if !ok || g.Pkg == nil || g.Pkg.Pkg.Path() != "os" {
		return false
	}
	return g.Name() == "Stdout" || g.Name() == "Stderr"
}

// parts splits obj into its package path, receiver type name and name.
func parts(obj *types.Func) (pkg, recv, name string) {
	if obj == nil {
		return "", "", ""
	}
	if obj.Pkg() != nil {
		pkg = obj.Pkg().Path()
	}
	if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil {
		t := sig.Recv().Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			recv = n.Obj().Name()
		}
	}
	return pkg, recv, obj.Name()
}
