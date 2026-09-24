// Package errlogreturn reports an error that is both logged and returned.
//
// An error should be handled once. A function that logs an error and then
// returns it hands the same failure to a caller that will log it again, so
// one failure shows up as several log lines, one per layer.
package errlogreturn

import (
	"fmt"
	"regexp"
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
	m, err := parseSinks(v)
	if err != nil {
		return err
	}
	f.raw, f.names = v, m
	return nil
}

// sinkSpelling is a function or a method as go/types names it:
// pkg/path.Func, or (pkg/path.Type).Method with an optional * before the
// type, and type arguments after it for a generic type.
var sinkSpelling = regexp.MustCompile(`^(\(\*?[^()*\s\[\]]+\.[\pL_][\pL\pN_]*(\[[^()\[\]]+\])?\)|[^()*\s\[\]]+)\.[\pL_][\pL\pN_]*$`)

// parseSinks reads a -sinks value. The * of a pointer receiver is dropped,
// so that either spelling names the method whatever its receiver is.
func parseSinks(flag string) (map[string]bool, error) {
	m := make(map[string]bool)
	for _, s := range splitSinks(flag) {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !sinkSpelling.MatchString(s) {
			return nil, fmt.Errorf("%q is not spelled pkg/path.Func or (pkg/path.Type).Method", s)
		}
		m[strings.Replace(s, "(*", "(", 1)] = true
	}
	return m, nil
}

// splitSinks splits a -sinks value at the commas outside brackets, so that
// the type arguments of (pkg.Map[K, V]).Put stay in one name.
func splitSinks(flag string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range flag {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, flag[start:i])
				start = i + 1
			}
		}
	}
	return append(out, flag[start:])
}
