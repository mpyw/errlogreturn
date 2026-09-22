package zapuse

import (
	"errors"

	"go.uber.org/zap"
)

func do() error { return errors.New("boom") }

// ===== SHOULD REPORT =====

func field(l *zap.Logger) error {
	if err := do(); err != nil {
		l.Error("failed", zap.Error(err)) // want `error is logged here`
		return err
	}
	return nil
}

func with(l *zap.Logger) error {
	err := do()
	l.With(zap.Error(err)).Info("failed") // want `error is logged here`
	return err
}

func sugared(l *zap.Logger) error {
	err := do()
	l.Sugar().Errorw("failed", "err", err) // want `error is logged here`
	return err
}

func level(l *zap.Logger) error {
	err := do()
	l.Log(zap.ErrorLevel, "failed", zap.Error(err)) // want `error is logged here`
	return err
}

// ===== SHOULD NOT REPORT =====

func debug(l *zap.Logger) error {
	err := do()
	l.Debug("retrying", zap.Error(err))
	l.Sugar().Debugw("retrying", "err", err)
	l.Log(zap.DebugLevel, "retrying", zap.Error(err))
	return err
}

func fatal(l *zap.Logger) error {
	err := do()
	l.Fatal("failed", zap.Error(err))
	return err
}

func variableLevel(l *zap.Logger, lvl zap.Level) error {
	err := do()
	l.Log(lvl, "failed", zap.Error(err))
	return err
}
