package internal

import (
	"fmt"
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
	c   *checker
	idx *store.Index
	// phis resolves a φ-node to the edge a path took.
	phis map[*ssa.Phi]ssa.Value
	// leaf is a call not traced through: its callee logs what it returns.
	leaf ssa.Value
	// along is the path a forward walk took to the read, or nil.
	along *store.Along
	// expand makes an unresolved φ-node stand for all of its edges.
	expand bool
	seen   map[walkerSeen]bool
	errs   map[walkerOrigin]bool
	inputs map[ssa.Value]bool
	// fields holds the inputs reached only through memory below them, by
	// the paths read there.
	fields map[ssa.Value][][]int
}

// walkerOrigin is an error a trace found: an SSA value, or the contents of
// memory the function did not write, which several loads read alike.
//
//declscope:package
type walkerOrigin struct {
	// v is the value, or the root of the memory.
	v ssa.Value
	// path is the memory's path below v, rendered. It is empty for a
	// value.
	path string
	// clob is the call after which the memory was read, when a call wrote
	// it by a route the index does not follow. Two reads after one such
	// call read the same thing.
	clob ssa.Instruction
}

// walkerSeen keys a visit. The contents of memory depend on where they are
// read, so a read is keyed by the reading instruction and the path too.
type walkerSeen struct {
	v     ssa.Value
	at    ssa.Instruction
	path  string
	whole bool
}

// walkerBook holds the write index of every function traced so far.
//
//declscope:package
type walkerBook struct {
	//declscope:private
	walkerIndexes map[*ssa.Function]*store.Index
}

// walkerIndex indexes the writes in fn, once.
//
//declscope:package
func (c *checker) walkerIndex(fn *ssa.Function) *store.Index {
	if c.walkerIndexes == nil {
		c.walkerIndexes = make(map[*ssa.Function]*store.Index)
	}
	x, ok := c.walkerIndexes[fn]
	if !ok {
		x = store.New(fn, c.calleeWrites)
		c.walkerIndexes[fn] = x
	}
	return x
}

// walkerErrs traces vs, as read by instruction at in fn, and returns the
// error-typed values they are made from. phis resolves the φ-nodes a path has
// chosen and may be nil; leaf, when set, is a call not traced through; along,
// when set, is the path a forward walk took to at, which memory is read
// along.
//
//declscope:package
func (c *checker) walkerErrs(fn *ssa.Function, vs []ssa.Value, at ssa.Instruction, phis map[*ssa.Phi]ssa.Value, leaf ssa.Value, along *store.Along) map[walkerOrigin]bool {
	w := c.newWalker(fn, phis, leaf, false)
	w.along = along
	return w.trace(vs, at).errs
}

// walkerInputs traces vs, as read by instruction at in fn, and returns the
// inputs of fn they are made from as a whole, and the inputs they read memory
// below, by path.
//
//declscope:package
func (c *checker) walkerInputs(fn *ssa.Function, vs []ssa.Value, at ssa.Instruction) (map[ssa.Value]bool, map[ssa.Value][][]int) {
	w := c.newWalker(fn, nil, nil, true).trace(vs, at)
	return w.inputs, w.fields
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
		errs:   make(map[walkerOrigin]bool),
		inputs: make(map[ssa.Value]bool),
		fields: make(map[ssa.Value][][]int),
	}
}

// trace visits each of vs as read by instruction at, and returns the walker.
func (w *walker) trace(vs []ssa.Value, at ssa.Instruction) *walker {
	for _, v := range vs {
		w.value(v, at)
	}
	return w
}

// value visits v as read by instruction at, with everything written into it.
func (w *walker) value(v ssa.Value, at ssa.Instruction) {
	w.visit(v, at, true)
}

