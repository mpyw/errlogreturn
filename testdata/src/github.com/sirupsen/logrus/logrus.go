// Package logrus is a stub of github.com/sirupsen/logrus for the tests.
package logrus

type Level uint32

const (
	PanicLevel Level = iota
	FatalLevel
	ErrorLevel
	WarnLevel
	InfoLevel
	DebugLevel
	TraceLevel
)

type Fields map[string]any

type Logger struct{}

type Entry struct{ Data Fields }

func New() *Logger                                 { return &Logger{} }
func WithError(err error) *Entry                   { return &Entry{} }
func WithField(key string, value any) *Entry       { return &Entry{} }
func Error(args ...any)                            {}
func Errorf(format string, args ...any)            {}
func Debug(args ...any)                            {}
func (l *Logger) WithError(err error) *Entry       { return &Entry{} }
func (l *Logger) Warnf(format string, args ...any) {}
func (e *Entry) Error(args ...any)                 {}
func (e *Entry) Debug(args ...any)                 {}
func (e *Entry) Log(level Level, args ...any)      {}
