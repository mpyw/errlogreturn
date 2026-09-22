package logrususe

import (
	"errors"

	"github.com/sirupsen/logrus"
)

func do() error { return errors.New("boom") }

// ===== SHOULD REPORT =====

func withError() error {
	err := do()
	logrus.WithError(err).Error("failed") // want `error is logged here`
	return err
}

func pkgLevel() error {
	err := do()
	logrus.Errorf("failed: %v", err) // want `error is logged here`
	return err
}

func method(l *logrus.Logger) error {
	err := do()
	l.Warnf("failed: %v", err) // want `error is logged here`
	return err
}

func level(l *logrus.Logger) error {
	err := do()
	l.WithError(err).Log(logrus.ErrorLevel, "failed") // want `error is logged here`
	return err
}

// ===== SHOULD NOT REPORT =====

func debug(l *logrus.Logger) error {
	err := do()
	logrus.Debug(err)
	l.WithError(err).Debug("retrying")
	l.WithError(err).Log(logrus.DebugLevel, "retrying")
	return err
}

func notLogged() error {
	err := do()
	_ = logrus.WithError(err)
	return err
}
