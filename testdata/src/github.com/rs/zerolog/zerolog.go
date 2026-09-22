// Package zerolog is a stub of github.com/rs/zerolog for the tests.
package zerolog

import "context"

type Level int8

const (
	TraceLevel Level = -1
	DebugLevel Level = 0
	InfoLevel  Level = 1
	WarnLevel  Level = 2
	ErrorLevel Level = 3
	FatalLevel Level = 4
	PanicLevel Level = 5
)

type Logger struct{ ctx []byte }

type Event struct{ buf []byte }

type Context struct{ l Logger }

func New() Logger                      { return Logger{} }
func Ctx(ctx context.Context) *Logger  { return &Logger{} }
func (l *Logger) Trace() *Event        { return &Event{} }
func (l *Logger) Debug() *Event        { return &Event{} }
func (l *Logger) Info() *Event         { return &Event{} }
func (l *Logger) Warn() *Event         { return &Event{} }
func (l *Logger) Error() *Event        { return &Event{} }
func (l *Logger) Fatal() *Event        { return &Event{} }
func (l *Logger) Err(err error) *Event { return &Event{} }
func (l *Logger) Log() *Event          { return &Event{} }
func (l *Logger) WithLevel(Level) *Event {
	return &Event{}
}
func (l Logger) With() Context                  { return Context{l} }
func (c Context) Err(err error) Context         { return c }
func (c Context) Logger() Logger                { return c.l }
func (e *Event) Err(err error) *Event           { return e }
func (e *Event) Str(key, val string) *Event     { return e }
func (e *Event) Ctx(ctx context.Context) *Event { return e }
func (e *Event) Msg(msg string)                 {}
func (e *Event) Msgf(format string, v ...any)   {}
func (e *Event) Send()                          {}
