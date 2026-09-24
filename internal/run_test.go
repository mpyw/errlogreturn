package internal

import (
	"errors"
	"go/token"
	"testing"

	"golang.org/x/tools/go/analysis"
)

func TestRunWithoutSSA(t *testing.T) {
	pass := &analysis.Pass{Fset: token.NewFileSet(), ResultOf: map[*analysis.Analyzer]any{}}
	if _, err := Run(pass, Config{}); !errors.Is(err, ErrRunWithoutSSA) {
		t.Errorf("Run without buildssa = %v", err)
	}
}
