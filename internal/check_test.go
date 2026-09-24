package internal

import (
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"math"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// checkTestBuild builds src, a package named p, and returns its SSA package.
func checkTestBuild(t *testing.T, src string) *ssa.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := types.NewPackage("p", "p")
	sp, _, err := ssautil.BuildPackage(&types.Config{Importer: importer.Default()}, fset, pkg, []*ast.File{f}, ssa.SanityCheckFunctions)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// checkTestConds lists the conditions of the branches of fn, in block order.
func checkTestConds(fn *ssa.Function) []*ssa.BinOp {
	var out []*ssa.BinOp
	for _, b := range fn.Blocks {
		if iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If); ok {
			if c, ok := iff.Cond.(*ssa.BinOp); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func TestCheckSet(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
func F(x int, e error, s string) {
	if x < 5 { g() }
	if x > 10 { g() }
	if x != 7 { g() }
	if x == 7 { g() }
	if 3 <= x { g() }
	if x >= 3 { g() }
	if e == nil { g() }
	if e != nil { g() }
	if s == "a" { g() }
	if s != "b" { g() }
	if 1.5 == float64(x) { g() }
	if e == e { g() }
	if x+x > 0 { g() }
}`)
	c := checkTestConds(sp.Func("F"))
	fact := func(i int) checkSet {
		t.Helper()
		_, s, ok := checkFactOf(c[i])
		if !ok {
			t.Fatalf("checkFactOf(%v) failed", c[i])
		}
		return s
	}
	lt5, gt10, ne7, eq7, ge3flip, ge3 := fact(0), fact(1), fact(2), fact(3), fact(4), fact(5)
	isNil, notNil, isA, notB := fact(6), fact(7), fact(8), fact(9)

	decides := func(name string, s, q checkSet, want, wantOK bool) {
		t.Helper()
		got, ok := s.decides(q)
		if ok != wantOK || (ok && got != want) {
			t.Errorf("%s: decides = %v, %v, want %v, %v", name, got, ok, want, wantOK)
		}
	}
	decides("x<5 => x>10", lt5, gt10, false, true)
	decides("x<5 => !(x>10)", lt5, gt10.not(), true, true)
	decides("x==7 => x!=7", eq7, ne7, false, true)
	decides("x!=7 => x==7", ne7, eq7, false, true)
	decides("x!=7 => x<5", ne7, lt5, false, false)
	decides("3<=x flipped is x>=3", ge3flip, ge3, true, true)
	decides("x>=3 => x<5", ge3, lt5, false, false)
	decides("nil => nil", isNil, isNil, true, true)
	decides("nil => not nil", isNil, notNil, false, true)
	decides("nil against int", isNil, lt5, false, false)
	decides("s==a => s!=b", isA, notB, true, true)
	decides("s==a => s==a", isA, isA, true, true)
	decides("s!=b => s==b", notB, notB.not(), false, true)
	decides("s!=b => s==a", notB, isA, false, false)
	decides("s==a against int", isA, lt5, false, false)
	decides("empty set", checkSet{lo: 0, hi: 1}, isNil, false, false)

	if got := gt10.not().not(); len(got.iv) != 1 || got.iv[0] != [2]int64{11, math.MaxInt64} {
		t.Errorf("not(not(x>10)) = %v", got.iv)
	}
	if got := isNil.not(); len(got.iv) != 1 || got.iv[0] != [2]int64{1, 1} {
		t.Errorf("not(nil) = %v", got.iv)
	}

	holds := func(name string, s checkSet, k *ssa.Const, want, wantOK bool) {
		t.Helper()
		got, ok := s.holds(k)
		if ok != wantOK || (ok && got != want) {
			t.Errorf("%s: holds = %v, %v, want %v, %v", name, got, ok, want, wantOK)
		}
	}
	errNil := ssa.NewConst(nil, types.Universe.Lookup("error").Type())
	holds("nil holds on nil", isNil, errNil, true, true)
	holds("not nil on nil", notNil, errNil, false, true)
	holds("nil on an int", isNil, ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int]), false, false)
	holds("x<5 on 3", lt5, ssa.NewConst(constant.MakeInt64(3), types.Typ[types.Int]), true, true)
	holds("x<5 on 9", lt5, ssa.NewConst(constant.MakeInt64(9), types.Typ[types.Int]), false, true)
	holds("x<5 on nil", lt5, errNil, false, false)
	holds("x<5 on a string", lt5, ssa.NewConst(constant.MakeString("a"), types.Typ[types.String]), false, false)
	holds("x<5 on a huge int", lt5, ssa.NewConst(constant.MakeUint64(math.MaxUint64), types.Typ[types.Uint64]), false, false)
	holds("s==a on a", isA, ssa.NewConst(constant.MakeString("a"), types.Typ[types.String]), true, true)
	holds("s==a on an int", isA, ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int]), false, false)

	if _, _, ok := checkFactOf(c[11]); ok {
		t.Error("checkFactOf accepts a value compared with itself")
	}
	if v, _, ok := checkFactOf(c[12]); !ok || v != c[12].X {
		t.Error("checkFactOf reads a sum compared with a constant")
	}
}

func TestCheckFactOfRejects(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
const big = 1 << 63 - 1
func F(x int64, e error, f float64) {
	if x < big { g() }
	if e == nil { g() }
	if f < 1.5 { g() }
	if x&1 == 0 { g() }
}`)
	c := checkTestConds(sp.Func("F"))
	if _, _, ok := checkFactOf(c[0]); ok {
		t.Error("a bound at MaxInt64 is not read")
	}
	if _, _, ok := checkFactOf(c[2]); ok {
		t.Error("an ordered test of a float is not read")
	}
	nilLess := &ssa.BinOp{Op: token.LSS, X: c[1].X, Y: c[1].Y}
	if _, _, ok := checkFactOf(nilLess); ok {
		t.Error("an ordered test against nil is not read")
	}
	and := &ssa.BinOp{Op: token.AND, X: c[3].X, Y: c[3].Y}
	if _, _, ok := checkFactOf(and); ok {
		t.Error("a bit mask is not a comparison")
	}
}

func TestCheckFlipNegate(t *testing.T) {
	pairs := map[token.Token][2]token.Token{
		token.LSS: {token.GTR, token.GEQ},
		token.LEQ: {token.GEQ, token.GTR},
		token.GTR: {token.LSS, token.LEQ},
		token.GEQ: {token.LEQ, token.LSS},
		token.EQL: {token.EQL, token.NEQ},
		token.NEQ: {token.NEQ, token.EQL},
		token.ADD: {token.ADD, token.ADD},
	}
	for op, want := range pairs {
		if got := checkFlip(op); got != want[0] {
			t.Errorf("checkFlip(%v) = %v, want %v", op, got, want[0])
		}
		if got := checkNegate(op); got != want[1] {
			t.Errorf("checkNegate(%v) = %v, want %v", op, got, want[1])
		}
	}
}

func TestCheckLinearAndLast(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
func Up(n int) {
	for i := 0; i < n; i++ { g() }
}
func UpLeq(n int) {
	for i := 0; i <= n+2; i++ { g() }
}
func Flipped(n int) {
	for i := 0; n > i; i++ { g() }
}
func Down(n int) {
	for i := n; i >= 1; i-- { g() }
}
func DownGt(n int) {
	for i := n; 0 < i; i-- { g() }
}
func Conv(n int32) {
	for i := int64(0); i < int64(n); i++ { g() }
}
func Mul(n int) {
	for i := 1; i < n; i *= 2 { g() }
}
func Wrong(n int) {
	for i := 0; i > n; i++ { g() }
}`)
	type want struct {
		off   int64
		known bool
	}
	cases := map[string]want{
		"Up": {-1, true}, "UpLeq": {2, true}, "Flipped": {-1, true},
		"Down": {1, true}, "DownGt": {1, true}, "Conv": {-1, true},
		"Mul": {0, false}, "Wrong": {0, false},
	}
	for name, w := range cases {
		fn := sp.Func(name)
		var h *ssa.BasicBlock
		var phi *ssa.Phi
		for _, b := range fn.Blocks {
			for _, in := range b.Instrs {
				if p, ok := in.(*ssa.Phi); ok && h == nil {
					h, phi = b, p
				}
			}
		}
		body := checkBody(h)
		lc, ok := checkCounter(h, h.Succs[0], phi, body)
		if !ok {
			t.Errorf("%s: no counter", name)
			continue
		}
		if lc.known != w.known || (w.known && lc.last.off != w.off) {
			t.Errorf("%s: last = %+v, known %v, want %d, %v", name, lc.last, lc.known, w.off, w.known)
		}
	}
	if l := checkLinear(ssa.NewConst(constant.MakeUint64(math.MaxUint64), types.Typ[types.Uint64])); l.base == nil {
		t.Error("a constant too big for int64 is its own base")
	}
}

func TestCheckAt(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
func F(n int, m int) {
	for i := 0; i < n; i++ {
		if i <= n-1 { g() }
		if i >= n-1 { g() }
		if i > 0 { g() }
		if i < m { g() }
		if 1 < 2 { g() }
		if n > 3 { g() }
		if i != n { g() }
	}
}`)
	fn := sp.Func("F")
	var phi *ssa.Phi
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			if p, ok := in.(*ssa.Phi); ok {
				phi = p
			}
		}
	}
	n := fn.Params[0]
	last := checkLin{base: n, off: -1}
	c := checkTestConds(fn)[1:]
	type want struct{ v, ok, open bool }
	wants := []want{{true, true, false}, {true, true, false}, {false, false, true}, {false, false, false}, {false, false, false}, {true, true, false}}
	for i, w := range wants {
		v, ok, open := checkAt(c[i], phi, last)
		if v != w.v || ok != w.ok || open != w.open {
			t.Errorf("checkAt(%v) = %v, %v, %v, want %+v", c[i], v, ok, open, w)
		}
	}
	if _, ok, _ := checkAt(n, phi, last); ok {
		t.Error("checkAt reads a value that is no comparison")
	}
	odd := &ssa.BinOp{Op: token.AND, X: phi, Y: n}
	if _, ok, _ := checkAt(odd, phi, last); ok {
		t.Error("checkAt reads a bit mask")
	}
}

