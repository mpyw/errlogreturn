package linedirective

import "github.com/rs/zerolog"

// A report is anchored on the enclosing statement, which needs the file. An
// ignore above a multi-line chain below a //line directive still reaches it.
//
//line chain.tmpl:1
func chain(l *zerolog.Logger) error {
	if err := do(); err != nil {
		//errlogreturn:ignore
		l.Error().
			Err(err).
			Msg("failed")
		return err
	}
	return nil
}
