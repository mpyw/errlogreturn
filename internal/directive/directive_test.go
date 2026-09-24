package directive

import (
	"go/ast"
	"testing"
)

func TestVerb(t *testing.T) {
	tests := []struct {
		text string
		want string
		ok   bool
	}{
		{text: "//errlogreturn:ignore", want: "ignore", ok: true},
		{text: "//errlogreturn:ignore because", want: "ignore", ok: true},
		{text: "// errlogreturn:ignore", ok: false},
		{text: "//errlogreturn:sink", want: "sink", ok: true},
		{text: "// errlogreturn:sink", ok: false},
		{text: "//go:generate stringer", ok: false},
		{text: "//errlogreturn:typo", want: "typo", ok: true},
		{text: "// errlogreturn: see the README", ok: false},
		{text: "// errlogreturn:typo", ok: false},
		{text: "/*errlogreturn:ignore*/", ok: false},
		{text: "// nothing here", ok: false},
	}
	for _, tt := range tests {
		got, ok := verb(&ast.Comment{Text: tt.text})
		if ok != tt.ok || got != tt.want {
			t.Errorf("verb(%q) = %q, %v, want %q, %v", tt.text, got, ok, tt.want, tt.ok)
		}
	}
}
