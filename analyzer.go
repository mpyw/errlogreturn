// Package errlogreturn reports an error that is both logged and returned.
//
// An error should be handled once. A function that logs an error and then
// returns it hands the same failure to a caller that will log it again, so
// one failure shows up as several log lines, one per layer.
package errlogreturn

import (
	"fmt"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/analysis/passes/inspect"

	"github.com/mpyw/errlogreturn/internal"
	"github.com/mpyw/errlogreturn/internal/sinkname"
)

// Analyzer reports an error that is both logged and returned on one path. It
// takes its settings from flags, as the errlogreturn command and go vet give
// them. A program that holds its own settings builds an analyzer with New.
var Analyzer = analyzerWithFlags()

// ErrNoSSA is returned when the pass carries no buildssa result, which means
// the analyzer was registered without its requirement.
var ErrNoSSA = internal.ErrRunWithoutSSA

// ErrNoInspector is returned when the pass carries no inspect result, which
// means the analyzer was registered without its requirement.
var ErrNoInspector = internal.ErrRunWithoutInspector

// Config holds the settings of an analyzer that New builds. The zero Config
// names no extra sink.
type Config struct {
	// Sinks names functions that log every argument, beyond the loggers
	// errlogreturn knows, each spelled pkg/path.Func or
	// (pkg/path.Type).Method.
	Sinks []string
}

// New builds an analyzer with the settings of cfg, and no flags. Each call
// builds one of its own, so two of them share nothing. It fails on a name
// that is not spelled like a function.
func New(cfg Config) (*analysis.Analyzer, error) {
	sinks, err := sinkname.ParseEach(cfg.Sinks)
	if err != nil {
		return nil, fmt.Errorf("sinks: %w", err)
	}
	c := internal.Config{Sinks: sinks}
	return analyzerBuild(func() internal.Config { return c }), nil
}

// analyzerWithFlags builds the analyzer with its -sinks flag. The flag lives
// in the closure of Run, so nothing is shared but the analyzer itself.
func analyzerWithFlags() *analysis.Analyzer {
	// sinks holds the -sinks flag, checked as it is set.
	sinks := new(sinkFlag)
	a := analyzerBuild(func() internal.Config { return internal.Config{Sinks: sinks.names} })
	a.Flags.Var(sinks, "sinks",
		"comma-separated functions that log every argument, "+
			"spelled pkg/path.Func or (pkg/path.Type).Method")
	return a
}

// analyzerBuild builds an analyzer that runs with the settings config gives
// when a pass starts.
func analyzerBuild(config func() internal.Config) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:      "errlogreturn",
		Doc:       "reports an error that is both logged and returned",
		URL:       "https://github.com/mpyw/errlogreturn",
		Requires:  []*analysis.Analyzer{buildssa.Analyzer, inspect.Analyzer},
		FactTypes: []analysis.Fact{new(internal.Fact)},
		Run: func(pass *analysis.Pass) (any, error) {
			return internal.Run(pass, config())
		},
	}
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
