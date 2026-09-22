// Package directive reads the //errlogreturn: comments in a package.
//
// Two directives exist. //errlogreturn:ignore silences a report on its own
// line or the line below it. //errlogreturn:sink, in the doc comment of a
// function or an interface method, declares that the function logs every
// argument. A sink directive anywhere else, and any other directive, is
// reported.
package directive

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// prefix opens every directive. A space after the slashes is accepted, as
// people write one by hand.
const prefix = "errlogreturn:"

// Set is what the directives in a package say.
type Set struct {
	fset     *token.FileSet
	sinks    map[*types.Func]bool
	ignores  map[string]map[int]*ignore
	problems []Problem
}

// Problem is a directive that is misplaced or unknown.
type Problem struct {
	Pos     token.Pos
	Message string
}

// ignore is one ignore comment, and whether it silenced anything.
type ignore struct {
	pos  token.Pos
	used bool
}

// Scan reads the directives in files. A generated file is read for sinks
// only: nothing is reported in it, so neither its ignores nor its problems
// mean anything.
func Scan(fset *token.FileSet, files []*ast.File, info *types.Info) *Set {
	s := &Set{
		fset:    fset,
		sinks:   make(map[*types.Func]bool),
		ignores: make(map[string]map[int]*ignore),
	}
	for _, f := range files {
		s.scanFile(f, info)
	}
	return s
}

func (s *Set) scanFile(f *ast.File, info *types.Info) {
	generated := ast.IsGenerated(f)
	placed := make(map[*ast.Comment]bool)
	claim := func(doc *ast.CommentGroup, id *ast.Ident) {
		if doc == nil {
			return
		}
		for _, cm := range doc.List {
			if v, ok := verb(cm.Text); ok && v == "sink" {
				placed[cm] = true
				if obj, ok := info.Defs[id].(*types.Func); ok {
					s.sinks[obj] = true
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			claim(n.Doc, n.Name)
		case *ast.InterfaceType:
			for _, m := range n.Methods.List {
				if len(m.Names) == 1 {
					claim(m.Doc, m.Names[0])
					claim(m.Comment, m.Names[0])
				}
			}
		}
		return true
	})
	if generated {
		return
	}

	lines := make(map[int]*ignore)
	s.ignores[s.fset.Position(f.Pos()).Filename] = lines
	for _, cg := range f.Comments {
		for _, cm := range cg.List {
			v, ok := verb(cm.Text)
			if !ok {
				continue
			}
			switch v {
			case "ignore":
				lines[s.fset.Position(cm.Pos()).Line] = &ignore{pos: cm.Pos()}
			case "sink":
				if !placed[cm] {
					s.problems = append(s.problems, Problem{Pos: cm.Pos(),
						Message: "errlogreturn:sink belongs in the doc comment of a function or an interface method"})
				}
			default:
				s.problems = append(s.problems, Problem{Pos: cm.Pos(),
					Message: "unknown directive errlogreturn:" + v})
			}
		}
	}
}

// verb returns the word after the prefix, when text is a directive.
func verb(text string) (string, bool) {
	text = strings.TrimPrefix(text, "//")
	text = strings.TrimPrefix(text, " ")
	rest, ok := strings.CutPrefix(text, prefix)
	if !ok {
		return "", false
	}
	v, _, _ := strings.Cut(rest, " ")
	return v, true
}

// Sink reports whether obj is declared with //errlogreturn:sink.
func (s *Set) Sink(obj *types.Func) bool {
	return s.sinks[obj]
}

// Sinks lists the declared sinks.
func (s *Set) Sinks() []*types.Func {
	out := make([]*types.Func, 0, len(s.sinks))
	for obj := range s.sinks {
		out = append(out, obj)
	}
	return out
}

// Ignored reports whether an ignore comment on the line of pos, or the line
// above it, silences a report there, and records that it did.
func (s *Set) Ignored(pos token.Pos) bool {
	p := s.fset.Position(pos)
	lines := s.ignores[p.Filename]
	for _, l := range []int{p.Line, p.Line - 1} {
		if ig, ok := lines[l]; ok {
			ig.used = true
			return true
		}
	}
	return false
}

// Unused lists the ignore comments that silenced nothing.
func (s *Set) Unused() []token.Pos {
	var out []token.Pos
	for _, lines := range s.ignores {
		for _, ig := range lines {
			if !ig.used {
				out = append(out, ig.pos)
			}
		}
	}
	return out
}

// Problems lists the directives that are misplaced or unknown.
func (s *Set) Problems() []Problem {
	return s.problems
}
