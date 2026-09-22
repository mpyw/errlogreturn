// Package errlogreturn reports an error that is both logged and returned.
//
// An error should be handled once. A function that logs an error and then
// returns it hands the same failure to a caller that will log it again, so
// one failure shows up as several log lines, one per layer.
package errlogreturn

import (
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"

	"github.com/mpyw/errlogreturn/internal"
)

// Analyzer reports an error that is both logged and returned on one path.
var Analyzer = &analysis.Analyzer{
	Name:      "errlogreturn",
	Doc:       "reports an error that is both logged and returned",
	URL:       "https://github.com/mpyw/errlogreturn",
	Requires:  []*analysis.Analyzer{buildssa.Analyzer},
	FactTypes: []analysis.Fact{new(internal.Fact)},
	Run:       run,
}

// ErrNoSSA is returned when the pass carries no buildssa result, which means
// the analyzer was registered without its requirement.
var ErrNoSSA = internal.ErrRunWithoutSSA

// sinks holds the -sinks flag.
var sinks string

func init() {
	Analyzer.Flags.StringVar(&sinks, "sinks", "",
		"comma-separated functions that log every argument, "+
			"spelled pkg/path.Func or (pkg/path.Type).Method")
}

func run(pass *analysis.Pass) (any, error) {
	cfg := internal.Config{Sinks: make(map[string]bool)}
	for _, s := range strings.Split(sinks, ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.Sinks[s] = true
		}
	}
	return internal.Run(pass, cfg)
}
