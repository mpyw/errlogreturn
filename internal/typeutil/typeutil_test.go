package typeutil

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestMayCarry(t *testing.T) {
	const src = `package p
type self []self
type left []right
type right []left
type generic[P any] []generic[P]
type errs []error
type strs [2][]string
type stringer interface{ String() string }
type n int
var (
	vSelf    self
	vLeft    left
	vGeneric generic[int]
	vErrs    errs
	vStrs    strs
	vAny     any
	vStr     stringer
	vN       n
)`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"vSelf": false, "vLeft": false, "vGeneric": false,
		"vErrs": true, "vStrs": true, "vAny": true, "vStr": false, "vN": false,
	}
	for name, w := range want {
		typ := pkg.Scope().Lookup(name).Type()
		if got := MayCarry(typ); got != w {
			t.Errorf("MayCarry(%s) = %v, want %v", typ, got, w)
		}
	}
	if !Inert(pkg.Scope().Lookup("vN").Type()) || Inert(pkg.Scope().Lookup("vStrs").Type()) {
		t.Error("Inert: want true for a named int, false for a string array")
	}
}

func TestIsErrorNil(t *testing.T) {
	if IsError(nil) {
		t.Error("IsError(nil)")
	}
}
