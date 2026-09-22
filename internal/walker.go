package internal

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/store"
	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// walker traces values back to what they were made from, within one function.
//
// It runs in one of two modes. Collecting errors, it records every error-typed
// value on the way, so that a logged value and a returned value can be compared
// by what they share. A φ-node the path has not resolved is then compared by
// identity: it may be any of its edges, and guessing which would match an error
// that was never logged. Collecting inputs, it records the parameters and free
// variables reached, which is what a summary is built from; there every edge
// of a φ-node counts, since a result carries whatever it may be.
type walker struct {
	//declscope:private
	c *checker
	//declscope:private
	idx *store.Index
	// phis resolves a φ-node to the edge a path took.
	//
	//declscope:private
	phis map[*ssa.Phi]ssa.Value
	// leaf is a call not traced through: its callee logs what it returns.
	//
	//declscope:private
	leaf ssa.Value
	// expand makes an unresolved φ-node stand for all of its edges.
	//
	//declscope:private
	expand bool
	//declscope:private
	seen map[walkerSeen]bool
	//declscope:private
	errs map[ssa.Value]bool
	//declscope:private
	inputs map[ssa.Value]bool
}

// walkerSeen keys a visit. The contents of a variable depend on where they are
// read, so a local variable is keyed by the reading instruction too.
type walkerSeen struct {
	v  ssa.Value
	at ssa.Instruction
}

// walkerBook holds the write index of every function traced so far.
//
//declscope:package
type walkerBook struct {
	//declscope:private
	walkerIndexes map[*ssa.Function]*store.Index
}

// walkerIndex indexes the writes in fn, once.
func (c *checker) walkerIndex(fn *ssa.Function) *store.Index {
	if c.walkerIndexes == nil {
		c.walkerIndexes = make(map[*ssa.Function]*store.Index)
	}
	x, ok := c.walkerIndexes[fn]
	if !ok {
		x = store.New(fn)
		c.walkerIndexes[fn] = x
	}
	return x
}

// walkerErrs traces vs, as read by instruction at in fn, and returns the
// error-typed values they are made from. phis resolves the φ-nodes a path has
// chosen and may be nil; leaf, when set, is a call not traced through.
//
//declscope:package
func (c *checker) walkerErrs(fn *ssa.Function, vs []ssa.Value, at ssa.Instruction, phis map[*ssa.Phi]ssa.Value, leaf ssa.Value) map[ssa.Value]bool {
	return c.newWalker(fn, phis, leaf, false).trace(vs, at).errs
}

// walkerInputs traces vs, as read by instruction at in fn, and returns the
// inputs of fn they are made from.
//
//declscope:package
func (c *checker) walkerInputs(fn *ssa.Function, vs []ssa.Value, at ssa.Instruction) map[ssa.Value]bool {
	return c.newWalker(fn, nil, nil, true).trace(vs, at).inputs
}

// newWalker starts a trace in fn. phis and leaf may be nil.
func (c *checker) newWalker(fn *ssa.Function, phis map[*ssa.Phi]ssa.Value, leaf ssa.Value, expand bool) *walker {
	return &walker{
		c:      c,
		idx:    c.walkerIndex(fn),
		phis:   phis,
		leaf:   leaf,
		expand: expand,
		seen:   make(map[walkerSeen]bool),
		errs:   make(map[ssa.Value]bool),
		inputs: make(map[ssa.Value]bool),
	}
}

// trace visits each of vs as read by instruction at, and returns the walker.
func (w *walker) trace(vs []ssa.Value, at ssa.Instruction) *walker {
	for _, v := range vs {
		w.value(v, at)
	}
	return w
}

