package internal

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/store"
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
	// must[i] reports that the function assigns the whole of what input i
	// points to on every path that returns, as a closure that sets a
	// captured err does.
	must []bool
	// fields lists the results made from memory below an input, where the
	// input itself does not flow: a getter's result is one field of its
	// receiver.
	fields []FactFieldFlow
	// writes[i] lists the paths below input i that the function may write,
	// itself or through a call it hands the pointer to. An empty path is
	// everything input i points to.
	writes [][][]int
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
	s := &summary{flows: make([]uint64, len(ins)), logs: make([]bool, len(ins)), writes: make([][][]int, len(ins))}
	// A recursive call reads the summary before it is filled. It is taken
	// to write every pointer it is handed, which leans toward silence: a
	// variable a call may write matches nothing afterwards.
	for i, in := range ins {
		if _, iface := in.Type().Underlying().(*types.Interface); summaryPointer(in) && !iface {
			s.writes[i] = [][]int{{}}
		}
	}
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
			got, fields := c.walkerInputs(fn, []ssa.Value{r}, ret)
			for i, in := range ins {
				switch {
				case got[in]:
					s.flows[i] |= 1 << j
				case len(fields[in]) > summaryMaxPaths:
					s.flows[i] |= 1 << j
				default:
					for _, p := range fields[in] {
						s.fields = summaryAddField(s.fields, FactFieldFlow{In: i, Out: j, Path: p})
					}
				}
			}
		}
	}
	copy(s.logs, c.summaryLogs(fn, ins))
	copy(s.writes, c.summaryWrites(fn, ins))
	s.must = summaryMust(fn, ins)
	return s
}

// summaryMust finds the inputs fn stores to as a whole on every path that
// returns: a store to *p in a block that dominates every return, before the
// return when it is the same block.
func summaryMust(fn *ssa.Function, ins []ssa.Value) []bool {
	out := make([]bool, len(ins))
	var rets []*ssa.BasicBlock
	for _, b := range fn.Blocks {
		if _, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
			rets = append(rets, b)
		}
	}
	if len(rets) == 0 {
		return out
	}
	for i, in := range ins {
		for _, r := range *in.Referrers() {
			st, ok := r.(*ssa.Store)
			if !ok || st.Addr != in {
				continue
			}
			all := true
			for _, rb := range rets {
				if !st.Block().Dominates(rb) {
					all = false
					break
				}
			}
			if all {
				out[i] = true
			}
		}
	}
	return out
}

// summaryWrites finds what fn may write below each input: the paths it stores
// into, the paths a call it hands the input to writes, and everything below
// an input passed where the analysis loses it, such as stored in memory.
func (c *checker) summaryWrites(fn *ssa.Function, ins []ssa.Value) [][][]int {
	out := make([][][]int, len(ins))
	pos := make(map[ssa.Value]int, len(ins))
	for i, in := range ins {
		if summaryPointer(in) {
			pos[in] = i
		}
	}
	if len(pos) == 0 {
		return out
	}
	// A local variable an input is copied into, as a closure's capture is,
	// is the input under another name. A write through it writes the
	// input, and a store to the whole variable only replaces the copy.
	alias := make(map[ssa.Value]int)
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			if st, ok := in.(*ssa.Store); ok {
				if a, ok := st.Addr.(*ssa.Alloc); ok {
					if i, ok := pos[st.Val]; ok {
						alias[a] = i
					}
				}
			}
		}
	}
	add := func(root ssa.Value, path []int) {
		if i, ok := pos[root]; ok {
			out[i] = summaryAddPath(out[i], path)
		} else if i, ok := alias[root]; ok && len(path) > 0 && path[0] == store.Deref {
			out[i] = summaryAddPath(out[i], path[1:])
		}
	}
	idx := c.walkerIndex(fn)
	for _, m := range []map[ssa.Value]int{pos, alias} {
		for root := range m {
			for _, w := range idx.Rooted(root) {
				add(root, w.Path)
			}
		}
	}
	hit := func(v ssa.Value, rel []int) {
		if r, base := store.LocateArg(v); r != nil {
			add(r, store.Join(base, rel))
		}
	}
	// An interface is not a pointer the function can lose by storing it:
	// only a callee that asserts it can write through it. Storing an error
	// in a struct writes nothing.
	lose := func(v ssa.Value) {
		if _, iface := typeutil.Unbox(v).Type().Underlying().(*types.Interface); !iface {
			hit(v, nil)
		}
	}
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			switch in := in.(type) {
			case ssa.CallInstruction:
				for _, w := range c.calleeWrites(in.Common()) {
					hit(w.Val, w.Path)
				}
			case *ssa.Store:
				if _, a := alias[in.Addr]; !a {
					lose(in.Val)
				}
			case *ssa.MakeClosure:
				// A closure writes what its own summary says it does.
				for _, w := range c.calleeClosureWrites(in) {
					hit(w.Val, w.Path)
				}
			case *ssa.Send:
				lose(in.X)
			case *ssa.MapUpdate:
				lose(in.Value)
			case *ssa.MakeInterface:
				// A pointer converted to an interface may be written by
				// whoever receives it. Unboxed arguments are judged by
				// the call above; any other use is lost.
				if !summaryOnlyArgument(in) {
					lose(in.X)
				}
			}
		}
	}
	return out
}

