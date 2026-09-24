package known

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// knownBuild builds src as the package at path, and returns it.
func knownBuild(t *testing.T, path, src string) *ssa.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	sp, _, err := ssautil.BuildPackage(&types.Config{Importer: importer.Default()}, fset, types.NewPackage(path, f.Name.Name), []*ast.File{f}, ssa.SanityCheckFunctions)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// knownCalls lists the calls in fn.
func knownCalls(fn *ssa.Function) []*ssa.Call {
	var out []*ssa.Call
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ssa.Call); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

// A module may replace a logger with a fork whose methods take fewer
// arguments. Nothing is read from a level that is not passed.
func TestLogsFork(t *testing.T) {
	sp := knownBuild(t, "github.com/sirupsen/logrus", `package logrus
type Entry struct{}
func (e *Entry) Log() {}
func F(e *Entry) { e.Log() }`)
	call := knownCalls(sp.Func("F"))[0]
	obj := call.Common().StaticCallee().Object().(*types.Func)
	in := append([]ssa.Value{}, call.Common().Args...)
	if _, ok := Logs(obj, in[:1]); ok {
		t.Error("a Log with no level logs")
	}

	zl := knownBuild(t, "github.com/rs/zerolog", `package zerolog
type Logger struct{}
type Event struct{}
func (l *Logger) WithLevel() *Event { return &Event{} }
func (e *Event) Msg(string) {}
func F(l *Logger) { l.WithLevel().Msg("x") }`)
	calls := knownCalls(zl.Func("F"))
	msg := calls[len(calls)-1]
	mobj := msg.Common().StaticCallee().Object().(*types.Func)
	if _, ok := Logs(mobj, msg.Common().Args); ok {
		t.Error("an event from a WithLevel with no level logs")
	}
}