func TestCheckSame(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
type T struct{ max int; min int }
func F(s []int, u []int, c *T, x int32) {
	if len(s) > cap(s) { g() }
	if len(s) > len(s) { g() }
	if len(s) > len(u) { g() }
	if c.max > c.max { g() }
	if c.max > c.min { g() }
	if int64(x) > int64(x) { g() }
	if len(s) > int(x) { g() }
	if cap(s) > len(s) { g() }
	a := c.max
	g()
	if a > c.max { g() }
}`)
	c := checkTestConds(sp.Func("F"))
	wants := []bool{false, true, false, true, false, true, false, false, false}
	for i, want := range wants {
		if got := checkSame(c[i].X, c[i].Y, 0); got != want {
			t.Errorf("checkSame(%v, %v) = %v, want %v", c[i].X, c[i].Y, got, want)
		}
	}
	if checkSame(c[0].X, nil, 0) {
		t.Error("checkSame matches nil")
	}
	if checkSame(c[0].X, c[0].X, checkMaxDepth+9) != true {
		t.Error("a value is itself at any depth")
	}
	if checkSame(c[1].X, c[1].Y, 9) {
		t.Error("checkSame gives up past its depth")
	}
}

func TestCheckCanonAndResolve(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
type T struct{ max int }
func F(s []int, c *T, x int32, e error) any {
	if len(s) > 0 { g() }
	if c.max > 0 { g() }
	if int64(x) > 0 { g() }
	var a any = e
	return a
}`)
	fn := sp.Func("F")
	c := checkTestConds(fn)
	if got := checkCanon(c[0].X); got != fn.Params[0] {
		t.Errorf("checkCanon(len(s)) = %v", got)
	}
	if got := checkCanon(c[1].X); got != fn.Params[1] {
		t.Errorf("checkCanon(c.max) = %v", got)
	}
	if got := checkCanon(c[2].X); got != fn.Params[2] {
		t.Errorf("checkCanon(int64(x)) = %v", got)
	}
	var ret ssa.Value
	for _, b := range fn.Blocks {
		if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
			ret = r.Results[0]
		}
	}
	if got := checkCanon(ret); got != fn.Params[3] {
		t.Errorf("checkCanon(any(e)) = %v", got)
	}
	if got := checkResolve(ret, nil); got != fn.Params[3] {
		t.Errorf("checkResolve(any(e)) = %v", got)
	}
	if got := checkCanon(c[0].Y); got != c[0].Y {
		t.Errorf("checkCanon(const) = %v", got)
	}
}

