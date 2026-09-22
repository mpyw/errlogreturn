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
