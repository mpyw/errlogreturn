package internal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mpyw/errlogreturn/internal/store"
)

// Fact carries a function's summary to the packages that import it.
type Fact struct {
	// FlowsTo[i] is a bit set of the results that parameter i may carry into.
	FlowsTo []uint64
	// Logs[i] reports that parameter i is logged on every path that returns.
	Logs []bool
	// Must[i] reports that the function assigns the whole of what parameter
	// i points to on every path that returns.
	Must []bool
	// FieldFlows lists the results made from memory below a parameter,
	// where the parameter itself does not flow.
	FieldFlows []FactFieldFlow
	// Sink reports that the function is declared with //errlogreturn:sink.
	Sink bool
	// Writes[i] lists the paths below parameter i that the function may
	// write. An empty path is everything parameter i points to.
	Writes [][][]int
}

// factPath renders a written path: a field by its index, an element as [],
// and a loaded pointer as *. The empty path renders as nothing.
func factPath(p []int) string {
	var b strings.Builder
	for _, s := range p {
		switch s {
		case store.Elem:
			b.WriteString(".[]")
		case store.Deref:
			b.WriteString(".*")
		default:
			if i, ok := store.IsIndex(s); ok {
				b.WriteString(".[" + strconv.Itoa(i) + "]")
				continue
			}
			b.WriteString("." + strconv.Itoa(s))
		}
	}
	return b.String()
}

// FactFieldFlow is a result made from the memory at Path below input In.
type FactFieldFlow struct {
	In, Out int
	Path    []int
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
	for _, w := range f.Writes {
		if len(w) > 0 {
			return true
		}
	}
	return len(f.FieldFlows) > 0
}

// String renders the fact the way the tests spell it: "carries p0→r1" for a
// parameter carried into a result, "logs p0" for a parameter always logged,
// "writes p0.1" for field 1 of what a parameter points to, which the
// function may write, "writes p0" for all of it,
// "sink" for a declared sink, and "carries nothing" for a summary published
// only to stop a caller from guessing.
func (f *Fact) String() string {
	var parts []string
	for i, b := range f.FlowsTo {
		for j := range 64 {
			if b&(1<<j) != 0 {
				parts = append(parts, fmt.Sprintf("carries p%d→r%d", i, j))
			}
		}
	}
	for _, ff := range f.FieldFlows {
		parts = append(parts, fmt.Sprintf("carries p%d%s→r%d", ff.In, factPath(ff.Path), ff.Out))
	}
	for i, l := range f.Logs {
		if l {
			parts = append(parts, fmt.Sprintf("logs p%d", i))
		}
	}
	for i, paths := range f.Writes {
		for _, p := range paths {
			parts = append(parts, "writes p"+strconv.Itoa(i)+factPath(p))
		}
	}
	if f.Sink {
		parts = append(parts, "sink")
	}
	if len(parts) == 0 {
		return "carries nothing"
	}
	return strings.Join(parts, ", ")
}
