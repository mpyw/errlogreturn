// Package recursive holds types that refer to themselves. Asking whether a
// value of one can carry an error must end.
package recursive

type self []self

type left []right

type right []left

type nested [][2]struct{ inner []nested }

type array [1][]array

type generic[P any] []generic[P]

type viaPointer []*viaPointer

func useSelf(v self)               {}
func useMutual(l left, r right)    {}
func useNested(v nested)           {}
func useArray(v array)             {}
func useGeneric(v generic[int])    {}
func usePointer(v viaPointer)      {}
func closure(v self) func()        { return func() { _ = v } }
func returns(v self) (self, error) { return v, nil } // want returns:"carries p0→r0"
