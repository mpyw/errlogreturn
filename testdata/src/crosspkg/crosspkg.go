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

func wrapOnly() error {
	return helperlib.Wrap(do(), "do")
}
