// Package sinkname reads the names of extra sinks, as the -sinks flag and the
// golangci-lint plugin's settings spell them.
package sinkname

import (
	"fmt"
	"regexp"
	"strings"
)

// spelling is a function or a method as go/types names it:
// pkg/path.Func, or (pkg/path.Type).Method with an optional * before the
// type, and type arguments after it for a generic type.
var spelling = regexp.MustCompile(`^(\(\*?[^()*\s\[\]]+\.[\pL_][\pL\pN_]*(\[[^()\[\]]+\])?\)|[^()*\s\[\]]+)\.[\pL_][\pL\pN_]*$`)

// Parse reads a comma-separated list of names. The * of a pointer receiver
// is dropped, so that either spelling names the method whatever its receiver
// is. A name that is not spelled like a function is refused, since a typo
// would otherwise switch its sink off in silence.
func Parse(list string) (map[string]bool, error) {
	m := make(map[string]bool)
	for _, s := range split(list) {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !spelling.MatchString(s) {
			return nil, fmt.Errorf("%q is not spelled pkg/path.Func or (pkg/path.Type).Method", s)
		}
		m[strings.Replace(s, "(*", "(", 1)] = true
	}
	return m, nil
}

// split splits a list at the commas outside brackets, so that the type
// arguments of (pkg.Map[K, V]).Put stay in one name.
func split(list string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range list {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, list[start:i])
				start = i + 1
			}
		}
	}
	return append(out, list[start:])
}
