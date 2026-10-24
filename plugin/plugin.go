// Package plugin registers errlogreturn as a golangci-lint module plugin.
//
// Import it from .custom-gcl.yml to build a golangci-lint binary that holds
// errlogreturn. The settings in .golangci.yml take the flags of the
// errlogreturn command by name. sinks is a list there, one name per item.
package plugin

import (
	"fmt"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"

	"github.com/mpyw/errlogreturn"
)

func init() {
	register.Plugin(errlogreturn.Analyzer.Name, newPlugin)
}

// pluginSettings is the settings block, one key per flag.
type pluginSettings struct {
	// Sinks is the -sinks flag: the functions that log every argument, one
	// per item rather than comma-separated.
	Sinks []string `json:"sinks"`
}

// pluginAnalyzer is the plugin built from one settings block.
type pluginAnalyzer struct {
	analyzer *analysis.Analyzer
}

func newPlugin(settings any) (register.LinterPlugin, error) {
	s, err := register.DecodeSettings[pluginSettings](settings)
	if err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	a, err := errlogreturn.New(errlogreturn.Config{Sinks: s.Sinks})
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	return &pluginAnalyzer{analyzer: a}, nil
}

// BuildAnalyzers gives the analyzer that errlogreturn.New built from the
// settings. It has no flags, so errlogreturn.Analyzer is left alone.
func (p *pluginAnalyzer) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{p.analyzer}, nil
}

// GetLoadMode asks for type information, which buildssa needs.
func (*pluginAnalyzer) GetLoadMode() string {
	return register.LoadModeTypesInfo
}
