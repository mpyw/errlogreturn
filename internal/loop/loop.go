package loop

import (
	"go/constant"
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/cond"
)

// Exits lists the loop exits the walk from a log in block b does not
// take. A loop ends by a test of its counter, and each test ends it at one
// value of the counter. The exit is taken right after a log only when the log
// runs in that iteration. So an exit is kept when its value is the last one
// on which the log can run, and every branch that decides the log leads to it
// there. Every other exit of a counted loop is dropped. A retry loop that
// breaks before logging its last attempt never exits right after a log.
func Exits(b *ssa.BasicBlock) map[[2]*ssa.BasicBlock]bool {
	out := make(map[[2]*ssa.BasicBlock]bool)
	for h := b; h != nil; h = h.Idom() {
		body := body(h)
		if !body[b] {
			continue
		}
		for _, in := range h.Instrs {
			phi, ok := in.(*ssa.Phi)
			if !ok {
				break
			}
			lc, ok := counterOf(h, b, phi, body)
			if !ok {
				continue
			}
			// A loop with one test ends only by it, on whatever value. A
			// log that no branch on the counter decides runs on that one.
			if len(lc.tests) == 1 && !readsCounter(h, b, phi, lc, body) {
				continue
			}
			for _, t := range lc.tests {
				if !lc.known || t.at.off != lc.last.off || !logsAt(h, b, phi, lc, body) {
					out[t.exit] = true
				}
			}
		}
	}
	return out
}

// body lists the blocks of the loop headed by h: those h dominates that
// lead back to it. It is empty when h heads no loop.
func body(h *ssa.BasicBlock) map[*ssa.BasicBlock]bool {
	body := make(map[*ssa.BasicBlock]bool)
	var stack []*ssa.BasicBlock
	for _, p := range h.Preds {
		if h.Dominates(p) && !body[p] {
			body[p] = true
			stack = append(stack, p)
		}
	}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if x == h {
			continue
		}
		for _, p := range x.Preds {
			if h.Dominates(p) && !body[p] {
				body[p] = true
				stack = append(stack, p)
			}
		}
	}
	if len(body) > 0 {
		body[h] = true
	}
	return body
}

// readsCounter reports whether a branch inside the loop headed by h,
// other than the loop's own tests, reads the counter phi or another counter
// of the loop to decide the log block b.
func readsCounter(h, b *ssa.BasicBlock, phi *ssa.Phi, lc counted, body map[*ssa.BasicBlock]bool) bool {
	for d := b; d != nil && d != h; d = d.Idom() {
		if len(d.Preds) != 1 {
			continue
		}
		p := d.Preds[0]
		iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok || lc.isTest[p] || !h.Dominates(p) || p.Succs[0] == p.Succs[1] {
			continue
		}
		if Uses(iff.Cond, phi, make(map[ssa.Value]bool)) || otherCounter(h, phi, iff.Cond, body) {
			return true
		}
	}
	return false
}

// otherCounter reports whether test reads a φ-node of h other than phi
// that steps by a constant on every back edge: another counter of the loop.
func otherCounter(h *ssa.BasicBlock, phi *ssa.Phi, test ssa.Value, body map[*ssa.BasicBlock]bool) bool {
	for _, in := range h.Instrs {
		other, ok := in.(*ssa.Phi)
		if !ok {
			break
		}
		if other == phi || !Uses(test, other, make(map[ssa.Value]bool)) {
			continue
		}
		steps := true
		for j, e := range other.Edges {
			if j < len(h.Preds) && body[h.Preds[j]] {
				if l := linear(e); l.base != other || l.off == 0 {
					steps = false
				}
			}
		}
		if steps {
			return true
		}
	}
	return false
}

// logsAt reports whether the log block b may run on the last iteration
// of the loop headed by h, whose counter is phi: each branch inside the loop
// that decides b and reads the counter, other than the loop's own tests, leads
// to b at the counter's last value, or may for some bound. A branch the rule
// cannot read, such as i%2 == 0, is taken to lead away, which leans toward
// silence.
func logsAt(h, b *ssa.BasicBlock, phi *ssa.Phi, lc counted, body map[*ssa.BasicBlock]bool) bool {
	for d := b; d != nil && d != h; d = d.Idom() {
		if len(d.Preds) != 1 {
			continue
		}
		p := d.Preds[0]
		iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok || lc.isTest[p] || !h.Dominates(p) || p.Succs[0] == p.Succs[1] {
			continue
		}
		// A guard on another counter of the loop, as j > 1 is when j
		// counts down beside i, cannot be read at i's last value.
		if otherCounter(h, phi, iff.Cond, body) {
			return false
		}
		if !Uses(iff.Cond, phi, make(map[ssa.Value]bool)) {
			continue
		}
		v, ok, open := at(iff.Cond, phi, lc.last)
		if open {
			// The branch leads to the log for some bound. The walk
			// follows it there, as it follows any branch it cannot rule
			// out.
			continue
		}
		if !ok || v != (d == p.Succs[0]) {
			return false
		}
	}
	return true
}

