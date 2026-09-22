package zerologuse

import (
	"errors"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func do() error { return errors.New("boom") }

// ===== SHOULD REPORT =====

func chain(l *zerolog.Logger) error {
	if err := do(); err != nil {
		l.Error().Err(err).Msg("failed") // want `error is logged here`
		return err
	}
	return nil
}

func multiline(l *zerolog.Logger) error {
	if err := do(); err != nil {
		l.Error(). // want `error is logged here`
				Str("op", "do").
				Err(err).
				Msg("failed")
		return err
	}
	return nil
}

func loggerErr(l *zerolog.Logger) error {
	err := do()
	l.Err(err).Send() // want `error is logged here`
	return err
}

func globalLogger() error {
	err := do()
	log.Err(err).Msg("failed") // want `error is logged here`
	return err
}

func brokenChain(l *zerolog.Logger) error {
	err := do()
	e := l.Warn()
	e.Err(err)
	e.Msg("failed") // want `error is logged here`
	return err
}

func contextLogger(l zerolog.Logger) error {
	err := do()
	sub := l.With().Err(err).Logger()
	sub.Info().Msg("failed") // want `error is logged here`
	return err
}

func withLevel(l *zerolog.Logger) error {
	err := do()
	l.WithLevel(zerolog.ErrorLevel).Err(err).Msg("failed") // want `error is logged here`
	return err
}

// ===== SHOULD NOT REPORT =====

func debug(l *zerolog.Logger) error {
	err := do()
	l.Debug().Err(err).Msg("retrying")
	return err
}

func trace(l *zerolog.Logger) error {
	err := do()
	l.Trace().Err(err).Msg("retrying")
	return err
}

func withDebugLevel(l *zerolog.Logger) error {
	err := do()
	l.WithLevel(zerolog.DebugLevel).Err(err).Msg("retrying")
	return err
}

func notSent(l *zerolog.Logger) error {
	err := do()
	l.Error().Err(err)
	return err
}

func eventFromParam(e *zerolog.Event) error {
	err := do()
	e.Err(err).Msg("failed")
	return err
}
