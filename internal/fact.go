package internal

import (
	"fmt"
	"strings"
)

// Fact carries a function's summary to the packages that import it.
type Fact struct {
	// FlowsTo[i] is a bit set of the results that parameter i may carry into.
	FlowsTo []uint64
	// Logs[i] reports that parameter i is logged on every path that returns.
	Logs []bool
	// Sink reports that the function is declared with //errlogreturn:sink.
	Sink bool
}

// AFact marks Fact as an analysis fact.
func (*Fact) AFact() {}

// meaningful reports whether a fact says anything a caller could use.
//
//declscope:package
func (f *Fact) meaningful() bool {
	for _, b := range f.FlowsTo {
		if b != 0 {
			return true
		}
	}
	for _, l := range f.Logs {
		if l {
			return true
		}
	}
	return false
}

// String renders the fact the way the tests spell it: "carries p0→r1" for a
// parameter carried into a result, "logs p0" for a parameter always logged,
// and "sink" for a declared sink.
func (f *Fact) String() string {
	var parts []string
	for i, b := range f.FlowsTo {
		for j := range 64 {
			if b&(1<<j) != 0 {
				parts = append(parts, fmt.Sprintf("carries p%d→r%d", i, j))
			}
		}
	}
	for i, l := range f.Logs {
		if l {
			parts = append(parts, fmt.Sprintf("logs p%d", i))
		}
	}
	if f.Sink {
		parts = append(parts, "sink")
	}
	return strings.Join(parts, ", ")
}
