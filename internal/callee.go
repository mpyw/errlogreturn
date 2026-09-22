package internal

import (
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/known"
	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// callee is a resolved call target.
//
//declscope:package
type callee struct {
	// obj is the declared function or method, when there is one. It is nil
	// for a function literal and for a synthetic wrapper, which are read
	// from their bodies instead.
	obj *types.Func
	// inputs are the values passed, numbered the way a summary numbers
	// them: receiver and arguments, then closure bindings.
	inputs []ssa.Value
	// builtin is set for a call to a builtin function.
	builtin *ssa.Builtin
	// fn is the function called, when it is known statically.
	//
	//declscope:private
	fn *ssa.Function
}

// resolveCallee finds what a call calls, and numbers its inputs the way the
// callee's summary does.
//
//declscope:package
func resolveCallee(cc *ssa.CallCommon) callee {
	if cc.IsInvoke() {
		inputs := append([]ssa.Value{cc.Value}, cc.Args...)
		return callee{obj: cc.Method, inputs: inputs}
	}
	switch f := cc.Value.(type) {
	case *ssa.Function:
		return callee{fn: f, obj: typeutil.Func(f), inputs: cc.Args}
	case *ssa.MakeClosure:
		fn, _ := f.Fn.(*ssa.Function)
		inputs := append(append([]ssa.Value{}, cc.Args...), f.Bindings...)
		cl := callee{fn: fn, inputs: inputs}
		if fn != nil {
			cl.obj = typeutil.Func(fn)
		}
		return cl
	case *ssa.Builtin:
		return callee{builtin: f, inputs: cc.Args}
	}
	return callee{inputs: cc.Args}
}

// calleeSummary is the callee's summary, read from its body when the pass has
// one and from a fact otherwise. It is nil when neither exists.
//
//declscope:package
func (c *checker) calleeSummary(cl callee) *summary {
	fn := cl.fn
	if fn != nil && fn.Blocks == nil {
		if o := fn.Origin(); o != nil && o.Blocks != nil {
			fn = o
		}
	}
	if fn != nil && fn.Blocks != nil {
		return c.summary(fn)
	}
	if cl.obj == nil {
		return nil
	}
	var f Fact
	if !c.pass.ImportObjectFact(cl.obj, &f) {
		return nil
	}
	return &summary{flows: f.FlowsTo, logs: f.Logs}
}

// calleeFlows lists the inputs of a call that result idx may carry.
//
//declscope:package
func (c *checker) calleeFlows(call *ssa.Call, idx int) []ssa.Value {
	cl := resolveCallee(call.Common())
	if cl.builtin != nil {
		switch cl.builtin.Name() {
		case "append", "min", "max":
			return cl.inputs
		case "ssa:wrapnilchk":
			return cl.inputs[:1]
		}
		return nil
	}
	if cl.obj != nil {
		if known.Carries(cl.obj) {
			return cl.inputs
		}
		if known.Accessor(cl.obj) {
			return cl.inputs[:1]
		}
	}
	if s := c.calleeSummary(cl); s != nil {
		var out []ssa.Value
		for i, in := range cl.inputs {
			if i < len(s.flows) && idx < 64 && s.flows[i]&(1<<idx) != 0 {
				out = append(out, in)
			}
		}
		return out
	}
	return guessCalleeFlow(cl, call, idx)
}

// guessCalleeFlow is the fallback for a callee nothing is known about: an
// interface method, a function value, or a function without a body. An error
// result is taken to carry the error arguments, which is what a wrapping
// helper does.
func guessCalleeFlow(cl callee, call *ssa.Call, idx int) []ssa.Value {
	t := call.Type()
	if tup, ok := t.(*types.Tuple); ok {
		if idx >= tup.Len() {
			return nil
		}
		t = tup.At(idx).Type()
	}
	if !typeutil.IsError(t) {
		return nil
	}
	var out []ssa.Value
	for _, in := range cl.inputs {
		if calleeTakesError(in) {
			out = append(out, in)
		}
	}
	return out
}

// calleeTakesError reports whether v is an error, including one converted to a
// wider interface on its way into a call.
func calleeTakesError(v ssa.Value) bool {
	return typeutil.IsError(v.Type()) || typeutil.IsError(typeutil.Unbox(v).Type())
}
