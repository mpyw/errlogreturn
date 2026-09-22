package internal

import (
	"golang.org/x/tools/go/analysis"

	"github.com/mpyw/errlogreturn/internal/directive"
)

// checker is one pass's state. Each stage keeps its own state in a struct
// declared in the file that owns it and embedded here, so that the call sites
// read c.summaries rather than c.summary.summaries, and reaching into another
// stage's state is a boundary crossing.
//
//declscope:package
type checker struct {
	pass *analysis.Pass
	cfg  Config
	// directives is what the package's //errlogreturn: comments say.
	directives *directive.Set

	summaryBook
	walkerBook
	checkBook
}

//declscope:package
func newChecker(pass *analysis.Pass, cfg Config) *checker {
	return &checker{
		pass:       pass,
		cfg:        cfg,
		directives: directive.Scan(pass.Fset, pass.Files, pass.TypesInfo),
	}
}
