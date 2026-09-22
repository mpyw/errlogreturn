// Package store indexes the writes in a function, so that a read can find what
// it may read.
package store

import (
	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/known"
	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// Index lists the writes in one function.
type Index struct {
	// whole holds the stores to a whole variable: a local one, or one a
	// closure captured.
	whole map[ssa.Value][]*ssa.Store
	// parts holds everything else written into a value: a store through a
	// pointer into it, a map update, a send, a receiver mutation.
	parts map[ssa.Value][]Write
}

// Write is one value written, and the instruction that wrote it.
type Write struct {
	Val ssa.Value
	At  ssa.Instruction
}

// New indexes the writes in fn. fn may be nil, which indexes nothing.
func New(fn *ssa.Function) *Index {
	x := &Index{
		whole: make(map[ssa.Value][]*ssa.Store),
		parts: make(map[ssa.Value][]Write),
	}
	if fn == nil {
		return x
	}
	add := func(base, val ssa.Value, at ssa.Instruction) {
		x.parts[base] = append(x.parts[base], Write{Val: val, At: at})
	}
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			switch in := in.(type) {
			case *ssa.Store:
				if Variable(in.Addr) {
					x.whole[in.Addr] = append(x.whole[in.Addr], in)
				} else {
					add(base(in.Addr), in.Val, in)
				}
			case *ssa.MapUpdate:
				add(in.Map, in.Key, in)
				add(in.Map, in.Value, in)
			case *ssa.Send:
				add(in.Chan, in.X, in)
			case *ssa.Call:
				cc := in.Common()
				if obj := typeutil.StaticFunc(cc); obj != nil && known.Mutates(obj) && len(cc.Args) > 0 {
					for _, a := range cc.Args[1:] {
						add(cc.Args[0], a, in)
					}
				}
			}
		}
	}
	return x
}

// Variable reports whether p is the address of a whole variable: a local one,
// or one captured by a closure.
func Variable(p ssa.Value) bool {
	switch p.(type) {
	case *ssa.Alloc, *ssa.FreeVar:
		return true
	}
	return false
}

// Parts lists what was written into v other than by a store to the whole of
// a local variable.
func (x *Index) Parts(v ssa.Value) []Write {
	return x.parts[v]
}

// Reaching finds the stores to the variable v that may be the last before
// instruction at, and reports whether some path reaches at from the function's
// entry without storing to v. On such a path a local variable holds its zero
// value and a captured one holds what it held when the closure was called.
//
// A read from another function, or from no instruction in particular, sees
// every store, and may see the variable unwritten.
func (x *Index) Reaching(v ssa.Value, at ssa.Instruction) ([]*ssa.Store, bool) {
	stores := x.whole[v]
	if len(stores) == 0 {
		return nil, true
	}
	if at == nil || at.Block() == nil || at.Parent() != stores[0].Parent() {
		return stores, true
	}
	var found []*ssa.Store
	fromEntry := false
	seen := make(map[*ssa.BasicBlock]bool)
	var scan func(b *ssa.BasicBlock, end int)
	scan = func(b *ssa.BasicBlock, end int) {
		for i := end - 1; i >= 0; i-- {
			if s, ok := b.Instrs[i].(*ssa.Store); ok && s.Addr == v {
				found = append(found, s)
				return
			}
		}
		if len(b.Preds) == 0 {
			fromEntry = true
		}
		for _, p := range b.Preds {
			if !seen[p] {
				seen[p] = true
				scan(p, len(p.Instrs))
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
	scan(b, end)
	return found, fromEntry
}

// base strips field and element selections from a pointer.
func base(p ssa.Value) ssa.Value {
	for {
		switch x := p.(type) {
		case *ssa.FieldAddr:
			p = x.X
		case *ssa.IndexAddr:
			p = x.X
		default:
			return p
		}
	}
}
