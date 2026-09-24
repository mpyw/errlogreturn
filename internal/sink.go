package internal

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/known"
)

// sinkUse is one place where values leave through a logger.
//
//declscope:package
type sinkUse struct {
	// values are what is logged.
	values []ssa.Value
	// via is the call's own value when the logging happens inside the
	// callee. A value the callee returns was logged there, and is reported
	// there, so it is not traced back through that call.
	via ssa.Value
	// by names the callee that logs, for the message. It is empty when the
	// instruction calls a logger directly.
	by string
	// deferred is set for a deferred call, whose closure bindings are read
	// when the function returns rather than where it is deferred.
	deferred bool
}

// sinkAt reports what instr logs, if it logs anything: directly through a
// logger, or through a callee whose summary logs some of its inputs.
//
//declscope:package
func (c *checker) sinkAt(instr ssa.Instruction) (sinkUse, bool) {
	ci, ok := instr.(ssa.CallInstruction)
	if !ok {
		return sinkUse{}, false
	}
	_, deferred := instr.(*ssa.Defer)
	cl := resolveCallee(ci.Common())

	if vals, ok := c.directSink(cl); ok {
		return sinkUse{values: vals, deferred: deferred}, len(vals) > 0
	}
	if cl.builtin != nil || (cl.obj != nil && known.Package(cl.obj)) {
		return sinkUse{}, false
	}
	s := c.calleeSummary(cl)
	if s == nil {
		return sinkUse{}, false
	}
	var vals []ssa.Value
	for i, in := range cl.inputs {
		if i < len(s.logs) && s.logs[i] {
			vals = append(vals, in)
		}
	}
	if len(vals) == 0 {
		return sinkUse{}, false
	}
	u := sinkUse{values: vals, by: sinkName(cl), deferred: deferred}
	if call, ok := instr.(*ssa.Call); ok {
		u.via = call
	}
	return u, true
}

// directSink reports whether the callee is a logger, and which of its inputs
// it logs.
func (c *checker) directSink(cl callee) ([]ssa.Value, bool) {
	if cl.obj == nil {
		return nil, false
	}
	if c.userSink(cl.obj) {
		return cl.inputs, true
	}
	return known.Logs(cl.obj, cl.inputs)
}

// userSink reports whether obj is named by -sinks or declared with
// //errlogreturn:sink, here or in the package that declares it.
func (c *checker) userSink(obj *types.Func) bool {
	// The flag's names have the * of a pointer receiver dropped.
	if c.cfg.Sinks[strings.Replace(obj.FullName(), "(*", "(", 1)] {
		return true
	}
	if c.directives.Sink(obj) {
		return true
	}
	var f Fact
	return c.pass.ImportObjectFact(obj, &f) && f.Sink
}

// sinkName names the callee that logs, the way a reader would spell it.
func sinkName(cl callee) string {
	obj := cl.calleeDeclared()
	if obj == nil {
		return "a function literal"
	}
	sig, _ := obj.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return obj.Name()
	}
	t := sig.Recv().Type()
	star := ""
	if p, ok := t.(*types.Pointer); ok {
		t, star = p.Elem(), "*"
	}
	if n, ok := t.(*types.Named); ok {
		return "(" + star + n.Obj().Name() + ")." + obj.Name()
	}
	return obj.Name()
}
