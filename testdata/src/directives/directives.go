package directives

import (
	"errors"
	"log"
)

func do() error { return errors.New("boom") }

func sameLine() error {
	err := do()
	log.Println(err) //errlogreturn:ignore the caller logs at debug level only
	return err
}

func lineAbove() error {
	err := do()
	//errlogreturn:ignore
	log.Println(err)
	return err
}

// A space after the slashes makes a comment prose, as it does for //go:
// directives, so it silences nothing.
func spaced() error {
	err := do()
	// errlogreturn:ignore
	log.Println(err) // want `error is logged here`
	return err
}

func unused() error {
	//errlogreturn:ignore // want `unused errlogreturn:ignore directive`
	return do()
}

func misplaced() {
	//errlogreturn:sink // want `errlogreturn:sink belongs in the doc comment`
	_ = 0
}

//errlogreturn:typo // want `unknown directive errlogreturn:typo`
func unknown() {}

// errlogreturn: this comment is prose, not a directive.
func prose() {}
