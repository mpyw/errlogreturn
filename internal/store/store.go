// Package store indexes the writes in a function, so that a read can find what
// it may read.
//
// A write is filed under the value its address starts from, the root, with
// the path of fields and elements below it. A read of j.id is then told apart
// from a write to j.err, and a write to j.err that comes after another one
// replaces it, as a store to a variable does.
package store

import (
	"go/constant"
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/known"
	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// Elem is the path step of an element, a map entry or a channel's contents.
// Which element is not known, so a write to one does not replace another.
const Elem = -1

// constIndex is the path step of the element at a constant index i, which is
// told apart from the others. Elem matches it too.
func constIndex(i int64) int { return indexBase - int(i) }

// IsIndex reports whether step is a constant index, and which.
func IsIndex(step int) (int, bool) {
	if step <= indexBase {
		return indexBase - step, true
	}
	return 0, false
}

// indexBase is the step of constant index 0. Every step at or below it is a
// constant index.
const indexBase = -3

// maxIndex bounds the constant indexes told apart. A larger one is an Elem.
const maxIndex = 1 << 16

// Deref is the path step through a pointer held in memory: j.inner.err, with
// inner a pointer, is j at inner, Deref, err. A write to j.inner replaces what
// is read through it, since the pointer then points elsewhere.
const Deref = -2

// Written is a value a call may write through, and the path below it that it
// writes. An empty path is everything the value points to.
type Written struct {
	Val  ssa.Value
	Path []int
	// Must reports that the call writes it on every path. A deferred call
	// replaces what is there only then: a deferred Close that sets err when
	// err is nil leaves an error that was there alone.
	Must bool
}

// CallWrites lists what a call may write: the pointers it is handed whose
// memory the callee stores into, or passes on to a call that may. The engine
// answers it from the callee's summary.
type CallWrites func(cc *ssa.CallCommon) []Written

// Index lists the writes in one function.
type Index struct {
	fn *ssa.Function
	// calls answers what a call writes.
	calls CallWrites
	// writes holds every write, by the root of its address.
	writes map[ssa.Value][]Write
	// at holds the writes by the instruction that made them.
	at map[ssa.Instruction][]Write
	// deferred holds the calls deferred in the function. They run at a
	// RunDefers, so that is where they may write.
	deferred []*ssa.Defer
	// escaped holds the local variables whose address was stored in
	// memory. A store through any loaded pointer may write them.
	escaped map[ssa.Value]bool
}

// Write is one value written, the instruction that wrote it, and where.
type Write struct {
	Val ssa.Value
	At  ssa.Instruction
	// Path is the fields and elements below root. It is empty for a store
	// to the whole of root.
	Path []int
	root ssa.Value
}

// New indexes the writes in fn. calls answers what a call writes.
func New(fn *ssa.Function, calls CallWrites) *Index {
	x := &Index{
		fn:      fn,
		calls:   calls,
		writes:  make(map[ssa.Value][]Write),
		at:      make(map[ssa.Instruction][]Write),
		escaped: make(map[ssa.Value]bool),
	}
	add := func(root ssa.Value, path []int, val ssa.Value, at ssa.Instruction) {
		w := Write{Val: val, At: at, root: root, Path: path}
		x.writes[root] = append(x.writes[root], w)
		x.at[at] = append(x.at[at], w)
	}
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			switch in := in.(type) {
			case *ssa.Store:
				root, path := Locate(in.Addr)
				add(root, path, in.Val, in)
				x.escape(in.Val)
			case *ssa.MapUpdate:
				add(in.Map, []int{Elem}, in.Key, in)
				add(in.Map, []int{Elem}, in.Value, in)
				x.escape(in.Value)
			case *ssa.Send:
				add(in.Chan, []int{Elem}, in.X, in)
				x.escape(in.X)
			case *ssa.Defer:
				x.deferred = append(x.deferred, in)
			case *ssa.Call:
				cc := in.Common()
				if obj := typeutil.StaticFunc(cc); obj != nil && known.Mutates(obj) && len(cc.Args) > 0 {
					for _, a := range cc.Args[1:] {
						add(cc.Args[0], []int{Elem}, a, in)
					}
				}
			}
		}
	}
	return x
}

// escape records a local variable whose address v is being stored.
func (x *Index) escape(v ssa.Value) {
	if r, _ := LocateArg(v); r != nil {
		if _, ok := r.(*ssa.Alloc); ok {
			x.escaped[r] = true
		}
	}
}

// Locate splits an address into the value it starts from and the path of
// fields, elements and loaded pointers below it.
func Locate(addr ssa.Value) (ssa.Value, []int) {
	var rev []int
	for {
		switch x := addr.(type) {
		case *ssa.FieldAddr:
			rev = append(rev, x.Field)
			addr = x.X
		case *ssa.IndexAddr:
			rev = append(rev, elemStep(x.Index))
			addr = x.X
		case *ssa.Slice:
			// A slice shares the elements of what it slices. Element i
			// of it is element i of X only when it starts at 0.
			if n := len(rev); n > 0 && x.Low != nil && !storeZero(x.Low) {
				if _, ok := IsIndex(rev[n-1]); ok {
					rev[n-1] = Elem
				}
			}
			addr = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return storeReverse(addr, rev)
			}
			rev = append(rev, Deref)
			addr = x.X
		case *ssa.ChangeType:
			// An instance of a generic function converts its pointer
			// arguments to the type parameter's pointer type.
			addr = x.X
		case *ssa.TypeAssert:
			// A pointer asserted out of an interface is the pointer
			// that was put in. An assertion with ok is a tuple, and an
			// address is never one.
			addr = x.X
		default:
			return storeReverse(addr, rev)
		}
	}
}