// visit visits v as read by instruction at. whole includes what was written
// into the memory v points to; a read of one field of v leaves it out, and
// finds its own writes.
func (w *walker) visit(v ssa.Value, at ssa.Instruction, whole bool) {
	// A boolean or a number made from an error does not carry it: logging
	// err == nil, or errors.Is(err, target), says whether the call failed,
	// not what the error was.
	if v == nil || typeutil.Inert(v.Type()) {
		return
	}
	k := walkerSeen{v: v, whole: whole}
	if store.Variable(v) {
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
		} else if at, root, path, ok := walkerLoaded(v); ok {
			w.read(root, path, at)
		} else {
			w.value(v.Tuple, v)
		}
	case *ssa.Lookup:
		// A string's byte is inert, so what is left is a map element.
		w.mark(v)
		at, root, path, _ := walkerLoaded(v)
		w.read(root, path, at)
	case *ssa.UnOp:
		// Negation, complement and not give numbers and booleans, which
		// are inert.
		switch v.Op {
		case token.MUL:
			w.load(v)
		case token.ARROW:
			w.mark(v)
		}
	case *ssa.BinOp:
		w.value(v.X, v)
		w.value(v.Y, v)
	case *ssa.Alloc:
		w.read(v, nil, at)
	case *ssa.FieldAddr, *ssa.IndexAddr:
		root, path := store.Locate(v)
		w.read(root, path, v.(ssa.Instruction))
	case *ssa.Field, *ssa.Index:
		w.mark(v)
		if ld, root, path, ok := walkerLoaded(v); ok {
			// A field of a map element is the element's: two reads of
			// it are one read only through one lookup.
			if _, load := ld.(*ssa.UnOp); !load && typeutil.IsError(v.Type()) {
				base, rel := walkerPart(v)
				w.origin(base, rel, nil)
			}
			w.read(root, path, ld)
		} else if !walkerFromCall(v) && typeutil.IsError(v.Type()) {
			// A field of a value that is not memory, as a struct received
			// from a channel or asserted out of an interface is, is
			// recorded by where it is in that value: two reads of r.err
			// read the same error. The value is not traced whole. It
			// would match, as an error, with any other field of it.
			base, path := walkerPart(v)
			w.origin(base, path, nil)
		}
	case *ssa.Global, *ssa.MakeSlice, *ssa.MakeMap, *ssa.MakeChan:
	default:
		w.mark(v)
		in, _ := v.(ssa.Instruction)
		for _, op := range walkerOperands(v) {
			w.value(op, in)
		}
	}
	if whole {
		w.stored(v)
	}
}

// origin records memory, or a part of a value, as an error found. A path
// through an element whose index is not a constant is not recorded: two
// reads of it may read two elements, errs[i] and errs[j].
func (w *walker) origin(v ssa.Value, path []int, clob ssa.Instruction) {
	for _, s := range path {
		if s == store.Elem {
			return
		}
	}
	w.errs[walkerOrigin{v: v, path: fmt.Sprint(path), clob: clob}] = true
}

// walkerPart splits a chain of fields and elements into the value it starts
// from and the path below it.
func walkerPart(v ssa.Value) (ssa.Value, []int) {
	var rev []int
	for {
		switch x := v.(type) {
		case *ssa.Field:
			rev = append(rev, x.Field)
			v = x.X
		case *ssa.Index:
			rev = append(rev, store.Elem)
			v = x.X
		default:
			path := make([]int, len(rev))
			for i, s := range rev {
				path[len(rev)-1-i] = s
			}
			return v, path
		}
	}
}

// walkerFromCall reports whether v is a field or an element of a value a call
// returned. The call's summary says what its result carries as a whole, not
// which field holds it, so a field of it is not traced through the call.
func walkerFromCall(v ssa.Value) bool {
	for {
		switch x := v.(type) {
		case *ssa.Field:
			v = x.X
		case *ssa.Index:
			v = x.X
		case *ssa.Call:
			return true
		case *ssa.Extract:
			_, call := x.Tuple.(*ssa.Call)
			return call
		default:
			return false
		}
	}
}

