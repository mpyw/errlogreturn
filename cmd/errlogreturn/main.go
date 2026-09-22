// Command errlogreturn reports an error that is both logged and returned.
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/mpyw/errlogreturn"
)

func main() {
	singlechecker.Main(errlogreturn.Analyzer)
}