// elemStep is the path step of an element at index idx: its constant index
// when there is one, Elem otherwise.
func elemStep(idx ssa.Value) int {
	if k, ok := idx.(*ssa.Const); ok && k.Value != nil && k.Value.Kind() == constant.Int {
		if n, exact := constant.Int64Val(k.Value); exact && n >= 0 && n < maxIndex {
			return constIndex(n)
		}
	}
	return Elem
}

// storeZero reports whether v is the constant 0.
func storeZero(v ssa.Value) bool {
	k, ok := v.(*ssa.Const)
	return ok && k.Value != nil && constant.Sign(k.Value) == 0
}

// storeReverse pairs root with the path steps collected from the leaf up.
func storeReverse(root ssa.Value, rev []int) (ssa.Value, []int) {
	path := make([]int, len(rev))
	for i, s := range rev {
		path[len(rev)-1-i] = s
	}
	return root, path
}

// LocateArg is where a value handed to a call points: Locate for a pointer
// passed as it stands, or through a conversion to an interface. It is nil for
// a value that is not a pointer.
func LocateArg(v ssa.Value) (ssa.Value, []int) {
	v = typeutil.Unbox(v)
	switch v.Type().Underlying().(type) {
	case *types.Pointer, *types.Map, *types.Slice, *types.Chan:
		return Locate(v)
	case *types.Interface:
		// An interface parameter may hold a pointer, which a callee that
		// asserts it writes through.
		switch v.(type) {
		case *ssa.Parameter, *ssa.FreeVar:
			return v, nil
		}
	}
	return nil, nil
}

// Join is the path rel below the memory at base.
func Join(base, rel []int) []int {
	return append(append(make([]int, 0, len(base)+len(rel)), base...), rel...)
}

// Rooted lists every write whose address starts from root, in any order.
func (x *Index) Rooted(root ssa.Value) []Write {
	return x.writes[root]
}

// Variable reports whether p is the address of a whole variable: a local one,
// one captured by a closure, or a package-level one.
func Variable(p ssa.Value) bool {
	switch p.(type) {
	case *ssa.Alloc, *ssa.FreeVar, *ssa.Global:
		return true
	}
	return false
}

// Parts lists what was written into the memory below root, in any order: the
// writes a read of the whole value may see, other than a store to the whole
// of a variable.
func (x *Index) Parts(root ssa.Value) []Write {
	var out []Write
	for _, w := range x.writes[root] {
		if len(w.Path) > 0 || !Variable(root) {
			out = append(out, w)
		}
	}
	return out
}

// Reach is what a read may see.
type Reach struct {
	// Writes are the writes that may be the last before the read, or that
	// wrote into a part of what is read.
	Writes []Write
	// Unwritten reports that some path reaches the read from the
	// function's entry with no write that replaces what is read. On such a
	// path the memory holds what it held when the function was called.
	Unwritten bool
	// Zero reports that some path reaches the read from where the local
	// root is allocated, with no write that replaces what is read.
	Zero bool
	// Clobbers are the calls that may have written what is read, by a
	// route the index does not follow. What the memory holds after one is
	// not known, but two reads after the same one read the same thing.
	Clobbers []ssa.Instruction
}

// Along restricts a read to the path a forward walk took: from the
// instruction at From.Instrs[FromIndex], entering each block of Prev from the
// block it maps to. A read on that path looks back along it, so a write the
// walk passed replaces what was there, and a branch the walk did not take is
// not read. Before From, every path counts again.
type Along struct {
	From      *ssa.BasicBlock
	FromIndex int
	Prev      map[*ssa.BasicBlock]*ssa.BasicBlock
}

// on reports whether instruction at lies on the path after the walk's start.
func (a *Along) on(b *ssa.BasicBlock, index int) bool {
	if a == nil {
		return false
	}
	_, entered := a.Prev[b]
	return entered || (b == a.From && index > a.FromIndex)
}

