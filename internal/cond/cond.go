package cond

import (
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/store"
	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// MaxDepth bounds how deep checkKeys and Eval read a condition.
const MaxDepth = 16

// Succs lists the successors of b that the path may take: both edges of
// a branch whose condition is not decided, and only the agreeing one of a
// branch whose condition is.
func Succs(b *ssa.BasicBlock, phis map[*ssa.Phi]ssa.Value, known Known) []*ssa.BasicBlock {
	iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
	if !ok {
		return b.Succs
	}
	if v, ok := Eval(iff.Cond, phis, known, 0); ok {
		if v {
			return b.Succs[:1]
		}
		return b.Succs[1:]
	}
	return b.Succs
}

// Eval decides a boolean on the path: a constant, a condition a branch
// on the path decided, a φ-node the path resolved, or the negation of one.
func Eval(v ssa.Value, phis map[*ssa.Phi]ssa.Value, known Known, depth int) (bool, bool) {
	if depth > MaxDepth {
		return false, false
	}
	if b, ok := known.Conds[v]; ok {
		return b, true
	}
	// A boolean compared with a constant on the path decides it: after
	// v == false held, v is false.
	if typeutil.Inert(v.Type()) {
		for cond, taken := range known.Conds {
			c, ok := cond.(*ssa.BinOp)
			if !ok {
				continue
			}
			if v2, set, ok := FactOf(c); ok && v2 == v && set.eq != nil && set.eq.Kind() == constant.Bool {
				return constant.BoolVal(set.eq) == (taken == set.in), true
			}
		}
	}
	switch x := v.(type) {
	case *ssa.Const:
		if x.Value != nil && x.Value.Kind() == constant.Bool {
			return constant.BoolVal(x.Value), true
		}
	case *ssa.Phi:
		// Following the path's resolutions spends no depth: the chain is
		// as long as the path, and ends at a value that is not one.
		if e := Resolve(x, phis); e != ssa.Value(x) {
			return Eval(e, phis, known, depth)
		}
	case *ssa.UnOp:
		if x.Op == token.NOT {
			if b, ok := Eval(x.X, phis, known, depth+1); ok {
				return !b, true
			}
		}
	case *ssa.BinOp:
		return evalCmp(x, phis, known, depth)
	}
	return false, false
}

// evalCmp decides a comparison of a value with a constant from what the
// path decided about other comparisons of the same value. v == true is v.
// After mode == 1 held, mode == 2 does not. After code < 500 held,
// code >= 500 does not. After err != nil held on a value, the same test of a
// φ-node resolved to that value holds too.
func evalCmp(x *ssa.BinOp, phis map[*ssa.Phi]ssa.Value, known Known, depth int) (bool, bool) {
	v, q, ok := FactOf(x)
	if !ok {
		return false, false
	}
	if q.eq != nil && q.eq.Kind() == constant.Bool {
		if b, ok := Eval(v, phis, known, depth+1); ok {
			return (b == constant.BoolVal(q.eq)) == q.in, true
		}
		return false, false
	}
	// A φ-node the path resolved to a constant is that constant.
	if k, ok := Resolve(v, phis).(*ssa.Const); ok {
		return q.Holds(k)
	}
	if op, ok := OperandOf(v, phis); ok && q.eq == nil {
		if set, ok := known.Facts[op]; ok {
			if d, ok := set.Decides(q); ok {
				return d, true
			}
		}
	}
	for cond, taken := range known.Conds {
		c, ok := cond.(*ssa.BinOp)
		if !ok || c == x {
			continue
		}
		v2, set, ok := FactOf(c)
		if !ok || !sameOperand(v, v2, phis) {
			continue
		}
		if !taken {
			set = set.Not()
		}
		if d, ok := set.Decides(q); ok {
			return d, true
		}
	}
	return false, false
}

// Known is what a path decided. conds holds each condition a branch on
// the path decided, by its value. facts holds, for each operand compared with
// an integer or nil, the set of values the comparisons leave it: two switch
// statements over one mode decide one fact, not a condition per case.
type Known struct {
	Conds map[ssa.Value]bool
	Facts map[Operand]Set
}

// Operand names what a comparison compares: a value, or its length or
// capacity.
type Operand struct {
	v    ssa.Value
	kind byte
}

// Value is the value the operand reads.
func (op Operand) Value() ssa.Value { return op.v }

// Kind tells the reads of one value apart, as len(s) from s itself.
func (op Operand) Kind() byte { return op.kind }

// KnownFrom is what the conditions conds decide, before the path has
// resolved anything.
func KnownFrom(conds map[ssa.Value]bool) Known {
	k := Known{Conds: make(map[ssa.Value]bool), Facts: make(map[Operand]Set)}
	for cond, taken := range conds {
		k.Decide(cond, taken, nil)
	}
	return k
}

// without is a copy of k that leaves out what drop says of a condition, or
// of an operand.
func (k Known) Without(drop func(ssa.Value) bool) Known {
	out := Known{Conds: make(map[ssa.Value]bool, len(k.Conds)+1), Facts: make(map[Operand]Set, len(k.Facts)+1)}
	for cond, v := range k.Conds {
		if !drop(cond) {
			out.Conds[cond] = v
		}
	}
	for op, set := range k.Facts {
		if !drop(op.v) {
			out.Facts[op] = set
		}
	}
	return out
}

// decide records that cond is taken, or not. A comparison of an operand with
// an integer or nil narrows the operand's fact. Any other condition is kept
// as it is.
func (k Known) Decide(cond ssa.Value, taken bool, phis map[*ssa.Phi]ssa.Value) {
	if c, ok := cond.(*ssa.BinOp); ok {
		if v, set, ok := FactOf(c); ok && set.eq == nil {
			if op, ok := OperandOf(v, phis); ok {
				if !taken {
					set = set.Not()
				}
				if prev, ok := k.Facts[op]; ok && prev.isNil == set.isNil {
					set = prev.And(set)
				}
				k.Facts[op] = set
				return
			}
		}
	}
	k.Conds[cond] = taken
}

// OperandOf is the operand v stands for on the path: a value, through
// conversions to an interface and the φ-nodes the path resolved, or the
// length or capacity of one. A load is none: memory may change between two
// reads of it.
func OperandOf(v ssa.Value, phis map[*ssa.Phi]ssa.Value) (Operand, bool) {
	r := Resolve(v, phis)
	switch x := r.(type) {
	case *ssa.UnOp:
		if x.Op == token.MUL {
			return Operand{}, false
		}
	case *ssa.Call:
		// The length of a map or a channel changes with no new value: an
		// update or a send changes it. Only a slice's, an array's or a
		// string's is fixed.
		if b, ok := x.Call.Value.(*ssa.Builtin); ok && len(x.Call.Args) == 1 && fixedLen(x.Call.Args[0].Type()) {
			switch b.Name() {
			case "len":
				return Operand{v: Resolve(x.Call.Args[0], phis), kind: 'l'}, true
			case "cap":
				return Operand{v: Resolve(x.Call.Args[0], phis), kind: 'c'}, true
			}
		}
	}
	return Operand{v: r}, true
}

// fixedLen reports whether a value of type t has a length no operation
// on it changes: a slice, an array, a pointer to an array, or a string.
func fixedLen(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Slice, *types.Array:
		return true
	case *types.Pointer:
		_, ok := u.Elem().Underlying().(*types.Array)
		return ok
	case *types.Basic:
		return u.Info()&types.IsString != 0
	}
	return false
}

