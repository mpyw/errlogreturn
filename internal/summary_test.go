package internal

import (
	"testing"

	"golang.org/x/tools/go/ssa"
)

func TestSummaryInputBehind(t *testing.T) {
	e := &ssa.Parameter{}
	boxed := &ssa.ChangeInterface{X: &ssa.MakeInterface{X: &ssa.ChangeType{X: e}}}
	if got := summaryInputBehind(boxed); got != ssa.Value(e) {
		t.Errorf("summaryInputBehind through three conversions = %v", got)
	}
}
