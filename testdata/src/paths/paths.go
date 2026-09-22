package paths

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
)

func do() error { return errors.New("boom") }

// ===== SHOULD REPORT =====

func phiMerge(flag bool) error {
	err := do()
	if flag {
		log.Println(err) // want `error is logged here`
	} else {
		err = fmt.Errorf("other: %w", err)
	}
	return err
}

func loopRetry() error {
	var err error
	for range 3 {
		if err = do(); err == nil {
			return nil
		}
		log.Printf("retrying: %v", err) // want `error is logged here`
	}
	return err
}

// The closure logs the named result, which holds what the function returns.
func deferClosureNamedResult() (err error) {
	defer func() { // want `error is logged by a function literal and also returned at line 42`
		if err != nil {
			slog.Error("failed", "err", err)
		}
	}()
	return do()
}

func immediateClosure() error {
	err := do()
	func() { // want `error is logged by a function literal`
		log.Println(err)
	}()
	return err
}

func literalReturns() func() error {
	return func() error {
		err := do()
		log.Println(err) // want `error is logged here`
		return err
	}
}

func typeAssert() error {
	err := do()
	var pe *pathError
	if errors.As(err, &pe) {
		log.Println(pe) // want `error is logged here`
		return pe
	}
	return nil
}

type pathError struct{ path string }

func (e *pathError) Error() string { return e.path } // want Error:"carries p0→r0"

func joined() error {
	err := do()
	cerr := do()
	log.Println(cerr) // want `error is logged here`
	return errors.Join(err, cerr)
}

// ===== SHOULD NOT REPORT =====

func phiOtherBranch(flag bool) error {
	err := do()
	if flag {
		log.Println(err)
		err = errors.New("replaced")
	}
	return err
}

func loopLogsEachAndReturnsNil() error {
	for range 3 {
		if err := do(); err != nil {
			log.Println(err)
			continue
		}
	}
	return nil
}

func deferClosureReturnsNil() (err error) {
	defer func() {
		if err != nil {
			slog.Error("failed", "err", err)
		}
	}()
	err = do()
	return nil
}

func cleanupError() error {
	if err := do(); err != nil {
		if cerr := do(); cerr != nil {
			log.Println(cerr)
		}
		return err
	}
	return nil
}

func closureDoesNotReturn() {
	err := do()
	func() error {
		return err
	}()
	log.Println(err)
}

// The error logged in one iteration is not the one returned in the next.
func nextIteration(items []int) error { // want nextIteration:"carries p0→r0"
	for _, it := range items {
		err := do()
		if err != nil {
			if it > 0 {
				return fmt.Errorf("item %d: %w", it, err)
			}
			log.Println(err)
		}
	}
	return nil
}

func nextIterationContinue(items []int) error {
	for range items {
		err := do()
		if err != nil {
			var pe *pathError
			if errors.As(err, &pe) {
				log.Println(err)
				continue
			}
			return err
		}
	}
	return nil
}

// Either edge may reach the log, and the return takes one of them; which one
// the log saw is not known, so nothing is claimed.
func nextIterationEitherEdge(items []int, shared error) error { // want nextIterationEitherEdge:"carries p0→r0, carries p1→r0"
	for _, it := range items {
		var err error
		if shared != nil && it > 1 {
			err = shared
		} else {
			err = do()
		}
		if err != nil {
			if it > 0 {
				return fmt.Errorf("item %d: %w", it, err)
			}
			log.Println(err)
		}
	}
	return nil
}

// The deferred closure logs the error it assigns, not the one it captured.
func deferClosureOwnError() error {
	var err error
	defer func() {
		err = do()
		if err != nil {
			log.Println(err)
		}
	}()
	if err = do(); err != nil {
		return fmt.Errorf("first: %w", err)
	}
	return nil
}