// Set is what a comparison with a constant says of the value compared.
// For an integer or a nil test, it is a union of intervals within [lo, hi]:
// a nil test reads nil as 0 and anything else as 1. For any other constant,
// it is equality with eq when in holds, or inequality.
type Set struct {
	iv    [][2]int64
	lo    int64
	hi    int64
	eq    constant.Value
	in    bool
	isNil bool
}

// Mix mixes the ranges of s into x, for a key of what a walk knows.
func (s Set) Mix(x uint64) uint64 {
	for _, r := range s.iv {
		x = x*1000003 ^ uint64(r[0])
		x = x*1000003 ^ uint64(r[1])
	}
	return x
}

// and is the set of values in both s and q, two sets of one kind.
func (s Set) And(q Set) Set {
	var out [][2]int64
	for _, a := range s.iv {
		for _, b := range q.iv {
			lo, hi := max(a[0], b[0]), min(a[1], b[1])
			if lo <= hi {
				out = append(out, [2]int64{lo, hi})
			}
		}
	}
	s.iv = out
	return s
}

// FactOf splits a comparison of a value with a constant into the value
// and what the comparison says of it.
func FactOf(b *ssa.BinOp) (ssa.Value, Set, bool) {
	op := b.Op
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
	default:
		return nil, Set{}, false
	}
	v, kv := b.X, b.Y
	k, ok := kv.(*ssa.Const)
	if !ok {
		v, kv = b.Y, b.X
		if k, ok = kv.(*ssa.Const); !ok {
			return nil, Set{}, false
		}
		op = Flip(op)
	}
	if _, isConst := v.(*ssa.Const); isConst {
		return nil, Set{}, false
	}
	switch {
	case k.IsNil():
		if op != token.EQL && op != token.NEQ {
			return nil, Set{}, false
		}
		s := Set{isNil: true, lo: 0, hi: 1, iv: [][2]int64{{1, 1}}}
		if op == token.EQL {
			s.iv = [][2]int64{{0, 0}}
		}
		return v, s, true
	case k.Value == nil:
		return nil, Set{}, false
	case k.Value.Kind() == constant.Int:
		n, exact := constant.Int64Val(k.Value)
		if !exact || n == math.MinInt64 || n == math.MaxInt64 {
			return nil, Set{}, false
		}
		s := Set{lo: math.MinInt64, hi: math.MaxInt64}
		switch op {
		case token.EQL:
			s.iv = [][2]int64{{n, n}}
		case token.NEQ:
			s.iv = [][2]int64{{math.MinInt64, n - 1}, {n + 1, math.MaxInt64}}
		case token.LSS:
			s.iv = [][2]int64{{math.MinInt64, n - 1}}
		case token.LEQ:
			s.iv = [][2]int64{{math.MinInt64, n}}
		case token.GTR:
			s.iv = [][2]int64{{n + 1, math.MaxInt64}}
		case token.GEQ:
			s.iv = [][2]int64{{n, math.MaxInt64}}
		}
		return v, s, true
	}
	if op != token.EQL && op != token.NEQ {
		return nil, Set{}, false
	}
	return v, Set{eq: k.Value, in: op == token.EQL}, true
}

