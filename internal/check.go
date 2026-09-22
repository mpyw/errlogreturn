package internal

import (
	"fmt"
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// checkBook indexes the pass's files for reporting.
//
//declscope:package
type checkBook struct {
	// checkFiles maps a file name to its syntax, for the statement a report
	// is anchored on.
	//
	//declscope:private
	checkFiles map[string]*ast.File
	// checkGenerated holds the names of generated files. They are
	// summarized, since code calls into them, but never reported on.
	//
	//declscope:private
	checkGenerated map[string]bool
}

// checkFile returns the syntax of the named file, and whether it is
// generated. It is nil for a file the pass does not hold.
func (c *checker) checkFile(name string) (*ast.File, bool) {
	if c.checkFiles == nil {
		c.checkFiles = make(map[string]*ast.File)
		c.checkGenerated = make(map[string]bool)
		for _, f := range c.pass.Files {
			n := c.pass.Fset.Position(f.Pos()).Filename
			c.checkFiles[n] = f
			if ast.IsGenerated(f) {
				c.checkGenerated[n] = true
			}
		}
	}
	return c.checkFiles[name], c.checkGenerated[name]
}

// check reports every place in fn where an error is logged and then returned
// on the same path.
//
//declscope:package
func (c *checker) check(fn *ssa.Function) {
	for _, b := range fn.Blocks {
		for i, instr := range b.Instrs {
			u, ok := c.sinkAt(instr)
			if !ok || !c.checkReportable(instr.Pos()) {
				continue
			}
			if ret := c.checkReturned(fn, b, i, u); ret != nil {
				c.checkReport(instr, ret, u)
			}
		}
	}
}

// checkReturned finds a return reachable from the logging instruction at
// b.Instrs[i] that returns an error sharing an origin with what was logged.
//
// The walk is forward and takes each block once. Entering a block resolves its
// φ-nodes to the edge the walk came in on, so that a value logged on one branch
// is not matched with a value returned only from another.
//
// A block entered again around a loop defines its values afresh: the error a
// call returns in the next iteration is not the one logged in this one, though
// SSA names both with one value. The walk records the blocks it entered, and a
// value defined in one of them is not the value that was logged.
func (c *checker) checkReturned(fn *ssa.Function, b *ssa.BasicBlock, i int, u sinkUse) *ssa.Return {
	logAt := b.Instrs[i]
	var logged map[ssa.Value]bool
	if !u.deferred {
		logged = c.walkerErrs(fn, u.values, logAt, nil, nil)
		if len(logged) == 0 {
			return nil
		}
	}

	type frame struct {
		b       *ssa.BasicBlock
		from    int
		phis    map[*ssa.Phi]ssa.Value
		entered map[*ssa.BasicBlock]bool
	}
	seen := map[*ssa.BasicBlock]bool{b: true}
	stack := []frame{{b: b, from: i + 1, phis: map[*ssa.Phi]ssa.Value{}, entered: map[*ssa.BasicBlock]bool{}}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, in := range f.b.Instrs[f.from:] {
			ret, ok := in.(*ssa.Return)
			if !ok {
				continue
			}
			got, fresh := logged, f.entered
			if u.deferred {
				// A deferred call runs at the return, so a variable it
				// captured is read there, and nothing it reads is stale.
				got, fresh = c.walkerErrs(fn, u.values, ret, f.phis, nil), nil
			}
			if c.checkShares(fn, ret, f.phis, u.via, got, fresh) {
				return ret
			}
		}
		for _, s := range f.b.Succs {
			if seen[s] {
				continue
			}
			seen[s] = true
			phis := make(map[*ssa.Phi]ssa.Value, len(f.phis))
			for k, v := range f.phis {
				phis[k] = v
			}
			entered := make(map[*ssa.BasicBlock]bool, len(f.entered)+1)
			for k := range f.entered {
				entered[k] = true
			}
			entered[s] = true
			pred := -1
			for j, p := range s.Preds {
				if p == f.b {
					pred = j
					break
				}
			}
			for _, in := range s.Instrs {
				phi, ok := in.(*ssa.Phi)
				if !ok {
					break
				}
				if pred >= 0 && pred < len(phi.Edges) {
					phis[phi] = phi.Edges[pred]
				}
			}
			stack = append(stack, frame{b: s, phis: phis, entered: entered})
		}
	}
	return nil
}

// checkShares reports whether an error result of ret shares an origin with
// logged. A value defined in a block of fresh was defined again after it was
// logged, and does not count.
func (c *checker) checkShares(fn *ssa.Function, ret *ssa.Return, phis map[*ssa.Phi]ssa.Value, via ssa.Value, logged map[ssa.Value]bool, fresh map[*ssa.BasicBlock]bool) bool {
	if len(logged) == 0 {
		return false
	}
	for _, r := range ret.Results {
		if !typeutil.IsError(r.Type()) {
			continue
		}
		for v := range c.walkerErrs(fn, []ssa.Value{r}, ret, phis, via) {
			if !logged[v] {
				continue
			}
			if in, ok := v.(ssa.Instruction); ok && fresh[in.Block()] {
				continue
			}
			return true
		}
	}
	return false
}

// checkReportable reports whether a diagnostic at pos would be shown: it is in
// a file of this pass that is not generated.
func (c *checker) checkReportable(pos token.Pos) bool {
	if !pos.IsValid() {
		return false
	}
	f, generated := c.checkFile(c.pass.Fset.Position(pos).Filename)
	return f != nil && !generated
}

// checkReport reports the logging instruction, anchored on the statement that
// holds it, so that an ignore directive on the line above a multi-line chain
// covers it.
func (c *checker) checkReport(instr ssa.Instruction, ret *ssa.Return, u sinkUse) {
	pos := c.checkStmt(instr.Pos())
	if c.directives.Ignored(pos) {
		return
	}
	line := c.pass.Fset.Position(ret.Pos()).Line
	msg := "error is logged here and also returned at line %d; log it or return it, not both"
	args := []any{line}
	if u.by != "" {
		msg = "error is logged by %s and also returned at line %d; log it or return it, not both"
		args = []any{u.by, line}
	}
	d := analysis.Diagnostic{Pos: pos, Message: fmt.Sprintf(msg, args...)}
	if ret.Pos().IsValid() {
		d.Related = []analysis.RelatedInformation{{Pos: ret.Pos(), Message: "returned here"}}
	}
	c.pass.Report(d)
}

// checkStmt is the start of the statement enclosing pos.
func (c *checker) checkStmt(pos token.Pos) token.Pos {
	f, _ := c.checkFile(c.pass.Fset.Position(pos).Filename)
	if f == nil {
		return pos
	}
	path, _ := astutil.PathEnclosingInterval(f, pos, pos)
	for _, n := range path {
		if s, ok := n.(ast.Stmt); ok {
			if _, block := s.(*ast.BlockStmt); !block {
				return s.Pos()
			}
		}
	}
	return pos
}
