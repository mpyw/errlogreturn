// Package log is a stub of github.com/rs/zerolog/log for the tests.
package log

import "github.com/rs/zerolog"

var Logger zerolog.Logger

func Debug() *zerolog.Event        { return Logger.Debug() }
func Info() *zerolog.Event         { return Logger.Info() }
func Error() *zerolog.Event        { return Logger.Error() }
func Err(err error) *zerolog.Event { return Logger.Err(err) }
