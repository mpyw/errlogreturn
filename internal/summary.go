package internal

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// summary is what a caller needs to know about a function. Inputs are
// numbered as the function's parameters, receiver first, followed by its free
// variables.
//
//declscope:package
type summary struct {
	// flows[i] is a bit set of the results input i may carry into.
	flows []uint64
	// logs[i] reports that input i is logged on every path that returns.
	logs []bool
}

// summaryBook holds every summary computed in the pass.
//
//declscope:package
type summaryBook struct {
	//declscope:private
	summaries map[*ssa.Function]*summary
}

// summary reduces fn to what a caller needs, computing it on first use.
//
// The entry is stored before it is filled, so a recursive call reads a
// summary that says nothing yet. That leans toward silence: a recursion can
// only make a function carry or log less than it does.
//
//declscope:package
func (c *checker) summary(fn *ssa.Function) *summary {
	if c.summaries == nil {
		c.summaries = make(map[*ssa.Function]*summary)
	}
	if s, ok := c.summaries[fn]; ok {
		return s
	}
	ins := summaryInputs(fn)
	s := &summary{flows: make([]uint64, len(ins)), logs: make([]bool, len(ins))}
	c.summaries[fn] = s

	for _, b := range fn.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for j, r := range ret.Results {
			if j >= 64 {
				break
			}
			got := c.walkerInputs(fn, []ssa.Value{r}, ret)
			for i, in := range ins {
				if got[in] {
					s.flows[i] |= 1 << j
				}
			}
		}
	}
	copy(s.logs, c.summaryLogs(fn, ins))
	return s
}

// summaryInputs numbers a function's inputs: parameters, receiver first, then
// free variables.
func summaryInputs(fn *ssa.Function) []ssa.Value {
	ins := make([]ssa.Value, 0, len(fn.Params)+len(fn.FreeVars))
	for _, p := range fn.Params {
		ins = append(ins, p)
	}
	for _, fv := range fn.FreeVars {
		ins = append(ins, fv)
	}
	return ins
}

// summaryLogs finds the inputs that fn logs on every path that returns.
//
// It is a forward must-analysis: a block's set is the intersection over its
// predecessors, joined with what the block itself logs. An edge on which an
// input is known to be nil satisfies that input, since a nil error is not a
// failure to log. A function with no return logs nothing.
func (c *checker) summaryLogs(fn *ssa.Function, ins []ssa.Value) []bool {
	n := len(ins)
	if n == 0 || len(fn.Blocks) == 0 {
		return nil
	}
	// Only an input that can carry an error is said to be logged. A log
	// call logs its receiver too, and a helper handed a logger would
	// otherwise be said to log the logger.
	pos := make(map[ssa.Value]int, n)
	for i, in := range ins {
		if typeutil.MayCarry(in.Type()) || summaryCapturesCarrier(in) {
			pos[in] = i
		}
	}

	gen := make([][]bool, len(fn.Blocks))
	for _, b := range fn.Blocks {
		gen[b.Index] = make([]bool, n)
		for _, instr := range b.Instrs {
			u, ok := c.sinkAt(instr)
			if !ok {
				continue
			}
			for in := range c.walkerInputs(fn, u.values, instr) {
				if i, ok := pos[in]; ok {
					gen[b.Index][i] = true
				}
			}
		}
	}

	out := make([][]bool, len(fn.Blocks))
	for i := range out {
		out[i] = summaryFull(n)
	}
	for changed := true; changed; {
		changed = false
		for _, b := range fn.Blocks {
			in := make([]bool, n)
			if len(b.Preds) > 0 {
				in = summaryFull(n)
				for _, p := range b.Preds {
					edge := append([]bool(nil), out[p.Index]...)
					if i, ok := summaryNilEdge(p, b, pos); ok {
						edge[i] = true
					}
					for k := range in {
						in[k] = in[k] && edge[k]
					}
				}
			}
			for k := range in {
				in[k] = in[k] || gen[b.Index][k]
			}
			if !summarySame(in, out[b.Index]) {
				out[b.Index] = in
				changed = true
			}
		}
	}

	logs := summaryFull(n)
	returns := false
	for _, b := range fn.Blocks {
		if _, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); !ok {
			continue
		}
		returns = true
		for k := range logs {
			logs[k] = logs[k] && out[b.Index][k]
		}
	}
	if !returns {
		return nil
	}
	return logs
}

// summaryNilEdge reports the input that is known to be nil on the edge from p to
// b, when p branches on comparing it with nil.
func summaryNilEdge(p, b *ssa.BasicBlock, pos map[ssa.Value]int) (int, bool) {
	iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
	if !ok {
		return 0, false
	}
	cmp, ok := iff.Cond.(*ssa.BinOp)
	if !ok || (cmp.Op != token.EQL && cmp.Op != token.NEQ) {
		return 0, false
	}
	x := cmp.X
	if summaryIsNil(x) {
		x = cmp.Y
	} else if !summaryIsNil(cmp.Y) {
		return 0, false
	}
	nilSide := p.Succs[0]
	if cmp.Op == token.NEQ {
		nilSide = p.Succs[1]
	}
	if nilSide != b {
		return 0, false
	}
	i, ok := pos[summaryInputBehind(x)]
	return i, ok
}

// summaryInputBehind strips conversions and a load through a free variable, to find
// the input a compared value is.
func summaryInputBehind(v ssa.Value) ssa.Value {
	for {
		switch x := v.(type) {
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeInterface:
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return v
			}
			if _, ok := x.X.(*ssa.FreeVar); !ok {
				return v
			}
			v = x.X
		default:
			return v
		}
	}
}

// summaryCapturesCarrier reports whether in is a free variable capturing a
// variable that can carry an error. A captured variable is passed by address.
func summaryCapturesCarrier(in ssa.Value) bool {
	fv, ok := in.(*ssa.FreeVar)
	if !ok {
		return false
	}
	ptr, ok := fv.Type().Underlying().(*types.Pointer)
	return ok && typeutil.MayCarry(ptr.Elem())
}

func summaryIsNil(v ssa.Value) bool {
	c, ok := v.(*ssa.Const)
	return ok && c.IsNil()
}

func summaryFull(n int) []bool {
	s := make([]bool, n)
	for i := range s {
		s[i] = true
	}
	return s
}

func summarySame(a, b []bool) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
