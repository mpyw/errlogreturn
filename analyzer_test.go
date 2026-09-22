package errlogreturn_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/mpyw/errlogreturn"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), errlogreturn.Analyzer, "basic", "helpers", "paths", "zerologuse", "zapuse", "logrususe",
		"crosspkg", "directives", "generated", "generics")
}

func TestSinksFlag(t *testing.T) {
	if err := errlogreturn.Analyzer.Flags.Set("sinks", "sinkflag.Report, (sinkflag.Client).Send"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errlogreturn.Analyzer.Flags.Set("sinks", "") })
	analysistest.Run(t, analysistest.TestData(), errlogreturn.Analyzer, "sinkflag")
}
