// Package zap is a stub of go.uber.org/zap for the tests.
package zap

type Field struct {
	Key       string
	Interface any
}

type Level int8

const (
	DebugLevel Level = -1
	InfoLevel  Level = 0
	WarnLevel  Level = 1
	ErrorLevel Level = 2
	FatalLevel Level = 5
)

type Logger struct{}

type SugaredLogger struct{}

func L() *Logger                                             { return &Logger{} }
func Error(err error) Field                                  { return Field{Key: "error", Interface: err} }
func String(key, val string) Field                           { return Field{Key: key, Interface: val} }
func (l *Logger) With(fields ...Field) *Logger               { return l }
func (l *Logger) Debug(msg string, fields ...Field)          {}
func (l *Logger) Info(msg string, fields ...Field)           {}
func (l *Logger) Error(msg string, fields ...Field)          {}
func (l *Logger) Fatal(msg string, fields ...Field)          {}
func (l *Logger) Log(lvl Level, msg string, fields ...Field) {}
func (l *Logger) Sugar() *SugaredLogger                      { return &SugaredLogger{} }
func (s *SugaredLogger) Debugw(msg string, kv ...any)        {}
func (s *SugaredLogger) Errorw(msg string, kv ...any)        {}
func (s *SugaredLogger) Errorf(format string, a ...any)      {}
func (s *SugaredLogger) Log(lvl Level, args ...any)          {}
