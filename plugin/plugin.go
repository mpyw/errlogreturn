// Package plugin registers errlogreturn as a golangci-lint module plugin.
//
// Import it from .custom-gcl.yml to build a golangci-lint binary that holds
// errlogreturn. The settings in .golangci.yml take the flags of the
// errlogreturn command by name.
package plugin

import (
	"fmt"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"

	"github.com/mpyw/errlogreturn"
	"github.com/mpyw/errlogreturn/internal"
	"github.com/mpyw/errlogreturn/internal/sinkname"
)

func init() {
	register.Plugin(errlogreturn.Analyzer.Name, newPlugin)
}

// pluginSettings is the settings block, one key per flag.
type pluginSettings struct {
	// Sinks is the -sinks flag: a comma-separated list of functions that log
	// every argument.
	Sinks string `json:"sinks"`
}

// pluginConfig is the plugin built from one settings block.
type pluginConfig struct {
	// cfg is what the settings resolve to.
	cfg internal.Config
}

func newPlugin(settings any) (register.LinterPlugin, error) {
	s, err := register.DecodeSettings[pluginSettings](settings)
	if err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	sinks, err := sinkname.Parse(s.Sinks)
	if err != nil {
		return nil, fmt.Errorf("sinks: %w", err)
	}
	return &pluginConfig{cfg: internal.Config{Sinks: sinks}}, nil
}

// BuildAnalyzers gives errlogreturn's analyzer, with the sinks the settings
// name. It is a copy, so the flags of errlogreturn.Analyzer are left alone.
func (p *pluginConfig) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	cfg := p.cfg
	a := errlogreturn.Analyzer
	return []*analysis.Analyzer{{
		Name:      a.Name,
		Doc:       a.Doc,
		URL:       a.URL,
		Requires:  a.Requires,
		FactTypes: a.FactTypes,
		Run: func(pass *analysis.Pass) (any, error) {
			return internal.Run(pass, cfg)
		},
	}}, nil
}

// GetLoadMode asks for type information, which buildssa needs.
func (*pluginConfig) GetLoadMode() string {
	return register.LoadModeTypesInfo
}
