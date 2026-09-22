package generics

import (
	"errors"
	"fmt"
	"log"
)

func do() error { return errors.New("boom") }

func logAny[T any](v T) { // want logAny:"logs p0"
	log.Println(v)
}

func wrap[E error](e E) error { // want wrap:"carries p0→r0"
	return fmt.Errorf("wrapped: %w", e)
}

type box[T any] struct{ v T }

func (b *box[T]) logValue(v T) { // want logValue:"logs p1"
	log.Println(v)
}

func genericMethod() error {
	err := do()
	b := &box[error]{}
	b.logValue(err) // want `error is logged by \(\*box\)\.logValue`
	return err
}

// ===== SHOULD REPORT =====

func genericHelper() error {
	err := do()
	logAny(err) // want `error is logged by logAny`
	return err
}

func genericWrap() error {
	err := do()
	log.Println(err) // want `error is logged here`
	return wrap(err)
}

// ===== SHOULD NOT REPORT =====

func genericWrapOnly() error {
	return wrap(do())
}
