package errlogreturn

import "testing"

func TestSinkFlagSet(t *testing.T) {
	var f sinkFlag
	if err := f.Set("garbage(("); err == nil {
		t.Error("Set accepts a misspelled name")
	}
	if err := f.Set("a.F"); err != nil || f.String() != "a.F" || !f.names["a.F"] {
		t.Errorf("Set(a.F) = %v, %q, %v", err, f.String(), f.names)
	}
}
