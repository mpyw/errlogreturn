// Package errlogreturn reports an error that is both logged and returned.
//
// An error should be handled once. A function that logs an error and then
// returns it hands the same failure to a caller that will log it again, so
// one failure shows up as several log lines, one per layer.
package errlogreturn

import (
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/analysis/passes/inspect"

	"github.com/mpyw/errlogreturn/internal"
	"github.com/mpyw/errlogreturn/internal/sinkname"
)

// Analyzer reports an error that is both logged and returned on one path.
var Analyzer = &analysis.Analyzer{
	Name:      "errlogreturn",
	Doc:       "reports an error that is both logged and returned",
	URL:       "https://github.com/mpyw/errlogreturn",
	Requires:  []*analysis.Analyzer{buildssa.Analyzer, inspect.Analyzer},
	FactTypes: []analysis.Fact{new(internal.Fact)},
	Run:       run,
}

// ErrNoSSA is returned when the pass carries no buildssa result, which means
// the analyzer was registered without its requirement.
var ErrNoSSA = internal.ErrRunWithoutSSA

// sinks holds the -sinks flag, checked as it is set.
var sinks sinkFlag

func init() {
	Analyzer.Flags.Var(&sinks, "sinks",
		"comma-separated functions that log every argument, "+
			"spelled pkg/path.Func or (pkg/path.Type).Method")
}

func run(pass *analysis.Pass) (any, error) {
	return internal.Run(pass, internal.Config{Sinks: sinks.names})
}

// sinkFlag is the -sinks flag. A name that is not spelled like a function is
// refused when the flag is parsed, since a typo would otherwise switch its
// sink off in silence.
type sinkFlag struct {
	raw   string
	names map[string]bool
}

// String returns the flag as it was set.
func (f *sinkFlag) String() string { return f.raw }

// Set parses a comma-separated list of names.
func (f *sinkFlag) Set(v string) error {
	m, err := sinkname.Parse(v)
	if err != nil {
		return err
	}
	f.raw, f.names = v, m
	return nil
}