// walkerLoaded finds the memory a field or an element of a loaded value
// comes from: for s.f, where s is *p, it is p's memory with f appended to
// the path. A map element is the map's memory at an element. at is where the
// memory is read: the load, the lookup, or the range step.
func walkerLoaded(v ssa.Value) (at ssa.Instruction, root ssa.Value, path []int, ok bool) {
	var rev []int
	for {
		switch x := v.(type) {
		case *ssa.Field:
			rev = append(rev, x.Field)
			v = x.X
		case *ssa.Index:
			rev = append(rev, store.Elem)
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return nil, nil, nil, false
			}
			root, base := store.Locate(x.X)
			for i := len(rev) - 1; i >= 0; i-- {
				base = append(base, rev[i])
			}
			return x, root, base, true
		case *ssa.Lookup:
			// An element of a map, read where it is looked up. A
			// string's byte has no fields.
			return walkerAt(x, x.X, rev)
		case *ssa.Extract:
			// The value of a map lookup with ok, read where it is looked
			// up, or of a map range, read where the range steps. The
			// iterator of a Next is always a Range.
			if lk, ok := x.Tuple.(*ssa.Lookup); ok && x.Index == 0 {
				return walkerAt(lk, lk.X, rev)
			}
			next, ok := x.Tuple.(*ssa.Next)
			if !ok || next.IsString || x.Index != 2 {
				return nil, nil, nil, false
			}
			return walkerAt(next, next.Iter.(*ssa.Range).X, rev)
		default:
			return nil, nil, nil, false
		}
	}
}

// walkerAt is the element of a map m at the fields rev lists from the leaf
// up, read by instruction at.
func walkerAt(at ssa.Instruction, m ssa.Value, rev []int) (ssa.Instruction, ssa.Value, []int, bool) {
	path := []int{store.Elem}
	for i := len(rev) - 1; i >= 0; i-- {
		path = append(path, rev[i])
	}
	return at, m, path, true
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
	case *ssa.Slice:
		return []ssa.Value{v.X}
	case *ssa.Next:
		return []ssa.Value{v.Iter}
	case *ssa.Range:
		return []ssa.Value{v.X}
	}
	return nil
}

// mark records v when it is an error. v may be nil.
func (w *walker) mark(v ssa.Value) {
	if v != nil && typeutil.IsError(v.Type()) {
		w.errs[walkerOrigin{v: v}] = true
	}
}

// markVar records the variable at p when it holds an error. A variable stands
// for its value when nothing visible was stored in it.
func (w *walker) markVar(p ssa.Value) {
	if ptr, ok := p.Type().Underlying().(*types.Pointer); ok && typeutil.IsError(ptr.Elem()) {
		w.errs[walkerOrigin{v: p}] = true
	}
}

// call visits the inputs that result idx of call may carry.
func (w *walker) call(call *ssa.Call, idx int) {
	if ssa.Value(call) == w.leaf {
		return
	}
	whole, fields := w.c.calleeFlows(call, idx)
	for _, in := range whole {
		w.value(in, call)
	}
	// A result made from memory below an argument reads that memory, as
	// it stands at the call.
	for _, f := range fields {
		if root, base := store.LocateArg(f.Val); root != nil {
			w.read(root, store.Join(base, f.Path), call)
		} else {
			w.value(f.Val, call)
		}
	}
}

// load visits what a load reads.
func (w *walker) load(u *ssa.UnOp) {
	root, path := store.Locate(u.X)
	w.read(root, path, u)
}

// read visits what the memory at root and path holds when instruction at
// reads it: the writes that reach at, and, where no write replaced it, what
// it held when the function was entered.
func (w *walker) read(root ssa.Value, path []int, at ssa.Instruction) {
	// A path through a pointer the memory holds can grow without end, as
	// p = p.next does. Past this depth the read is given up, and matches
	// nothing.
	if len(path) > walkerMaxPath {
		return
	}
	k := walkerSeen{v: root, at: at, path: fmt.Sprint(path)}
	if w.seen[k] {
		return
	}
	w.seen[k] = true

	r := w.idx.Reaching(root, path, at, w.along)
	if ld := walkerLoadOf(at); ld != nil && typeutil.IsError(ld.Type()) {
		for _, cl := range r.Clobbers {
			w.origin(root, path, cl)
		}
	}
	for _, s := range r.Writes {
		if len(s.Path) < len(path) {
			w.project(s.Val, path[len(s.Path):], at)
		} else {
			w.value(s.Val, s.At)
		}
	}
	// A local variable nothing was stored in stands for its value, which
	// is how errors.As fills its target.
	if a, ok := root.(*ssa.Alloc); ok && (r.Unwritten || r.Zero) && len(r.Writes) == 0 && len(path) == 0 {
		w.markVar(a)
	}
	if !r.Unwritten {
		return
	}
	switch x := root.(type) {
	case *ssa.Alloc:
	case *ssa.Global:
		if len(path) == 0 {
			w.markVar(x)
		}
	case *ssa.FreeVar:
		// A closure that assigns to a captured variable reads its own
		// store; only a path without one reads what was captured.
		if len(path) == 0 {
			w.markVar(x)
			w.visit(x, at, false)
			return
		}
		w.input(x, path, at)
	case *ssa.Parameter:
		if len(path) == 0 {
			w.mark(walkerLoadOf(at))
			w.visit(x, at, false)
			return
		}
		w.input(x, path, at)
	default:
		// The memory belongs to the caller, or to whatever made the
		// pointer. Every load of it that no write reaches reads the same
		// thing, so it is recorded by where it is, not by the load.
		if ld := walkerLoadOf(at); ld != nil && typeutil.IsError(ld.Type()) {
			w.origin(root, path, nil)
		}
		// A field of what a call returned is not traced through the
		// call: the call's summary says what its result carries as a
		// whole, not which field holds it.
		if _, made := root.(*ssa.Call); made && len(path) > 0 {
			return
		}
		if _, made := root.(*ssa.Extract); made && len(path) > 0 {
			return
		}
		w.visit(root, at, false)
	}
}

