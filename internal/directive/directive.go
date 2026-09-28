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

	"golang.org/x/tools/go/ast/inspector"
)

// tool is the tool name of every directive, as in //errlogreturn:ignore.
// Directives follow the syntax of https://go.dev/doc/comment#directives, and
// go/ast parses them. A comment with a space after the slashes is prose.
const tool = "errlogreturn"

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

// Scan reads the directives in the files in. A generated file is read for
// sinks only: nothing is reported in it, so neither its ignores nor its
// problems mean anything.
func Scan(fset *token.FileSet, in *inspector.Inspector, info *types.Info) *Set {
	s := &Set{
		fset:    fset,
		sinks:   make(map[*types.Func]bool),
		ignores: make(map[string]map[int]*ignore),
	}
	for file := range in.Root().Children() {
		s.scanFile(file, info)
	}
	return s
}

func (s *Set) scanFile(file inspector.Cursor, info *types.Info) {
	f := file.Node().(*ast.File)
	// Each comment is parsed once. The syntax is walked only when the file
	// has a sink directive, since only a sink needs its declaration.
	type found struct {
		cm   *ast.Comment
		verb string
		args string
	}
	var directives []found
	placed := make(map[*ast.Comment]bool) // a sink directive, and whether it has a declaration
	for _, cg := range f.Comments {
		for _, cm := range cg.List {
			if v, args, ok := verb(cm); ok {
				directives = append(directives, found{cm: cm, verb: v, args: args})
				if v == "sink" && args == "" {
					placed[cm] = false
				}
			}
		}
	}
	if len(placed) > 0 {
		claim := func(doc *ast.CommentGroup, id *ast.Ident) {
			if doc == nil {
				return
			}
			for _, cm := range doc.List {
				if _, ok := placed[cm]; ok {
					placed[cm] = true
					if obj, ok := info.Defs[id].(*types.Func); ok {
						s.sinks[obj] = true
					}
				}
			}
		}
		for c := range file.Preorder((*ast.FuncDecl)(nil), (*ast.InterfaceType)(nil)) {
			switch n := c.Node().(type) {
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
		}
	}
	if ast.IsGenerated(f) {
		return
	}

	// Keyed by the file and line on disk. A //line directive renames the
	// positions below it, and two of them can give two lines one number.
	lines := make(map[int]*ignore)
	s.ignores[s.fset.PositionFor(f.Pos(), false).Filename] = lines
	for _, d := range directives {
		switch {
		case d.args != "" && (d.verb == "ignore" || d.verb == "sink"):
			s.problems = append(s.problems, Problem{Pos: d.cm.Pos(),
				Message: "errlogreturn:" + d.verb + " takes no argument; write a reason after //"})
		case d.verb == "ignore":
			lines[s.fset.PositionFor(d.cm.Pos(), false).Line] = &ignore{pos: d.cm.Pos()}
		case d.verb == "sink":
			if !placed[d.cm] {
				s.problems = append(s.problems, Problem{Pos: d.cm.Pos(),
					Message: "errlogreturn:sink belongs in the doc comment of a function or an interface method"})
			}
		default:
			s.problems = append(s.problems, Problem{Pos: d.cm.Pos(),
				Message: "unknown directive errlogreturn:" + d.verb})
		}
	}
}

// verb returns the directive's name and arguments, when cm is one of this
// tool's. A trailing comment is a reason and is dropped first, so
// //errlogreturn:ignore // why and //errlogreturn:ignore//why are both a bare
// ignore. Neither directive takes an argument: text after the name that is
// not behind // is reported rather than read as a reason, so that every
// tool of this family reads a directive the same way.
func verb(cm *ast.Comment) (name, args string, ok bool) {
	text := cm.Text
	if body, line := strings.CutPrefix(text, "//"); line {
		if i := strings.Index(body, "//"); i >= 0 {
			text = "//" + body[:i]
		}
	}
	d, ok := ast.ParseDirective(cm.Slash, text)
	if !ok || d.Tool != tool {
		return "", "", false
	}
	return d.Name, d.Args, true
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
	p := s.fset.PositionFor(pos, false)
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