// Reaching finds what a read of the memory at root and path may see when
// instruction at, an instruction of the indexed function, reads it. along,
// when not nil, is the path a forward walk took to at.
//
// A write to the same place, or to a whole that holds it, replaces what was
// there, and the search stops at it. A write to a part of what is read is
// seen, and the search goes on. A call that may write the memory by a route
// the index cannot see, such as a closure that assigns a captured variable,
// ends the search with nothing: what it wrote is not known.
//
// A read from another function, or from no instruction in particular, sees
// every write, and may see the memory unwritten.
func (x *Index) Reaching(root ssa.Value, path []int, at ssa.Instruction, along *Along) Reach {
	all := x.writes[root]
	// A package-level variable the function never assigns is read as it
	// stands. It is usually a sentinel error, and any call could assign it,
	// so counting calls would lose every log of one.
	if _, ok := root.(*ssa.Global); ok && len(all) == 0 {
		return Reach{Unwritten: true}
	}
	var r Reach
	seen := make(map[*ssa.BasicBlock]bool)
	var scan func(b *ssa.BasicBlock, end int, restricted bool)
	scan = func(b *ssa.BasicBlock, end int, restricted bool) {
		for i := end - 1; i >= 0; i-- {
			in := b.Instrs[i]
			// Where a local is allocated it holds its zero value, and
			// no write before that reaches it: a loop allocating it
			// again reads a new one.
			if a, ok := in.(*ssa.Alloc); ok && ssa.Value(a) == root {
				r.Zero = true
				return
			}
			stop := false
			for _, w := range x.at[in] {
				if w.root != root || !storeOverlap(w.Path, path) {
					continue
				}
				r.Writes = append(r.Writes, w)
				if storeReplaces(w.Path, path) {
					stop = true
				}
			}
			if stop {
				return
			}
			if x.clobbers(in, root, path) {
				r.Clobbers = append(r.Clobbers, in)
				return
			}
		}
		if len(b.Preds) == 0 {
			r.Unwritten = true
		}
		// On the walk's path, a block was entered from one block, which
		// leads back to the walk's start. At the start, every path counts
		// again.
		if restricted && b != along.From {
			p := along.Prev[b]
			if p != nil && !seen[p] {
				seen[p] = true
				scan(p, len(p.Instrs), true)
			}
			return
		}
		for _, p := range b.Preds {
			if !seen[p] {
				seen[p] = true
				scan(p, len(p.Instrs), false)
			}
		}
	}
	b := at.Block()
	end := len(b.Instrs)
	for i, in := range b.Instrs {
		if in == at {
			end = i
			break
		}
	}
	scan(b, end, along.on(b, end))
	return r
}

// clobbers reports whether in may write the memory at root and path by a
// route the index does not record: a call that writes through a pointer it
// is handed, as fill(&err) does. A deferred call writes at the RunDefers.
//
// A package-level variable the function assigns may be assigned by any call.
// The libraries described in known are taken to write nothing: errors.As
// fills its target, and that value is what the target then stands for.
func (x *Index) clobbers(in ssa.Instruction, root ssa.Value, path []int) bool {
	if _, ok := in.(*ssa.RunDefers); ok {
		for _, d := range x.deferred {
			if x.callClobbers(d.Common(), root, path, true) {
				return true
			}
		}
		return false
	}
	// A local variable whose address was stored in memory may be reached
	// by a store through a pointer loaded from memory, or from anything
	// but a variable of this function.
	if st, ok := in.(*ssa.Store); ok && x.escaped[root] {
		if r, p := Locate(st.Addr); r != root && (storeHasDeref(p) || !Variable(r)) {
			return true
		}
	}
	ci, ok := in.(ssa.CallInstruction)
	if !ok {
		return false
	}
	if _, ok := in.(*ssa.Defer); ok {
		return false
	}
	return x.callClobbers(ci.Common(), root, path, false)
}

// storeHasDeref reports whether a path goes through a loaded pointer.
func storeHasDeref(p []int) bool {
	return slices.Contains(p, Deref)
}

// callClobbers reports whether the call may write the memory at root and
// path. A deferred call counts only for what it writes on every path.
func (x *Index) callClobbers(cc *ssa.CallCommon, root ssa.Value, path []int, deferred bool) bool {
	if obj := typeutil.StaticFunc(cc); obj != nil && known.Package(obj) {
		return false
	}
	if _, ok := root.(*ssa.Global); ok {
		return true
	}
	for _, w := range x.calls(cc) {
		if deferred && !w.Must {
			continue
		}
		r, base := LocateArg(w.Val)
		if r == root && storeOverlap(Join(base, w.Path), path) {
			return true
		}
		// A call that writes through a pointer held in memory may reach a
		// local variable whose address was stored there.
		if !deferred && x.escaped[root] && r != nil && r != root && storeHasDeref(Join(base, w.Path)) {
			return true
		}
	}
	return false
}

// storeOverlap reports whether a write at path w may touch a read at path r:
// one path holds the other, where an element step matches any element.
func storeOverlap(w, r []int) bool {
	for i := range min(len(w), len(r)) {
		if w[i] == r[i] {
			continue
		}
		_, wi := IsIndex(w[i])
		_, ri := IsIndex(r[i])
		if (w[i] == Elem && (ri || r[i] == Elem)) || (r[i] == Elem && wi) {
			continue
		}
		return false
	}
	return true
}

// storeReplaces reports whether a write at path w replaces everything a read
// at path r sees: w is r, or a whole that holds it, and names no element,
// since a write to one element does not replace another. A write to a
// pointer replaces what is read through it.
func storeReplaces(w, r []int) bool {
	if len(w) > len(r) {
		return false
	}
	for i, s := range w {
		if s == Elem || s != r[i] {
			return false
		}
	}
	return true
}
