// Package helperlib is imported by crosspkg, so that its summaries reach the
// caller as facts.
package helperlib

import (
	"fmt"
	"log/slog"
)

func LogErr(err error) { // want LogErr:"logs p0"
	slog.Error("failed", "err", err)
}

func Wrap(err error, msg string) error { // want Wrap:"carries p0→r0, carries p1→r0"
	return &wrapped{cause: err, msg: msg}
}

type wrapped struct {
	cause error
	msg   string
}

func (w *wrapped) Error() string { return w.msg + ": " + w.cause.Error() } // want Error:"carries p0→r0"

type Logger struct{ base *slog.Logger }

func (l *Logger) Failure(err error) { // want Failure:"logs p1"
	l.base.Error("failure", "err", err)
}

// Reporter is implemented elsewhere; the directive says what every
// implementation does.
type Reporter interface {
	//errlogreturn:sink
	Report(msg string, args ...any) // want Report:"sink"
}

// Remote sends its arguments to a service the analysis cannot see.
//
//errlogreturn:sink
func Remote(args ...any) { // want Remote:"sink"
	_ = fmt.Sprint(args...)
}

var ErrInternal = fmt.Errorf("internal")

// ToPublic hides the cause from the caller. It returns a sentinel, and carries
// nothing of its argument.
func ToPublic(err error) error { // want ToPublic:"carries nothing"
	return ErrInternal
}

// Clear always drops the error it is handed.
func Clear(p *error) { *p = nil } // want Clear:`^writes p0$`

// Reset sets what p points to to its zero value.
func Reset[T any](p *T) { var z T; *p = z } // want Reset:`^writes p0$`

type Box[T any] struct{ v T }

func (b *Box[T]) Set(p *T) { *p = b.v } // want Set:`^writes p1$`
