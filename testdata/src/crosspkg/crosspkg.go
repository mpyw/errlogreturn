package crosspkg

import (
	"errors"

	"helperlib"
)

func do() error { return errors.New("boom") }

// ===== SHOULD REPORT =====

func helper() error {
	err := do()
	helperlib.LogErr(err) // want `error is logged by LogErr`
	return helperlib.Wrap(err, "do")
}

func method(l *helperlib.Logger) error {
	err := do()
	l.Failure(err) // want `error is logged by \(\*Logger\)\.Failure`
	return err
}

func interfaceSink(r helperlib.Reporter) error {
	err := do()
	r.Report("failed", "err", err) // want `error is logged here`
	return err
}

func declaredSink() error {
	err := do()
	helperlib.Remote(err) // want `error is logged here`
	return err
}

// ===== SHOULD NOT REPORT =====

// The helper in another package translates the error, and a fact says so.
func translated() error {
	err := do()
	helperlib.LogErr(err)
	return helperlib.ToPublic(err)
}

// A deferred call in another package that always writes err replaces what
// was logged.
func deferredClearElsewhere() (err error) {
	defer helperlib.Clear(&err)
	if err = do(); err != nil {
		helperlib.LogErr(err)
		return err
	}
	return nil
}

// A generic writer in another package, and a method of a generic type.
func genericReset() error {
	err := do()
	helperlib.LogErr(err)
	helperlib.Reset(&err)
	return err
}

func genericMethod(b *helperlib.Box[error]) error {
	err := do()
	helperlib.LogErr(err)
	b.Set(&err)
	return err
}

func wrapOnly() error {
	return helperlib.Wrap(do(), "do")
}
