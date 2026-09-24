// Package typeutil holds the type questions every part of the analysis asks.
package typeutil

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// errorType is the predeclared error interface.
var errorType = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// IsError reports whether a value of type t is an error.
func IsError(t types.Type) bool {
	if t == nil {
		return false
	}
	if _, ok := t.Underlying().(*types.Tuple); ok {
		return false
	}
	return types.Implements(t, errorType)
}

// Func is the declared function behind fn, or nil when fn is a function
// literal or a synthetic wrapper. A wrapper orders its inputs differently from
// the method it wraps, so it has to be read from its body instead.
func Func(fn *ssa.Function) *types.Func {
	if o := fn.Origin(); o != nil {
		fn = o
	}
	if fn.Synthetic != "" && fn.Blocks != nil {
		return nil
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok {
		return nil
	}
	return obj.Origin()
}

// StaticFunc is the declared function a call names statically, or nil.
func StaticFunc(cc *ssa.CallCommon) *types.Func {
	if cc.IsInvoke() {
		return nil
	}
	f, ok := cc.Value.(*ssa.Function)
	if !ok {
		return nil
	}
	return Func(f)
}

// Unbox strips conversions to an interface.
func Unbox(v ssa.Value) ssa.Value {
	for {
		mi, ok := v.(*ssa.MakeInterface)
		if !ok {
			return v
		}
		v = mi.X
	}
}

// TakesError reports whether an error can be passed as a value of type t: t
// is an error, or an interface that error satisfies.
func TakesError(t types.Type) bool {
	if IsError(t) {
		return true
	}
	iface, ok := t.Underlying().(*types.Interface)
	return ok && types.Implements(errorType, iface)
}

// MayCarry reports whether a value of type t can carry an error's content: an
// error, an interface that error satisfies, a string that may hold its message,
// or a slice or array of any of these, as a variadic ...any parameter is.
//
// The element chain is followed in a loop, and a type seen before ends it. A
// type can refer to itself only through a named type, as in type T []T, so
// that is where every cycle closes. An instance of a generic type may be a new
// value each time it is expanded, so the chain is also cut at a depth that no
// written type reaches.
func MayCarry(t types.Type) bool {
	seen := make(map[types.Type]bool)
	for !seen[t] && len(seen) < maxDepth {
		seen[t] = true
		if IsError(t) {
			return true
		}
		switch u := t.Underlying().(type) {
		case *types.Interface:
			return types.Implements(errorType, u)
		case *types.Basic:
			return u.Info()&types.IsString != 0
		case *types.Slice:
			t = u.Elem()
		case *types.Array:
			t = u.Elem()
		default:
			return false
		}
	}
	return false
}

// maxDepth bounds the element chain MayCarry follows.
const maxDepth = 64

// Inert reports whether a value of type t cannot carry an error's content: a
// boolean or a number that is not itself an error, as syscall.Errno is. A
// comparison with an error, or a count of its bytes, says nothing that a
// caller would log again.
func Inert(t types.Type) bool {
	if IsError(t) {
		return false
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsBoolean|types.IsNumeric) != 0
}