// summaryAddField adds ff to fields, unless a flow from the same input to the
// same result already holds its path. The function's returns each add their
// own, and a getter with four returns would otherwise list one flow four
// times.
func summaryAddField(fields []FactFieldFlow, ff FactFieldFlow) []FactFieldFlow {
	for _, f := range fields {
		if f.In == ff.In && f.Out == ff.Out && summaryPrefix(f.Path, ff.Path) {
			return fields
		}
	}
	kept := fields[:0:0]
	for _, f := range fields {
		if f.In != ff.In || f.Out != ff.Out || !summaryPrefix(ff.Path, f.Path) {
			kept = append(kept, f)
		}
	}
	return append(kept, FactFieldFlow{In: ff.In, Out: ff.Out, Path: append([]int{}, ff.Path...)})
}

// summaryMaxPaths bounds the paths kept for one input. Past it, the input is
// taken to be written everywhere.
const summaryMaxPaths = 8

// summaryAddPath adds path to paths, unless a path already there holds it. The
// empty path holds every other one.
func summaryAddPath(paths [][]int, path []int) [][]int {
	for _, p := range paths {
		if summaryPrefix(p, path) {
			return paths
		}
	}
	kept := paths[:0:0]
	for _, p := range paths {
		if !summaryPrefix(path, p) {
			kept = append(kept, p)
		}
	}
	if len(kept) >= summaryMaxPaths {
		return [][]int{{}}
	}
	return append(kept, append([]int{}, path...))
}

// summaryPrefix reports whether p is a prefix of q.
func summaryPrefix(p, q []int) bool {
	if len(p) > len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// summaryOnlyArgument reports whether every use of mi is as an argument of a
// call, where calleeWrites judges it, or as a result, which hands it to the
// caller.
func summaryOnlyArgument(mi *ssa.MakeInterface) bool {
	for _, r := range *mi.Referrers() {
		switch r.(type) {
		case ssa.CallInstruction, *ssa.Return:
		default:
			return false
		}
	}
	return true
}

// summaryPointer reports whether a value of in's type refers to memory a
// callee could write: a pointer, a map, a slice, a channel, or an interface,
// which may hold a pointer.
func summaryPointer(in ssa.Value) bool {
	switch in.Type().Underlying().(type) {
	case *types.Pointer, *types.Map, *types.Slice, *types.Chan, *types.Interface:
		return true
	}
	return false
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
			got, _ := c.walkerInputs(fn, u.values, instr)
			for in := range got {
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
		case *ssa.ChangeType:
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
