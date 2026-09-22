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

// MayCarry reports whether a value of type t can carry an error's content: an
// error, an interface that error satisfies, a string that may hold its message,
// or a slice or array of any of these, as a variadic ...any parameter is.
func MayCarry(t types.Type) bool {
	if IsError(t) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Interface:
		return types.Implements(errorType, u)
	case *types.Basic:
		return u.Info()&types.IsString != 0
	case *types.Slice:
		return MayCarry(u.Elem())
	case *types.Array:
		return MayCarry(u.Elem())
	}
	return false
}