// Uses reports whether v is computed from phi, through arithmetic,
// comparisons and conversions.
func Uses(v ssa.Value, phi *ssa.Phi, seen map[ssa.Value]bool) bool {
	if v == ssa.Value(phi) {
		return true
	}
	if seen[v] || len(seen) > 64 {
		return false
	}
	seen[v] = true
	switch x := v.(type) {
	case *ssa.BinOp:
		return Uses(x.X, phi, seen) || Uses(x.Y, phi, seen)
	case *ssa.UnOp:
		return x.Op != token.MUL && x.Op != token.ARROW && Uses(x.X, phi, seen)
	case *ssa.Convert:
		return Uses(x.X, phi, seen)
	case *ssa.ChangeType:
		return Uses(x.X, phi, seen)
	case *ssa.Phi:
		// A counter changed in the body is a φ-node made from it.
		return slices.ContainsFunc(x.Edges, func(e ssa.Value) bool { return Uses(e, phi, seen) })
	}
	return false
}

// Lin is a value in linear form: base plus off, or off alone when base
// is nil.
type Lin struct {
	base ssa.Value
	off  int64
}

// linear reads v as a base plus a constant.
func linear(v ssa.Value) Lin {
	switch x := v.(type) {
	case *ssa.Const:
		if x.Value != nil && x.Value.Kind() == constant.Int {
			if n, exact := constant.Int64Val(x.Value); exact {
				return Lin{off: n}
			}
		}
	case *ssa.Convert:
		return linear(x.X)
	case *ssa.BinOp:
		l, r := linear(x.X), linear(x.Y)
		switch {
		case x.Op == token.ADD && r.base == nil:
			return Lin{base: l.base, off: l.off + r.off}
		case x.Op == token.ADD && l.base == nil:
			return Lin{base: r.base, off: l.off + r.off}
		case x.Op == token.SUB && r.base == nil:
			return Lin{base: l.base, off: l.off - r.off}
		}
	}
	return Lin{base: v}
}

// counted is what a loop's tests of its counter say: each test, and the
// last value of the counter on which the log can run, when every test can be
// read.
type counted struct {
	tests  []exitTest
	isTest map[*ssa.BasicBlock]bool
	last   Lin
	known  bool
}

// exitTest is a branch that ends a loop by its counter: the exit edge, and
// the counter's value in the iteration whose log the exit follows.
type exitTest struct {
	exit [2]*ssa.BasicBlock
	at   Lin
}

// counterOf finds the tests of phi, a number defined at the head h of a
// loop holding b, by which the loop ends: branches inside the loop with one
// edge out of it, whose condition reads phi. The head's condition, a test at
// the bottom of the body and a break in the middle all count. A φ-node no
// such test reads, such as a count of failures, is not a counter.
//
// Each test is read when phi steps by 1 or -1 and the test compares it with
// one bound. A test before the log in the iteration ends the loop on the next
// iteration, so the log it follows ran on the last value that passes it. A
// test after the log ends the loop in the iteration that logged, on the first
// value that fails. The log runs on no value past the least of these.
func counterOf(h, b *ssa.BasicBlock, phi *ssa.Phi, body map[*ssa.BasicBlock]bool) (counted, bool) {
	if bt, ok := phi.Type().Underlying().(*types.Basic); !ok || bt.Info()&types.IsNumeric == 0 {
		return counted{}, false
	}
	// Every back edge must step the counter by the same 1 or -1. A body
	// that adds 2 on one path and 1 on another has no last value to read.
	var step int64
	for j, e := range phi.Edges {
		if j >= len(h.Preds) || !body[h.Preds[j]] {
			continue
		}
		l := linear(e)
		if l.base != phi || (l.off != 1 && l.off != -1) || (step != 0 && l.off != step) {
			step = 0
			break
		}
		step = l.off
	}
	lc := counted{isTest: make(map[*ssa.BasicBlock]bool), known: step != 0}
	for _, p := range h.Parent().Blocks {
		if !body[p] {
			continue
		}
		iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok || p.Succs[0] == p.Succs[1] {
			continue
		}
		in0, in1 := body[p.Succs[0]], body[p.Succs[1]]
		if in0 == in1 {
			continue
		}
		cmp, ok := iff.Cond.(*ssa.BinOp)
		if !ok || (!Uses(cmp.X, phi, map[ssa.Value]bool{}) && !Uses(cmp.Y, phi, map[ssa.Value]bool{})) {
			continue
		}
		exit := p.Succs[1]
		if in1 {
			exit = p.Succs[0]
		}
		t := exitTest{exit: [2]*ssa.BasicBlock{p, exit}}
		if lc.known {
			// A test in the log's own block comes after the log.
			at, ok := LastOf(cmp, in0, phi, step, p == b || !p.Dominates(b))
			if ok && (len(lc.tests) == 0 || at.base == lc.last.base) {
				t.at = at
				if len(lc.tests) == 0 || (step > 0 && at.off < lc.last.off) || (step < 0 && at.off > lc.last.off) {
					lc.last = at
				}
			} else {
				lc.known = false
			}
		}
		lc.isTest[p] = true
		lc.tests = append(lc.tests, t)
	}
	if len(lc.tests) == 0 {
		return counted{}, false
	}
	return lc, true
}

