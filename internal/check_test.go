package internal

import (
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/mpyw/errlogreturn/internal/cond"
	"github.com/mpyw/errlogreturn/internal/loop"
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
	if got := cond.Resolve(boxed, nil); got != boxed.X {
		t.Errorf("cond.Resolve through an interface conversion = %v", got)
	}
	if got := checkStrip(boxed); got != e {
		t.Errorf("checkStrip through two conversions = %v", got)
	}
	// A cycle of resolutions ends.
	p1, p2 := &ssa.Phi{}, &ssa.Phi{}
	if got := cond.Resolve(p1, map[*ssa.Phi]ssa.Value{p1: p2, p2: p1}); got != ssa.Value(p1) && got != ssa.Value(p2) {
		t.Errorf("cond.Resolve of a cycle = %v", got)
	}
	// cond.Canon strips a conversion chain, stops at a receive, and at
	// a call that is not len or cap.
	var deep ssa.Value = n
	for range cond.MaxDepth + 2 {
		deep = &ssa.Convert{X: deep}
	}
	if got := cond.Canon(deep); got == n {
		t.Error("cond.Canon stops past its depth")
	}
	if got := cond.Canon(&ssa.ChangeInterface{X: &ssa.MakeInterface{X: e}}); got != e {
		t.Errorf("cond.Canon through interface conversions = %v", got)
	}
	recv := c[1].X
	if got := cond.Canon(recv); got != recv {
		t.Errorf("cond.Canon of a receive = %v", got)
	}
	call := &ssa.Call{Call: ssa.CallCommon{Value: fn}}
	if got := cond.Canon(call); got != ssa.Value(call) {
		t.Errorf("cond.Canon of a call = %v", got)
	}
	// loop.Uses follows a conversion and stops at a load or a receive.
	phi := &ssa.Phi{}
	if !loop.Uses(&ssa.ChangeType{X: phi}, phi, map[ssa.Value]bool{}) {
		t.Error("loop.Uses through a ChangeType")
	}
	if loop.Uses(recv, phi, map[ssa.Value]bool{}) {
		t.Error("loop.Uses through a receive")
	}
	// cond.Eval stops past its depth, and negates what it decides.
	if _, ok := cond.Eval(fn.Params[2], nil, cond.Known{}, cond.MaxDepth+1); ok {
		t.Error("cond.Eval past its depth")
	}
	var not ssa.Value
	for _, blk := range fn.Blocks {
		if iff, ok := blk.Instrs[len(blk.Instrs)-1].(*ssa.If); ok {
			if u, ok := iff.Cond.(*ssa.UnOp); ok && u.Op == token.NOT {
				not = u
			}
		}
	}
	if v, ok := cond.Eval(not, nil, cond.Known{Conds: map[ssa.Value]bool{fn.Params[2]: true}}, 0); !ok || v {
		t.Errorf("cond.Eval(!b) with b = %v, %v", v, ok)
	}
	// loop.LastOf gives up on a counter compared with itself.
	self := &ssa.BinOp{Op: token.LSS, X: phi, Y: &ssa.BinOp{Op: token.ADD, X: phi, Y: ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int])}}
	if _, ok := loop.LastOf(self, true, phi, 1, false); ok {
		t.Error("loop.LastOf of a counter against itself")
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
