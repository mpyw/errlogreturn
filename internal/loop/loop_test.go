package loop

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

func TestLinearAndLast(t *testing.T) {
	sp := testBuild(t, `package p
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
		body := body(h)
		lc, ok := counterOf(h, h.Succs[0], phi, body)
		if !ok {
			t.Errorf("%s: no counter", name)
			continue
		}
		if lc.known != w.known || (w.known && lc.last.off != w.off) {
			t.Errorf("%s: last = %+v, known %v, want %d, %v", name, lc.last, lc.known, w.off, w.known)
		}
	}
	if l := linear(ssa.NewConst(constant.MakeUint64(math.MaxUint64), types.Typ[types.Uint64])); l.base == nil {
		t.Error("a constant too big for int64 is its own base")
	}
}

func TestAt(t *testing.T) {
	sp := testBuild(t, `package p
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
		if i < 2 { g() }
		if 0 < i { g() }
		if i == 0 { g() }
		if i != 0 { g() }
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
	c := testConds(fn)[1:]
	type want struct{ v, ok bool }
	// Against n-1, counting up: a guard against a constant is read for a
	// large n.
	up := []want{{true, true}, {true, true}, {true, true}, {false, false}, {false, false}, {true, true}, {false, true}, {true, true}, {false, true}, {true, true}}
	// Counting down toward n, the last value falls as n does.
	down := []want{{true, true}, {true, true}, {false, true}, {false, false}, {false, false}, {true, true}, {true, true}, {false, true}, {false, true}, {true, true}}
	for _, run := range []struct {
		step  int64
		wants []want
	}{{1, up}, {-1, down}} {
		for i, w := range run.wants {
			v, ok := at(c[i], phi, Lin{base: n, off: -1}, run.step)
			if v != w.v || ok != w.ok {
				t.Errorf("step %d: At(%v) = %v, %v, want %+v", run.step, c[i], v, ok, w)
			}
		}
	}
	// Against a constant last value, a guard over another bound is not read.
	if _, ok := at(c[3], phi, Lin{off: 2}, 1); ok {
		t.Error("At reads i < m against a constant last value")
	}
	if v, ok := at(c[6], phi, Lin{off: 2}, 1); v || !ok {
		t.Errorf("At(i < 2) at 2 = %v, %v, want false, true", v, ok)
	}
	if _, ok := at(n, phi, Lin{base: n, off: -1}, 1); ok {
		t.Error("At reads a value that is no comparison")
	}
	odd := &ssa.BinOp{Op: token.AND, X: phi, Y: n}
	if _, ok := at(odd, phi, Lin{base: n, off: -1}, 1); ok {
		t.Error("At reads a bit mask")
	}
	if _, ok := farHolds(token.LSS, 0); ok {
		t.Error("farHolds reads a counter that does not step")
	}
	if _, ok := farHolds(token.AND, 1); ok {
		t.Error("farHolds reads a bit mask")
	}
}