// LastOf is the counter's value on the last iteration a test cmp lets
// run: stay says whether the loop goes on when cmp holds, after whether the
// test comes after the log in the iteration.
func LastOf(cmp *ssa.BinOp, stay bool, phi *ssa.Phi, step int64, after bool) (Lin, bool) {
	op, lhs, rhs := cmp.Op, linear(cmp.X), linear(cmp.Y)
	if lhs.base != ssa.Value(phi) {
		lhs, rhs = rhs, lhs
		op = cond.Flip(op)
	}
	if lhs.base != ssa.Value(phi) || rhs.base == ssa.Value(phi) {
		return Lin{}, false
	}
	if !stay {
		op = cond.Negate(op)
	}
	// Counting toward a bound, going on while it is not reached is going on
	// while it is below it, or above it when counting down.
	if op == token.NEQ {
		op = token.LSS
		if step < 0 {
			op = token.GTR
		}
	}
	// The loop goes on while phi + lhs.off op rhs.
	k := lhs.off
	var last int64
	switch {
	case step > 0 && op == token.LSS:
		last = rhs.off - k - 1
	case step > 0 && op == token.LEQ:
		last = rhs.off - k
	case step < 0 && op == token.GTR:
		last = rhs.off - k + 1
	case step < 0 && op == token.GEQ:
		last = rhs.off - k
	default:
		return Lin{}, false
	}
	if after {
		last += step
	}
	return Lin{base: rhs.base, off: last}, true
}

// at evaluates the comparison test with phi at the value last, when both
// sides are linear over one base. open reports that both sides are linear
// but over different bases, as i > 0 is against a last value of n-1: it holds
// for some bound and fails for another.
func at(test ssa.Value, phi *ssa.Phi, last Lin) (v, ok, open bool) {
	cmp, isCmp := test.(*ssa.BinOp)
	if !isCmp {
		return false, false, false
	}
	sub := func(l Lin) Lin {
		if l.base == ssa.Value(phi) {
			return Lin{base: last.base, off: last.off + l.off}
		}
		return l
	}
	onPhi := linear(cmp.X).base == ssa.Value(phi) || linear(cmp.Y).base == ssa.Value(phi)
	lhs, rhs := sub(linear(cmp.X)), sub(linear(cmp.Y))
	if !onPhi {
		return false, false, false
	}
	if lhs.base != rhs.base && !cond.Same(lhs.base, rhs.base, 0) {
		// Against a constant, a guard holds on the last value for some
		// bound: i > 0 does whenever the loop runs twice. Against another
		// bound, as i < *last is, nothing is known.
		open := (lhs.base == nil || rhs.base == nil) && !Uses(lhs.base, phi, map[ssa.Value]bool{}) && !Uses(rhs.base, phi, map[ssa.Value]bool{})
		return false, false, open
	}
	d := lhs.off - rhs.off
	switch cmp.Op {
	case token.EQL:
		return d == 0, true, false
	case token.NEQ:
		return d != 0, true, false
	case token.LSS:
		return d < 0, true, false
	case token.LEQ:
		return d <= 0, true, false
	case token.GTR:
		return d > 0, true, false
	case token.GEQ:
		return d >= 0, true, false
	}
	return false, false, false
}
