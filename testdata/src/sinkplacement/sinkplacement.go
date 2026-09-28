package sinkplacement

import (
	"errors"
	"fmt"
)

func do() error { return errors.New("boom") }

type impl struct{}

func (impl) Report(args ...any) { _ = fmt.Sprint(args...) }

// A sink on a method with a receiver.
//
//errlogreturn:sink
func (impl) Log(args ...any) { // want Log:"sink"
	_ = fmt.Sprint(args...)
}

func method() error {
	err := do()
	impl{}.Log(err) // want `error is logged here`
	return err
}

// A sink on a method of an interface declared inside a function body.
func local() error {
	type reporter interface {
		//errlogreturn:sink
		Report(args ...any) // want Report:"sink"
	}
	var r reporter = impl{}
	err := do()
	r.Report(err) // want `error is logged here`
	return err
}

// A sink in the trailing comment of an interface method.
type trailing interface {
	Report(args ...any) //errlogreturn:sink // want Report:"sink"
}

func viaTrailing(r trailing) error {
	err := do()
	r.Report(err) // want `error is logged here`
	return err
}

// A sink on a method of an interface written in a parameter type.
func param(r interface {
	//errlogreturn:sink
	Report(args ...any) // want Report:"sink"
}) error {
	err := do()
	r.Report(err) // want `error is logged here`
	return err
}