// not is the set of values s leaves out.
func (s Set) Not() Set {
	if s.eq != nil {
		s.in = !s.in
		return s
	}
	var out [][2]int64
	next, done := s.lo, false
	for _, r := range s.iv {
		if r[0] > next {
			out = append(out, [2]int64{next, r[0] - 1})
		}
		if r[1] == s.hi {
			done = true
			break
		}
		next = r[1] + 1
	}
	if !done {
		out = append(out, [2]int64{next, s.hi})
	}
	s.iv = out
	return s
}

// decides reports what q says of a value known to lie in s: true when every
// value of s satisfies q, false when none does.
func (s Set) Decides(q Set) (bool, bool) {
	if s.eq != nil || q.eq != nil {
		if s.eq == nil || q.eq == nil || !comparableValues(s.eq, q.eq) {
			return false, false
		}
		same := constant.Compare(s.eq, token.EQL, q.eq)
		switch {
		case s.in:
			return same == q.in, true
		case same:
			return !q.in, true
		}
		return false, false
	}
	if s.isNil != q.isNil || len(s.iv) == 0 {
		return false, false
	}
	all, none := true, true
	for _, a := range s.iv {
		covered := false
		for _, b := range q.iv {
			if a[0] <= b[1] && b[0] <= a[1] {
				none = false
			}
			if b[0] <= a[0] && a[1] <= b[1] {
				covered = true
			}
		}
		if !covered {
			all = false
		}
	}
	switch {
	case all:
		return true, true
	case none:
		return false, true
	}
	return false, false
}

// holds evaluates the comparison s stands for on the constant k.
func (s Set) Holds(k *ssa.Const) (bool, bool) {
	switch {
	case s.isNil:
		if !k.IsNil() {
			return false, false
		}
		return s.iv[0][0] == 0, true
	case k.Value == nil:
		return false, false
	case s.eq != nil:
		if !comparableValues(s.eq, k.Value) {
			return false, false
		}
		return constant.Compare(s.eq, token.EQL, k.Value) == s.in, true
	case k.Value.Kind() != constant.Int:
		return false, false
	}
	n, exact := constant.Int64Val(k.Value)
	if !exact {
		return false, false
	}
	return slices.ContainsFunc(s.iv, func(r [2]int64) bool { return r[0] <= n && n <= r[1] }), true
}

