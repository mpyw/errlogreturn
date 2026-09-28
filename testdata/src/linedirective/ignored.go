package linedirective

import "log"

// An ignore below a //line directive still silences the line under it, and
// is not reported as unused.
//
//line ignored.tmpl:1
func ignored() error {
	err := do()
	//errlogreturn:ignore
	log.Println(err)
	return err
}
