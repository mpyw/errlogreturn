package linedirective

import "log"

// Two //line directives give two lines one number. The ignore is on a.tmpl
// line 12, and the report below is on b.tmpl line 13. They are lines apart in
// the file, so the ignore does not reach the report.
//
//line a.tmpl:10
func first() error {
	err := do()
	//errlogreturn:ignore
	log.Println(err)
	return err
}

//line b.tmpl:11
func second() error {
	err := do()
	log.Println(err) // want `error is logged here and also returned at line 14`
	return err
}
