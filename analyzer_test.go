package errlogreturn

import (
	"maps"
	"slices"
	"testing"
)

func TestParseSinks(t *testing.T) {
	tests := []struct {
		flag    string
		want    []string
		wantErr bool
	}{
		{flag: "", want: nil},
		{flag: "example.com/tele.Send", want: []string{"example.com/tele.Send"}},
		{flag: " a.F , (b.T).M ", want: []string{"(b.T).M", "a.F"}},
		{flag: "(*example.com/tele.Client).Capture", want: []string{"(example.com/tele.Client).Capture"}},
		{flag: "garbage((", wantErr: true},
		{flag: "a.F,noDot", wantErr: true},
		{flag: "(a.T)", wantErr: true},
		{flag: "(a.T).", wantErr: true},
		{flag: "a.F()", wantErr: true},
		{flag: "(m.L[T]).Report", want: []string{"(m.L[T]).Report"}},
		{flag: "(*m.L[K, V]).Report", want: []string{"(m.L[K, V]).Report"}},
		{flag: "(m.L[T).Report", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseSinks(tt.flag)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSinks(%q) error = %v, want error %v", tt.flag, err, tt.wantErr)
			continue
		}
		if tt.wantErr {
			continue
		}
		if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, tt.want) {
			t.Errorf("parseSinks(%q) = %v, want %v", tt.flag, keys, tt.want)
		}
	}
}

func TestSinkFlagSet(t *testing.T) {
	var f sinkFlag
	if err := f.Set("garbage(("); err == nil {
		t.Error("Set accepts a misspelled name")
	}
	if err := f.Set("a.F"); err != nil || f.String() != "a.F" || !f.names["a.F"] {
		t.Errorf("Set(a.F) = %v, %q, %v", err, f.String(), f.names)
	}
}