// Resolve strips conversions from one interface to another from v, and
// follows a φ-node the path resolved to its edge, until a value that is
// neither. A resolution seen before ends it. A conversion of a concrete value
// to an interface is kept: a nil *T in an error is not a nil error.
func Resolve(v ssa.Value, phis map[*ssa.Phi]ssa.Value) ssa.Value {
	seen := make(map[ssa.Value]bool)
	for !seen[v] {
		seen[v] = true
		switch x := v.(type) {
		case *ssa.ChangeInterface:
			v = x.X
		case *ssa.Phi:
			e, ok := phis[x]
			if !ok {
				return v
			}
			v = e
		default:
			return v
		}
	}
	return v
}

// sameOperand reports whether a and b are one value on the path.
func sameOperand(a, b ssa.Value, phis map[*ssa.Phi]ssa.Value) bool {
	a, b = Resolve(a, phis), Resolve(b, phis)
	return a == b || Same(a, b, 0)
}

// Canon is the form of v that two reads of one bound share: the operand
// of len or cap, the memory a load reads, or what a conversion converts.
func Canon(v ssa.Value) ssa.Value {
	for range MaxDepth {
		switch x := v.(type) {
		case *ssa.Call:
			bi, ok := x.Call.Value.(*ssa.Builtin)
			if !ok || (bi.Name() != "len" && bi.Name() != "cap") || len(x.Call.Args) != 1 {
				return v
			}
			v = x.Call.Args[0]
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return v
			}
			v, _ = store.Locate(x.X)
		case *ssa.Convert:
			v = x.X
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeInterface:
			v = x.X
		default:
			return v
		}
	}
	return v
}

// comparableValues reports whether constant.Compare accepts a and b: the same
// kind, or both numeric.
func comparableValues(a, b constant.Value) bool {
	num := func(k constant.Kind) bool {
		return k == constant.Int || k == constant.Float || k == constant.Complex
	}
	return a.Kind() == b.Kind() || (num(a.Kind()) && num(b.Kind()))
}

// Same reports whether a and b compute the same bound: two reads of
// len(s), or two loads of one field that nothing can write between, which
// SSA names apart. A wrong answer decides a branch the path cannot decide,
// and the walk then leaves out an edge it could take, which misses a report.
func Same(a, b ssa.Value, depth int) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || depth > 8 {
		return false
	}
	switch x := a.(type) {
	case *ssa.Call:
		y, ok := b.(*ssa.Call)
		if !ok || x.Call.IsInvoke() || y.Call.IsInvoke() || len(x.Call.Args) != len(y.Call.Args) {
			return false
		}
		bx, ok1 := x.Call.Value.(*ssa.Builtin)
		by, ok2 := y.Call.Value.(*ssa.Builtin)
		if !ok1 || !ok2 || bx.Name() != by.Name() || (bx.Name() != "len" && bx.Name() != "cap") {
			return false
		}
		return slices.EqualFunc(x.Call.Args, y.Call.Args, func(a, b ssa.Value) bool { return Same(a, b, depth+1) })
	case *ssa.UnOp:
		y, ok := b.(*ssa.UnOp)
		if !ok || x.Op != token.MUL || y.Op != token.MUL || !nothingBetween(x, y) {
			return false
		}
		rx, px := store.Locate(x.X)
		ry, py := store.Locate(y.X)
		return Same(rx, ry, depth+1) && slices.Equal(px, py)
	case *ssa.Convert:
		y, ok := b.(*ssa.Convert)
		return ok && Same(x.X, y.X, depth+1)
	}
	return false
}

// nothingBetween reports whether loads x and y sit in one block with no
// store and no call between them, so that they read one value.
func nothingBetween(x, y *ssa.UnOp) bool {
	if x.Block() != y.Block() {
		return false
	}
	instrs := x.Block().Instrs
	i, j := slices.Index(instrs, ssa.Instruction(x)), slices.Index(instrs, ssa.Instruction(y))
	for _, instr := range instrs[min(i, j)+1 : max(i, j)] {
		switch instr.(type) {
		case *ssa.Store, ssa.CallInstruction, *ssa.MapUpdate, *ssa.Send:
			return false
		}
	}
	return true
}

// Flip swaps the sides of a comparison.
func Flip(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	}
	return op
}

// Negate is the comparison that holds when op does not.
func Negate(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GEQ
	case token.LEQ:
		return token.GTR
	case token.GTR:
		return token.LEQ
	case token.GEQ:
		return token.LSS
	case token.EQL:
		return token.NEQ
	case token.NEQ:
		return token.EQL
	}
	return op
}
