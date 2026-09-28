package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSinkDirectives runs the built command over //errlogreturn:sink in every
// place it can be written: a function, a method, and a method of an interface
// declared at package level, inside a function body, or in a parameter type.
// The directive is found by walking the syntax, so each place is a separate
// case of that walk.
func TestSinkDirectives(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "errlogreturn")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	mod := filepath.Join(dir, "mod")
	for name, src := range map[string]string{
		"go.mod": "module example.com/e2e\n\ngo 1.27\n",
		"p/p.go": `package p

import (
	"errors"
	"fmt"
)

func do() error { return errors.New("x") }

type impl struct{}

func (impl) Report(args ...any) { _ = fmt.Sprint(args...) }

//errlogreturn:sink
func Send(args ...any) { _ = fmt.Sprint(args...) }

//errlogreturn:sink
func (impl) Log(args ...any) { _ = fmt.Sprint(args...) }

type Top interface {
	Report(args ...any) //errlogreturn:sink
}

func Func() error { err := do(); Send(err); return err }

func Method() error { err := do(); impl{}.Log(err); return err }

func Package(r Top) error { err := do(); r.Report(err); return err }

func Local() error {
	type reporter interface {
		//errlogreturn:sink
		Report(args ...any)
	}
	var r reporter = impl{}
	err := do()
	r.Report(err)
	return err
}

func Param(r interface {
	//errlogreturn:sink
	Report(args ...any)
}) error {
	err := do()
	r.Report(err)
	return err
}
`,
	} {
		path := filepath.Join(mod, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "./...")
	cmd.Dir = mod
	out, err := cmd.CombinedOutput()
	if exit := (*exec.ExitError)(nil); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("want exit 3 with reports, got %v:\n%s", err, out)
	}
	var got []string
	for l := range strings.Lines(string(out)) {
		if i := strings.Index(l, "p/p.go:"); i >= 0 && strings.Contains(l, "error is logged here") {
			got = append(got, strings.TrimRight(l[i:], "\n"))
		}
	}
	slices.Sort(got)
	want := []string{
		"p/p.go:24:34: error is logged here and also returned at line 24; log it or return it, not both",
		"p/p.go:26:36: error is logged here and also returned at line 26; log it or return it, not both",
		"p/p.go:28:42: error is logged here and also returned at line 28; log it or return it, not both",
		"p/p.go:37:2: error is logged here and also returned at line 38; log it or return it, not both",
		"p/p.go:46:2: error is logged here and also returned at line 47; log it or return it, not both",
	}
	if !slices.Equal(got, want) {
		t.Errorf("reports:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
