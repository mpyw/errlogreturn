package plugin_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"

	_ "github.com/mpyw/errlogreturn/plugin"
)

func TestPluginDefaults(t *testing.T) {
	a := buildAnalyzer(t, nil)
	analysistest.Run(t, testdata(t), a, "basic", "crosspkg")
}

func TestPluginSinks(t *testing.T) {
	a := buildAnalyzer(t, map[string]any{
		"sinks": "sinkflag.Report, (sinkflag.Client).Send, (*sinkflag.Value).Push, (sinkflag.Gen[T]).Emit, (sinkflag.Gen[T]).Put",
	})
	analysistest.Run(t, testdata(t), a, "sinkflag")
}

func TestPluginRejectsBadSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings any
		want     string
	}{
		{"an unknown key", map[string]any{"sink": "a.F"}, `unknown field "sink"`},
		{"a value that is not a string", map[string]any{"sinks": []any{"a.F"}}, "reading settings"},
		{"a misspelled sink", map[string]any{"sinks": "garbage(("}, "is not spelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newPlugin(t)(tt.settings)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got error %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestPluginLoadMode(t *testing.T) {
	p, err := newPlugin(t)(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.GetLoadMode(); got != register.LoadModeTypesInfo {
		t.Errorf("load mode %q, want %q", got, register.LoadModeTypesInfo)
	}
}

// testdata gives the module's fixtures, shared with the analyzer's tests.
func testdata(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// newPlugin finds the constructor the package registered.
func newPlugin(t *testing.T) register.NewPlugin {
	t.Helper()
	np, err := register.GetPlugin("errlogreturn")
	if err != nil {
		t.Fatal(err)
	}
	return np
}

// buildAnalyzer builds the plugin's one analyzer from settings.
func buildAnalyzer(t *testing.T, settings any) *analysis.Analyzer {
	t.Helper()
	p, err := newPlugin(t)(settings)
	if err != nil {
		t.Fatal(err)
	}
	as, err := p.BuildAnalyzers()
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 {
		t.Fatalf("got %d analyzers, want 1", len(as))
	}
	return as[0]
}
