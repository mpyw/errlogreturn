package linedirective

import "log"

// A //line directive renames the positions below it. The function is still
// in this file, so it is still reported.
//
//line report.tmpl:1
func reported() error {
	err := do()
	log.Println(err) // want `error is logged here and also returned at line 4`
	return err
}
