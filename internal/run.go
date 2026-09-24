// Package internal implements the errlogreturn analysis.
//
// The analysis is a taint analysis over SSA. An error value is the source,
// and it has two exits: a logger, and a return statement. A report is made
// when one value reaches both on a single path.
//
// Each function is summarized by two facts about its inputs: which results an
// input may carry into, and which inputs it logs on every path that returns.
// Summaries cross package boundaries as analysis facts, so a helper that logs
// its argument is recognized wherever it is called, and a user-written logger
// wrapper needs no configuration.
//
// Every judgement leans toward silence. An unknown callee logs nothing and
// carries only its error arguments into an error result, a helper counts as
// logging only when every path logs, and a logging call whose level cannot be
// read is not a log.
package internal

import (
	"errors"
	"go/types"
	"slices"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// ErrRunWithoutSSA is returned by Run when the pass carries no buildssa
// result, which means the analyzer was registered without its requirement.
var ErrRunWithoutSSA = errors.New("errlogreturn: buildssa result missing")

// Run analyzes one package.
func Run(pass *analysis.Pass, cfg Config) (any, error) {
	info, ok := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	if !ok {
		return nil, ErrRunWithoutSSA
	}
	c := newChecker(pass, cfg)
	for _, p := range c.directives.Problems() {
		pass.Report(analysis.Diagnostic{Pos: p.Pos, Message: p.Message})
	}

	fns := runFuncs(info)
	for _, fn := range fns {
		c.summary(fn)
	}
	runExport(c, info)
	for _, fn := range fns {
		c.check(fn)
	}
	// Every function the pass reports on is seen in full, in a package's
	// test variant as in the ordinary one, so an ignore that silenced
	// nothing here silences nothing anywhere.
	for _, pos := range c.directives.Unused() {
		pass.Reportf(pos, "unused errlogreturn:ignore directive")
	}
	return nil, nil
}

// runFuncs lists every function written in the package, function literals
// included, in a stable order.
func runFuncs(info *buildssa.SSA) []*ssa.Function {
	var out []*ssa.Function
	seen := make(map[*ssa.Function]bool)
	var add func(fn *ssa.Function)
	add = func(fn *ssa.Function) {
		if seen[fn] {
			return
		}
		seen[fn] = true
		out = append(out, fn)
		for _, anon := range fn.AnonFuncs {
			add(anon)
		}
	}
	for _, fn := range info.SrcFuncs {
		add(fn)
	}
	return out
}

// runExport publishes the summary of every named function, and the sink
// declarations that have no body to summarize.
//
// A summary that says nothing is still published when a caller would guess
// otherwise. With no fact, a caller in another package takes an error result
// to carry the error arguments, which is wrong for a helper that translates
// an error into a sentinel.
func runExport(c *checker, info *buildssa.SSA) {
	for _, fn := range info.SrcFuncs {
		obj, ok := fn.Object().(*types.Func)
		if !ok || obj.Pkg() != c.pass.Pkg {
			continue
		}
		s := c.summary(fn)
		n := len(fn.Params)
		var fields []FactFieldFlow
		for _, ff := range s.fields {
			if ff.In < n {
				fields = append(fields, ff)
			}
		}
		f := &Fact{FlowsTo: s.flows[:n], Logs: s.logs[:n], Writes: s.writes[:n], FieldFlows: fields, Sink: c.directives.Sink(obj)}
		// Must says something only beside a write, which it qualifies, so
		// it is exported with the writes and renders nothing of its own.
		if slices.Contains(s.must[:n], true) {
			f.Must = s.must[:n]
		}
		if f.Sink || f.meaningful() || runGuessWrong(fn, f) {
			c.pass.ExportObjectFact(obj, f)
		}
	}
	for _, obj := range c.directives.Sinks() {
		var f Fact
		if !c.pass.ImportObjectFact(obj, &f) {
			c.pass.ExportObjectFact(obj, &Fact{Sink: true})
		}
	}
}

// runGuessWrong reports whether a caller with no fact about fn would say that
// an error result carries a parameter that f says it does not. The guess
// covers every parameter an error can be passed to.
func runGuessWrong(fn *ssa.Function, f *Fact) bool {
	res := fn.Signature.Results()
	for j := range min(res.Len(), 64) {
		if !typeutil.IsError(res.At(j).Type()) {
			continue
		}
		for i, p := range fn.Params {
			if typeutil.TakesError(p.Type()) && f.FlowsTo[i]&(1<<j) == 0 {
				return true
			}
		}
	}
	return false
}
