package errlogreturn_test

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/mpyw/errlogreturn"
)

// TestNew runs an analyzer that New builds with the sinks TestSinksFlag sets
// through the flag, and checks that it has no flags of its own.
func TestNew(t *testing.T) {
	t.Parallel()
	a, err := errlogreturn.New(errlogreturn.Config{
		Sinks: []string{"sinkflag.Report", "(sinkflag.Client).Send", "(*sinkflag.Value).Push", "(sinkflag.Gen[T]).Emit", "(sinkflag.Gen[T]).Put"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Flags.Lookup("sinks") != nil {
		t.Error("an analyzer New built has flags, want none")
	}
	analysistest.Run(t, analysistest.TestData(), a, "sinkflag")
}

func TestNewRejectsBadSinks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		sinks []string
		want  string
	}{
		{"two names in one item", []string{"a.F,b.G"}, "more than one name"},
		{"a misspelled sink", []string{"garbage(("}, "is not spelled"},
	}
	for _, tt := range tests {
		_, err := errlogreturn.New(errlogreturn.Config{Sinks: tt.sinks})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: got error %v, want one containing %q", tt.name, err, tt.want)
		}
	}
}
