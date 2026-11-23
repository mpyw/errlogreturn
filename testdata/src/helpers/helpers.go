package helpers

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
)

func do() error { return errors.New("boom") }

// ===== helpers the summaries recognize =====

func logErr(err error) { // want logErr:"logs p0"
	slog.Error("failed", "err", err)
}

func logIfErr(err error) { // want logIfErr:"logs p0"
	if err != nil {
		log.Println(err)
	}
}

func logUnlessNil(err error) { // want logUnlessNil:"logs p0"
	if err == nil {
		return
	}
	log.Println(err)
}

func logViaHelper(err error) { // want logViaHelper:"logs p0"
	logErr(fmt.Errorf("wrapped: %w", err))
}

type service struct{ logger *slog.Logger }

func (s *service) logError(msg string, err error) { // want logError:"logs p1, logs p2"
	s.logger.Error(msg, "err", err)
}

// ===== helpers that do not count =====

var errIgnored = errors.New("ignored")

func logSome(err error) { // conditional on something other than nil
	if !errors.Is(err, errIgnored) {
		log.Println(err)
	}
}

func logDebug(err error) {
	slog.Debug("trace", "err", err)
}

func recurse(err error, n int) {
	if n > 0 {
		recurse(err, n-1)
		return
	}
	log.Println(err)
}

// ===== SHOULD REPORT =====

func callsHelper() error {
	if err := do(); err != nil {
		logErr(err) // want `error is logged by logErr and also returned at line 68`
		return err
	}
	return nil
}

func callsNilGuard() error {
	err := do()
	logIfErr(err) // want `error is logged by logIfErr`
	return fmt.Errorf("wrap: %w", err)
}

func callsNestedHelper() error {
	err := do()
	logViaHelper(err) // want `error is logged by logViaHelper`
	return err
}

func (s *service) callsMethod() error {
	if err := do(); err != nil {
		s.logError("do", err) // want `error is logged by \(\*service\)\.logError`
		return err
	}
	return nil
}

func logAndReturn(err error) error { // want logAndReturn:"carries p0→r0, logs p0"
	log.Println(err) // want `error is logged here`
	return err
}

func deferredHelper() (err error) {
	err = do()
	defer logErr(err) // want `error is logged by logErr`
	return err
}

func goHelper() error {
	err := do()
	go logErr(err) // want `error is logged by logErr`
	return err
}

// ===== SHOULD NOT REPORT =====

func callsConditionalHelper() error {
	err := do()
	logSome(err)
	return err
}

func callsDebugHelper() error {
	err := do()
	logDebug(err)
	return err
}

func callsRecursiveHelper() error {
	err := do()
	recurse(err, 3)
	return err
}

// The helper both logs and returns, and is reported there. Returning what it
// returns hands over an error that was already reported once.
func returnsWhatHelperReturns() error {
	if err := do(); err != nil {
		return logAndReturn(err)
	}
	return nil
}

func helperLogsOther() error {
	err := do()
	logErr(errIgnored)
	return err
}

// ===== helpers that log a merge of their argument and another error =====

// The helper logs err or other, whichever the flag picks. It logs err only
// when the flag is false, so it is not a logger of either.
func logEither(err, other error, useOther bool) {
	e := err
	if useOther {
		e = other
	}
	log.Println(e)
}

func callsLogEither() error {
	err := do()
	logEither(err, errIgnored, true)
	return err
}

var errPublic = errors.New("internal error")

// The helper logs a public error in place of an internal one.
func logPublic(err error, internal bool) {
	if internal {
		err = errPublic
	}
	log.Println(err)
}

func callsLogPublic() error {
	err := do()
	logPublic(err, true)
	return err
}

// The same merge through a variable whose address is taken: the log reads
// one of two stores.
func logEitherStored(err, other error, useOther bool) {
	e := err
	p := &e
	if useOther {
		*p = other
	}
	log.Println(e)
}

func callsLogEitherStored() error {
	err := do()
	logEitherStored(err, errIgnored, true)
	return err
}

// Every edge of the merge carries err, so the helper logs it on every path.
func logWrappedOrNot(err error, wrap bool) { // want logWrappedOrNot:"logs p0"
	e := err
	if wrap {
		e = fmt.Errorf("wrapped: %w", err)
	}
	log.Println(e)
}

func callsLogWrappedOrNot() error {
	err := do()
	logWrappedOrNot(err, true) // want `error is logged by logWrappedOrNot`
	return err
}

// A loop that wraps the error again on each pass still carries it.
func logWrappedTimes(err error, n int) { // want logWrappedTimes:"logs p0"
	for i := 0; i < n; i++ {
		err = fmt.Errorf("again: %w", err)
	}
	log.Println(err)
}

func callsLogWrappedTimes() error {
	err := do()
	logWrappedTimes(err, 2) // want `error is logged by logWrappedTimes`
	return err
}

// Every merge carries err, but each one reads the one before it inside a
// loop. What a merge carries then assumes the loop's own merge, so it is met
// again from each edge, twice as often per merge. The trace gives up past its
// bound, and the helper is not counted.
func logManyMerges(err error, n int, c []bool) {
	for i := 0; i < n; i++ {
		if c[0] {
			err = fmt.Errorf("0: %w", err)
		}
		if c[1] {
			err = fmt.Errorf("1: %w", err)
		}
		if c[2] {
			err = fmt.Errorf("2: %w", err)
		}
		if c[3] {
			err = fmt.Errorf("3: %w", err)
		}
		if c[4] {
			err = fmt.Errorf("4: %w", err)
		}
		if c[5] {
			err = fmt.Errorf("5: %w", err)
		}
		if c[6] {
			err = fmt.Errorf("6: %w", err)
		}
		if c[7] {
			err = fmt.Errorf("7: %w", err)
		}
		if c[8] {
			err = fmt.Errorf("8: %w", err)
		}
		if c[9] {
			err = fmt.Errorf("9: %w", err)
		}
		if c[10] {
			err = fmt.Errorf("10: %w", err)
		}
		if c[11] {
			err = fmt.Errorf("11: %w", err)
		}
		if c[12] {
			err = fmt.Errorf("12: %w", err)
		}
	}
	log.Println(err)
}
