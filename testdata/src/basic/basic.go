package basic

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
)

func do() error { return errors.New("boom") }

// ===== SHOULD REPORT =====

func logAndReturn() error {
	if err := do(); err != nil {
		log.Printf("do failed: %v", err) // want `error is logged here and also returned at line 18`
		return err
	}
	return nil
}

func logAndWrap() error {
	err := do()
	if err != nil {
		slog.Error("do failed", "err", err) // want `error is logged here`
		return fmt.Errorf("wrap: %w", err)
	}
	return nil
}

func logAndWrapV() (int, error) {
	err := do()
	if err != nil {
		fmt.Println(err) // want `error is logged here`
		return 0, fmt.Errorf("wrap: %v", err)
	}
	return 1, nil
}

func logMessageAndReturn() error {
	err := do()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed: %s\n", err.Error()) // want `error is logged here`
		return err
	}
	return nil
}

func logThenReturnLater() error {
	err := do()
	if err != nil {
		log.Print(err) // want `error is logged here`
	}
	return err
}

type queryError struct{ cause error }

func (e *queryError) Error() string { return e.cause.Error() } // want Error:"carries p0→r0"

func logAndStructWrap() error {
	if err := do(); err != nil {
		log.Println(err) // want `error is logged here`
		return &queryError{cause: err}
	}
	return nil
}

var errSentinel = errors.New("sentinel")

func logSentinel() error {
	log.Println(errSentinel) // want `error is logged here`
	return errSentinel
}

// ===== SHOULD NOT REPORT =====

func onlyLog() {
	if err := do(); err != nil {
		log.Println(err)
	}
}

func onlyReturn() error {
	if err := do(); err != nil {
		return fmt.Errorf("wrap: %w", err)
	}
	return nil
}

func logAndReturnOther() error {
	if err := do(); err != nil {
		log.Println(err)
		return errSentinel
	}
	return nil
}

func separateBranches(flag bool) error {
	err := do()
	if flag {
		log.Println(err)
		return nil
	}
	return err
}

func reassigned() error {
	err := do()
	if err != nil {
		log.Println(err)
	}
	err = do()
	return err
}

func debugLevel() error {
	if err := do(); err != nil {
		slog.Debug("retrying", "err", err)
		return err
	}
	return nil
}

func fatal() error {
	if err := do(); err != nil {
		log.Fatal(err)
	}
	return nil
}

func toPublic(err error) error {
	if errors.Is(err, errSentinel) {
		return errSentinel
	}
	return errors.New("internal")
}

func logAndTranslate() error {
	if err := do(); err != nil {
		log.Println(err)
		return toPublic(err)
	}
	return nil
}

func logValueNotError(n int) error { // want logValueNotError:"carries p0→r0"
	log.Println(n)
	return fmt.Errorf("bad %d", n)
}

func fprintfToWriter(w *os.File) error {
	err := do()
	fmt.Fprintln(w, err)
	return err
}