func TestCheckComparable(t *testing.T) {
	i, f, s := constant.MakeInt64(1), constant.MakeFloat64(1.5), constant.MakeString("a")
	if !checkComparable(i, f) || checkComparable(i, s) || !checkComparable(s, s) {
		t.Error("checkComparable")
	}
}

func TestCheckConversions(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
type named int
func F(e error, n int, b bool, ch chan int) {
	if n > 0 { g() }
	if b { g() }
	if <-ch > 0 { g() }
	nb := !b
	if nb { g() }
	_ = e
}`)
	fn := sp.Func("F")
	e, n := fn.Params[0], fn.Params[1]
	c := checkTestConds(fn)
	boxed := &ssa.ChangeInterface{X: &ssa.MakeInterface{X: e}}
	if got := checkResolve(boxed, nil); got != boxed.X {
		t.Errorf("checkResolve through an interface conversion = %v", got)
	}
	if got := checkStrip(boxed); got != e {
		t.Errorf("checkStrip through two conversions = %v", got)
	}
	// A cycle of resolutions ends.
	p1, p2 := &ssa.Phi{}, &ssa.Phi{}
	if got := checkResolve(p1, map[*ssa.Phi]ssa.Value{p1: p2, p2: p1}); got != ssa.Value(p1) && got != ssa.Value(p2) {
		t.Errorf("checkResolve of a cycle = %v", got)
	}
	// checkCanon strips a conversion chain, stops at a receive, and at
	// a call that is not len or cap.
	var deep ssa.Value = n
	for range checkMaxDepth + 2 {
		deep = &ssa.Convert{X: deep}
	}
	if got := checkCanon(deep); got == n {
		t.Error("checkCanon stops past its depth")
	}
	if got := checkCanon(&ssa.ChangeInterface{X: &ssa.MakeInterface{X: e}}); got != e {
		t.Errorf("checkCanon through interface conversions = %v", got)
	}
	recv := c[1].X
	if got := checkCanon(recv); got != recv {
		t.Errorf("checkCanon of a receive = %v", got)
	}
	call := &ssa.Call{Call: ssa.CallCommon{Value: fn}}
	if got := checkCanon(call); got != ssa.Value(call) {
		t.Errorf("checkCanon of a call = %v", got)
	}
	// checkUses follows a conversion and stops at a load or a receive.
	phi := &ssa.Phi{}
	if !checkUses(&ssa.ChangeType{X: phi}, phi, map[ssa.Value]bool{}) {
		t.Error("checkUses through a ChangeType")
	}
	if checkUses(recv, phi, map[ssa.Value]bool{}) {
		t.Error("checkUses through a receive")
	}
	// checkEval stops past its depth, and negates what it decides.
	if _, ok := checkEval(fn.Params[2], nil, checkKnown{}, checkMaxDepth+1); ok {
		t.Error("checkEval past its depth")
	}
	var not ssa.Value
	for _, blk := range fn.Blocks {
		if iff, ok := blk.Instrs[len(blk.Instrs)-1].(*ssa.If); ok {
			if u, ok := iff.Cond.(*ssa.UnOp); ok && u.Op == token.NOT {
				not = u
			}
		}
	}
	if v, ok := checkEval(not, nil, checkKnown{conds: map[ssa.Value]bool{fn.Params[2]: true}}, 0); !ok || v {
		t.Errorf("checkEval(!b) with b = %v, %v", v, ok)
	}
	// checkLastOf gives up on a counter compared with itself.
	self := &ssa.BinOp{Op: token.LSS, X: phi, Y: &ssa.BinOp{Op: token.ADD, X: phi, Y: ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int])}}
	if _, ok := checkLastOf(self, true, phi, 1, false); ok {
		t.Error("checkLastOf of a counter against itself")
	}
}

func TestCheckLimits(t *testing.T) {
	// A chain longer than the bounds of checkReadsVar and checkDependsOn.
	var v ssa.Value = ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int])
	for range 300 {
		v = &ssa.BinOp{Op: token.ADD, X: v, Y: ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int])}
	}
	if !checkReadsVar(v, &ssa.FreeVar{}, map[ssa.Value]bool{}) {
		t.Error("checkReadsVar answers yes past its bound")
	}
	if !checkDependsOn(v, &ssa.BasicBlock{}, map[ssa.Value]bool{}) {
		t.Error("checkDependsOn answers yes past its bound")
	}
}

func TestCheckCycles(t *testing.T) {
	sp := checkTestBuild(t, `package p
func g() {}
func F(p bool, n int) {
	for i := 0; i < n; i++ { g() }
	if p { goto Y }
L:
	g()
	if n > 0 { return }
Y:
	g()
	goto L
}`)
	fn := sp.Func("F")
	on := checkCycles(fn)
	if on[fn.Blocks[0]] {
		t.Error("the entry is on no cycle")
	}
	count := 0
	for range on {
		count++
	}
	if count < 4 {
		t.Errorf("checkCycles found %d blocks on cycles, want the loop's and the goto cycle's", count)
	}
}
