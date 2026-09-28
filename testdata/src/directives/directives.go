package directives

import (
	"errors"
	"log"
)

func do() error { return errors.New("boom") }

func sameLine() error {
	err := do()
	log.Println(err) //errlogreturn:ignore // the caller logs at debug level only
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

// A reason goes after //. Text after the name that is not behind // is
// reported, and the directive does nothing.
func reasonWithoutSlashes() error {
	err := do()
	//errlogreturn:ignore the caller traces it // want `errlogreturn:ignore takes no argument; write a reason after //`
	log.Println(err) // want `error is logged here`
	return err
}

func reasonAfterDash() error {
	err := do()
	//errlogreturn:ignore - the caller traces it // want `errlogreturn:ignore takes no argument; write a reason after //`
	log.Println(err) // want `error is logged here`
	return err
}

// With no space before the reason, the directive still ends at //.
func reasonGlued() error {
	err := do()
	//errlogreturn:ignore//the caller traces it
	log.Println(err)
	return err
}

// A sink takes no argument either. This one is not a sink.
//
//errlogreturn:sink all arguments // want `errlogreturn:sink takes no argument; write a reason after //`
func notASink(v any) { _ = v }

func viaNotASink() error {
	err := do()
	notASink(err)
	return err
}

// A reason after // keeps the sink.
//
//errlogreturn:sink // sends every argument to a remote tracer
func sinkWithReason(v any) { _ = v } // want sinkWithReason:"sink"

func viaSinkWithReason() error {
	err := do()
	sinkWithReason(err) // want `error is logged here`
	return err
}