// walkerMaxPath bounds the path of a memory read.
const walkerMaxPath = 8

// input records memory below an input that no write in the function reached:
// the caller's, read at path.
func (w *walker) input(x ssa.Value, path []int, at ssa.Instruction) {
	w.fields[x] = append(w.fields[x], path)
	if ld := walkerLoadOf(at); ld != nil && typeutil.IsError(ld.Type()) {
		w.origin(x, path, nil)
	}
}

// project visits the part at path of v, a whole value that was stored and
// is read by readAt. A value loaded from memory is read at the longer path
// there. Any other value is visited whole.
func (w *walker) project(v ssa.Value, path []int, readAt ssa.Instruction) {
	if path[0] == store.Deref {
		// A pointer was stored. What is read goes through it.
		if _, ok := v.Type().Underlying().(*types.Pointer); ok {
			root, base := store.Locate(v)
			w.read(root, append(base, path[1:]...), readAt)
			return
		}
	}
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
		root, base := store.Locate(u.X)
		w.read(root, append(base, path...), u)
		return
	}
	// An input copied whole into memory, as an array parameter is, is read
	// at the path below the input.
	switch v.(type) {
	case *ssa.Parameter, *ssa.FreeVar:
		w.input(v, path, readAt)
		return
	}
	// A struct a call returned, copied whole into memory, is read by field
	// here. As for a field of the call's value itself, the call is not
	// traced through. A map element looked up with ok is read in the map.
	switch x := v.(type) {
	case *ssa.Call:
		return
	case *ssa.Extract:
		if _, call := x.Tuple.(*ssa.Call); call {
			return
		}
	}
	// A map element, looked up or ranged over, is read in the map at the
	// longer path.
	if elemAt, root, base, ok := walkerLoaded(v); ok {
		// The element is v's: two reads of it are one read only through
		// the lookup or the range step that produced v.
		if ld := walkerLoadOf(readAt); ld != nil && typeutil.IsError(ld.Type()) {
			w.origin(v, path, nil)
		}
		w.read(root, store.Join(base, path), elemAt)
		return
	}
	if walkerFromCall(v) {
		return
	}
	// Any other value, as one received or asserted out of an interface,
	// is recorded by where the part read is in it, so that two reads of
	// the part match. It is not traced whole: it would match, as an error,
	// with any other part of it.
	if ld := walkerLoadOf(readAt); ld != nil && typeutil.IsError(ld.Type()) {
		w.origin(v, path, nil)
	}
}

// walkerLoadOf is the value at loads, when at is a load, for marking what it
// read. It is nil otherwise.
func walkerLoadOf(at ssa.Instruction) ssa.Value {
	if u, ok := at.(*ssa.UnOp); ok && u.Op == token.MUL {
		return u
	}
	return nil
}

// stored visits what was written into the memory below v, in any order:
// through a pointer into it, a map update, a send, or a method that mutates
// it. A variable is read by read instead, which knows the order.
func (w *walker) stored(v ssa.Value) {
	if store.Variable(v) {
		return
	}
	for _, s := range w.idx.Parts(v) {
		w.value(s.Val, s.At)
	}
}