// value visits v as read by instruction at.
func (w *walker) value(v ssa.Value, at ssa.Instruction) {
	if v == nil {
		return
	}
	k := walkerSeen{v: v}
	if _, ok := v.(*ssa.Alloc); ok {
		k.at = at
	}
	if w.seen[k] {
		return
	}
	w.seen[k] = true

	switch v := v.(type) {
	case *ssa.Const, *ssa.Function, *ssa.Builtin:
		return
	case *ssa.Parameter, *ssa.FreeVar:
		w.inputs[v] = true
		w.mark(v)
	case *ssa.Phi:
		switch e, ok := w.phis[v]; {
		case ok:
			w.value(e, v)
		case w.expand:
			for _, e := range v.Edges {
				w.value(e, v)
			}
		default:
			w.mark(v)
		}
	case *ssa.Call:
		w.mark(v)
		w.call(v, 0)
	case *ssa.Extract:
		w.mark(v)
		if call, ok := v.Tuple.(*ssa.Call); ok {
			w.call(call, v.Index)
		} else {
			w.value(v.Tuple, v)
		}
	case *ssa.UnOp:
		switch v.Op {
		case token.MUL:
			w.load(v)
		case token.ARROW:
			w.mark(v)
		default:
			w.value(v.X, v)
		}
	case *ssa.BinOp:
		w.value(v.X, v)
		w.value(v.Y, v)
	case *ssa.Alloc:
		w.contents(v, at)
	case *ssa.FieldAddr:
		w.part(v.X, v)
	case *ssa.IndexAddr:
		w.part(v.X, v)
	case *ssa.Global, *ssa.MakeSlice, *ssa.MakeMap, *ssa.MakeChan:
	default:
		w.mark(v)
		in, _ := v.(ssa.Instruction)
		for _, op := range walkerOperands(v) {
			w.value(op, in)
		}
	}
	w.stored(v)
}

// walkerOperands are the values a derived value is computed from, for the
// kinds that carry an operand through unchanged: conversions, assertions,
// selections and slicing.
func walkerOperands(v ssa.Value) []ssa.Value {
	switch v := v.(type) {
	case *ssa.MakeInterface:
		return []ssa.Value{v.X}
	case *ssa.ChangeInterface:
		return []ssa.Value{v.X}
	case *ssa.ChangeType:
		return []ssa.Value{v.X}
	case *ssa.Convert:
		return []ssa.Value{v.X}
	case *ssa.MultiConvert:
		return []ssa.Value{v.X}
	case *ssa.SliceToArrayPointer:
		return []ssa.Value{v.X}
	case *ssa.TypeAssert:
		return []ssa.Value{v.X}
	case *ssa.Field:
		return []ssa.Value{v.X}
	case *ssa.Index:
		return []ssa.Value{v.X}
	case *ssa.Lookup:
		return []ssa.Value{v.X}
	case *ssa.Slice:
		return []ssa.Value{v.X}
	case *ssa.Next:
		return []ssa.Value{v.Iter}
	case *ssa.Range:
		return []ssa.Value{v.X}
	}
	return nil
}

// mark records v when it is an error.
func (w *walker) mark(v ssa.Value) {
	if typeutil.IsError(v.Type()) {
		w.errs[v] = true
	}
}

// markVar records the variable at p when it holds an error. A variable stands
// for its value when nothing visible was stored in it.
func (w *walker) markVar(p ssa.Value) {
	if ptr, ok := p.Type().Underlying().(*types.Pointer); ok && typeutil.IsError(ptr.Elem()) {
		w.errs[p] = true
	}
}

// call visits the inputs that result idx of call may carry.
func (w *walker) call(call *ssa.Call, idx int) {
	if ssa.Value(call) == w.leaf {
		return
	}
	for _, in := range w.c.calleeFlows(call, idx) {
		w.value(in, call)
	}
}

// load visits what a load reads.
func (w *walker) load(u *ssa.UnOp) {
	switch x := u.X.(type) {
	case *ssa.Global:
		w.markVar(x)
		w.stored(x)
	case *ssa.Alloc:
		w.contents(x, u)
	case *ssa.FreeVar:
		// A closure that assigns to a captured variable reads its own
		// store; only a path without one reads what was captured.
		stores, unwritten := w.idx.Reaching(x, u)
		for _, s := range stores {
			w.value(s.Val, s)
		}
		if unwritten {
			w.markVar(x)
			w.value(x, u)
		}
		w.stored(x)
	default:
		w.mark(u)
		w.value(u.X, u)
	}
}

// contents visits what the local variable a holds when instruction at reads
// it: the stores that reach at, and anything stored into a part of it.
func (w *walker) contents(a *ssa.Alloc, at ssa.Instruction) {
	direct, _ := w.idx.Reaching(a, at)
	for _, s := range direct {
		w.value(s.Val, s)
	}
	if len(direct) == 0 && len(w.idx.Parts(a)) == 0 {
		w.markVar(a)
	}
	w.stored(a)
}

// part visits a pointer into base: a field or an element.
func (w *walker) part(base ssa.Value, at ssa.Instruction) {
	if a, ok := base.(*ssa.Alloc); ok {
		w.contents(a, at)
		return
	}
	w.value(base, at)
}

// stored visits what was written into v other than by a whole store: through
// a pointer into it, a map update, a send, or a method that mutates it.
func (w *walker) stored(v ssa.Value) {
	for _, s := range w.idx.Parts(v) {
		w.value(s.Val, s.At)
	}
}
