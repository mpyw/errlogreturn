package cond

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

// testBuild builds src, a package named p, and returns its SSA package.
func testBuild(t *testing.T, src string) *ssa.Package {
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

// testConds lists the conditions of the branches of fn, in block order.
func testConds(fn *ssa.Function) []*ssa.BinOp {
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

func TestSet(t *testing.T) {
	sp := testBuild(t, `package p
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
	c := testConds(sp.Func("F"))
	fact := func(i int) Set {
		t.Helper()
		_, s, ok := FactOf(c[i])
		if !ok {
			t.Fatalf("FactOf(%v) failed", c[i])
		}
		return s
	}
	lt5, gt10, ne7, eq7, ge3flip, ge3 := fact(0), fact(1), fact(2), fact(3), fact(4), fact(5)
	isNil, notNil, isA, notB := fact(6), fact(7), fact(8), fact(9)

	decides := func(name string, s, q Set, want, wantOK bool) {
		t.Helper()
		got, ok := s.Decides(q)
		if ok != wantOK || (ok && got != want) {
			t.Errorf("%s: decides = %v, %v, want %v, %v", name, got, ok, want, wantOK)
		}
	}
	decides("x<5 => x>10", lt5, gt10, false, true)
	decides("x<5 => !(x>10)", lt5, gt10.Not(), true, true)
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
	decides("s!=b => s==b", notB, notB.Not(), false, true)
	decides("s!=b => s==a", notB, isA, false, false)
	decides("s==a against int", isA, lt5, false, false)
	decides("empty set", Set{lo: 0, hi: 1}, isNil, false, false)

	if got := gt10.Not().Not(); len(got.iv) != 1 || got.iv[0] != [2]int64{11, math.MaxInt64} {
		t.Errorf("not(not(x>10)) = %v", got.iv)
	}
	if got := isNil.Not(); len(got.iv) != 1 || got.iv[0] != [2]int64{1, 1} {
		t.Errorf("not(nil) = %v", got.iv)
	}

	holds := func(name string, s Set, k *ssa.Const, want, wantOK bool) {
		t.Helper()
		got, ok := s.Holds(k)
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

	if _, _, ok := FactOf(c[11]); ok {
		t.Error("FactOf accepts a value compared with itself")
	}
	if v, _, ok := FactOf(c[12]); !ok || v != c[12].X {
		t.Error("FactOf reads a sum compared with a constant")
	}
}

func TestFactOfRejects(t *testing.T) {
	sp := testBuild(t, `package p
func g() {}
const big = 1 << 63 - 1
func F(x int64, e error, f float64) {
	if x < big { g() }
	if e == nil { g() }
	if f < 1.5 { g() }
	if x&1 == 0 { g() }
}`)
	c := testConds(sp.Func("F"))
	if _, _, ok := FactOf(c[0]); ok {
		t.Error("a bound at MaxInt64 is not read")
	}
	if _, _, ok := FactOf(c[2]); ok {
		t.Error("an ordered test of a float is not read")
	}
	nilLess := &ssa.BinOp{Op: token.LSS, X: c[1].X, Y: c[1].Y}
	if _, _, ok := FactOf(nilLess); ok {
		t.Error("an ordered test against nil is not read")
	}
	and := &ssa.BinOp{Op: token.AND, X: c[3].X, Y: c[3].Y}
	if _, _, ok := FactOf(and); ok {
		t.Error("a bit mask is not a comparison")
	}
}

func TestFlipNegate(t *testing.T) {
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
		if got := Flip(op); got != want[0] {
			t.Errorf("Flip(%v) = %v, want %v", op, got, want[0])
		}
		if got := Negate(op); got != want[1] {
			t.Errorf("Negate(%v) = %v, want %v", op, got, want[1])
		}
	}
}

func TestSame(t *testing.T) {
	sp := testBuild(t, `package p
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
	c := testConds(sp.Func("F"))
	wants := []bool{false, true, false, true, false, true, false, false, false}
	for i, want := range wants {
		if got := Same(c[i].X, c[i].Y, 0); got != want {
			t.Errorf("Same(%v, %v) = %v, want %v", c[i].X, c[i].Y, got, want)
		}
	}
	if Same(c[0].X, nil, 0) {
		t.Error("Same matches nil")
	}
	if Same(c[0].X, c[0].X, MaxDepth+9) != true {
		t.Error("a value is itself at any depth")
	}
	if Same(c[1].X, c[1].Y, 9) {
		t.Error("Same gives up past its depth")
	}
}

func TestCanonAndResolve(t *testing.T) {
	sp := testBuild(t, `package p
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
	c := testConds(fn)
	if got := Canon(c[0].X); got != fn.Params[0] {
		t.Errorf("Canon(len(s)) = %v", got)
	}
	if got := Canon(c[1].X); got != fn.Params[1] {
		t.Errorf("Canon(c.max) = %v", got)
	}
	if got := Canon(c[2].X); got != fn.Params[2] {
		t.Errorf("Canon(int64(x)) = %v", got)
	}
	var ret ssa.Value
	for _, b := range fn.Blocks {
		if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
			ret = r.Results[0]
		}
	}
	if got := Canon(ret); got != fn.Params[3] {
		t.Errorf("Canon(any(e)) = %v", got)
	}
	if got := Resolve(ret, nil); got != fn.Params[3] {
		t.Errorf("Resolve(any(e)) = %v", got)
	}
	if got := Canon(c[0].Y); got != c[0].Y {
		t.Errorf("Canon(const) = %v", got)
	}
}

func TestComparable(t *testing.T) {
	i, f, s := constant.MakeInt64(1), constant.MakeFloat64(1.5), constant.MakeString("a")
	if !comparableValues(i, f) || comparableValues(i, s) || !comparableValues(s, s) {
		t.Error("comparableValues")
	}
}
