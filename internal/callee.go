package internal

import (
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/known"
	"github.com/mpyw/errlogreturn/internal/store"
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

// calleeDeclared is the declared method a call reaches, for naming it. It is
// obj, or for a method value or a method expression, the method that the
// synthetic wrapper calls. It is nil for a function literal.
//
//declscope:package
func (cl callee) calleeDeclared() *types.Func {
	if cl.obj != nil {
		return cl.obj
	}
	if cl.fn != nil && cl.fn.Synthetic != "" {
		if obj, ok := cl.fn.Object().(*types.Func); ok {
			return obj.Origin()
		}
	}
	return nil
}

// calleeWrites lists what a call may write, for the write index. A callee
// with a summary writes what the summary says. A declared function with
// neither a body nor a fact writes nothing: a fact is exported whenever a
// function writes, so no fact means none. An interface method and a function
// value are unknown, and write everything below every pointer they are
// handed. A closure handed to any call writes what its summary says, since
// the call may run it.
//
//declscope:package
func (c *checker) calleeWrites(cc *ssa.CallCommon) []store.Written {
	cl := resolveCallee(cc)
	if cl.builtin != nil {
		switch cl.builtin.Name() {
		case "copy", "clear", "delete":
			if len(cl.inputs) > 0 {
				return []store.Written{{Val: cl.inputs[0]}}
			}
		}
		return nil
	}
	if cl.obj != nil && known.Package(cl.obj) {
		return nil
	}
	var out []store.Written
	for _, v := range cl.inputs {
		if mc, ok := v.(*ssa.MakeClosure); ok {
			out = append(out, c.calleeClosureWrites(mc)...)
		}
	}
	if !cc.IsInvoke() {
		if s := c.calleeSummary(cl); s != nil {
			for i, in := range cl.inputs {
				if i < len(s.writes) {
					for _, p := range s.writes[i] {
						must := len(p) == 0 && i < len(s.must) && s.must[i]
						out = append(out, store.Written{Val: in, Path: p, Must: must})
					}
				}
			}
			return out
		}
		if cl.obj != nil {
			return out
		}
	}
	for i, in := range cl.inputs {
		// The receiver of an interface method is the interface value
		// itself, not a pointer the caller handed over.
		if i == 0 && cc.IsInvoke() {
			continue
		}
		out = append(out, store.Written{Val: in})
	}
	return out
}

// calleeClosureWrites lists what a closure may write through the variables it
// captured, for a closure handed to a call that may run it.
//
//declscope:package
func (c *checker) calleeClosureWrites(mc *ssa.MakeClosure) []store.Written {
	// A closure's function is always a *ssa.Function.
	fn := mc.Fn.(*ssa.Function)
	s := c.summary(fn)
	var out []store.Written
	for j, b := range mc.Bindings {
		if i := len(fn.Params) + j; i < len(s.writes) {
			for _, p := range s.writes[i] {
				must := len(p) == 0 && i < len(s.must) && s.must[i]
				out = append(out, store.Written{Val: b, Path: p, Must: must})
			}
		}
	}
	return out
}

// calleeSummary is the callee's summary, read from its body when the pass has
// one and from a fact otherwise. It is nil when neither exists.
//
//declscope:package
func (c *checker) calleeSummary(cl callee) *summary {
	fn := cl.fn
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
	return &summary{flows: f.FlowsTo, logs: f.Logs, writes: f.Writes, must: f.Must, fields: f.FieldFlows}
}

// calleeFlows lists the inputs of a call that result idx may carry as a
// whole, and the memory below inputs that it is made from.
//
//declscope:package
func (c *checker) calleeFlows(call *ssa.Call, idx int) ([]ssa.Value, []store.Written) {
	cl := resolveCallee(call.Common())
	if cl.builtin != nil {
		switch cl.builtin.Name() {
		case "append", "min", "max":
			return cl.inputs, nil
		case "ssa:wrapnilchk":
			return cl.inputs[:1], nil
		}
		return nil, nil
	}
	if cl.obj != nil {
		if known.Carries(cl.obj, idx) {
			return cl.inputs, nil
		}
		if known.Accessor(cl.obj) {
			return cl.inputs[:1], nil
		}
	}
	if s := c.calleeSummary(cl); s != nil {
		var whole []ssa.Value
		for i, in := range cl.inputs {
			if i < len(s.flows) && idx < 64 && s.flows[i]&(1<<idx) != 0 {
				whole = append(whole, in)
			}
		}
		var fields []store.Written
		for _, ff := range s.fields {
			if ff.Out == idx && ff.In < len(cl.inputs) {
				fields = append(fields, store.Written{Val: cl.inputs[ff.In], Path: ff.Path})
			}
		}
		return whole, fields
	}
	return guessCalleeFlow(cl, call, idx), nil
}

// guessCalleeFlow is the fallback for a callee nothing is known about: an
// interface method, a function value, or a function without a body. An error
// result is taken to carry the error arguments, which is what a wrapping
// helper does.
func guessCalleeFlow(cl callee, call *ssa.Call, idx int) []ssa.Value {
	// idx is an Extract's index, which is always within the tuple.
	t := call.Type()
	if tup, ok := t.(*types.Tuple); ok {
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
