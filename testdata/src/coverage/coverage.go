// Package coverage holds the shapes that reach the analysis's less common
// branches, each with what it must report.
package coverage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"

	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
	"go.uber.org/zap"
)

func do() error { return errors.New("boom") }

func more() bool { return true }

// ===== Loggers =====

func loggerOutput(l *log.Logger) error {
	err := do()
	l.Output(2, err.Error()) // want `error is logged here`
	return err
}

func slogLog(ctx context.Context) error {
	err := do()
	slog.Log(ctx, slog.LevelWarn, "failed", "err", err) // want `error is logged here`
	return err
}

func slogLogAttrs(ctx context.Context) error {
	err := do()
	slog.LogAttrs(ctx, slog.LevelError, "failed", slog.Any("err", err)) // want `error is logged here`
	return err
}

func sugaredLog(l *zap.SugaredLogger) error {
	err := do()
	l.Log(zap.ErrorLevel, err) // want `error is logged here`
	return err
}

// A logrus type that is not a logger does not log.
func logrusOther(f *logrus.TextFormatter) error {
	err := do()
	f.Error(err)
	return err
}

// An event chosen on a branch logs when both are events that count.
func eventOnBranch(l *zerolog.Logger, warn bool) error {
	err := do()
	var e *zerolog.Event
	if warn {
		e = l.Warn()
	} else {
		e = l.Error()
	}
	e.Err(err).Msg("failed") // want `error is logged here`
	return err
}

func eventOnBranchDebug(l *zerolog.Logger, warn bool) error {
	err := do()
	var e *zerolog.Event
	if warn {
		e = l.Warn()
	} else {
		e = l.Debug()
	}
	e.Err(err).Msg("failed")
	return err
}

// An event built up in a loop reaches its producer round the loop.
func eventInLoop(l *zerolog.Logger, keys []string) error {
	err := do()
	e := l.Error()
	for _, k := range keys {
		e = e.Str(k, k)
	}
	e.Err(err).Msg("failed") // want `error is logged here`
	return err
}

// An event from a function value has no producer to read.
func eventFromValue(mk func() *zerolog.Event) error {
	err := do()
	mk().Err(err).Msg("failed")
	return err
}

// fmt writes to a writer read from memory are not a standard stream.
func fprintToLoaded(w *io.Writer) error {
	err := do()
	fmt.Fprintln(*w, err)
	return err
}

// ===== Values =====

// A received error is its own origin.
func received(ch chan error) error {
	err := <-ch
	log.Println(err) // want `error is logged here`
	return err
}

type pathError struct{ path string }

func (e *pathError) Error() string { return e.path } // want Error:"carries p0.0→r0"

// A method value of a pointer is checked for nil, which the call carries.
func boundMethod(pe *pathError) error { // want boundMethod:`^carries p0→r0, logs p0$`
	fn := pe.Error
	log.Println(fn()) // want `error is logged here`
	return pe
}

// An array element and a converted slice.
func arrayElement(a [2]error) error { // want arrayElement:`^carries p0\.\[0\]→r0$`
	log.Println(a[0]) // want `error is logged here`
	return a[0]
}

func sliceToArray(s []error) error { // want sliceToArray:`^carries p0→r0, logs p0$`
	p := (*[1]error)(s)
	log.Println(p[0]) // want `error is logged here`
	return p[0]
}

type pair struct {
	id  string
	err error
}

func pairs() [1]pair { return [1]pair{{id: "a", err: do()}} }

// A field of an element of what a call returned is not traced through the
// call.
func fieldOfCallElement() error {
	err := do()
	slog.Info("pair", "id", pairs()[0].id)
	return err
}

func loadedArray(p *[2]pair) error { // want loadedArray:`^carries p0\.\[0\]\.1→r0$`
	log.Println(p[0].err) // want `error is logged here`
	return p[0].err
}

// A field of a received struct is traced through the receive.
func receivedField(ch chan pair) error {
	r := <-ch
	log.Println(r.err) // want `error is logged here`
	return r.err
}

type nested struct{ inner pair }

func mkNested() nested { return nested{} }

// A field of a call's field, copied into memory, is not traced through the
// call.
func copiedCallField() error {
	err := do()
	var n pair
	n = mkNested().inner
	slog.Info("pair", "id", n.id)
	return err
}

// ===== Conditions =====

// Two logs in one function read one set of branch reads.
func twoLogs(c bool) error {
	err := do()
	if c {
		log.Println(err) // want `error is logged here`
	} else {
		slog.Error("failed", "err", err) // want `error is logged here`
	}
	return err
}

// A negated condition read after the log.
func negated(ready bool) error {
	err := do()
	notReady := !ready
	if notReady {
		log.Println(err)
	}
	if !notReady {
		return err
	}
	return nil
}

// A counter converted, and one on the right of an addition.
func convertedCounter(n int64) error {
	var err error
	for i := int32(0); int64(i) < n; i++ {
		err = do()
		if int64(i) < n-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

func constantPlusCounter(n int) error {
	var err error
	for i := 0; 1+i < n; i++ {
		err = do()
		if 1+i < n-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

type attempt int

func namedCounter(n attempt) error {
	var err error
	for i := attempt(0); i < n; i++ {
		err = do()
		if i < n-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

// ===== Deferred clears =====

// The clear runs before the log, in a block that dominates it.
func deferClearsFirst() (err error) {
	defer func() {
		e := err
		err = nil
		if e != nil {
			slog.Error("failed", "err", e)
		}
	}()
	return do()
}

// The wrap reads err through another local.
func deferWrapsThroughLocal() (err error) {
	defer func() { // want `error is logged by a function literal`
		if err != nil {
			log.Println(err)
			x := err
			p := &x
			err = fmt.Errorf("wrapped: %v", *p)
		}
	}()
	return do()
}

// A replacement that does not read err, after a branch.
func deferReplacesAfterBranch(c bool) (err error) {
	defer func() {
		if err != nil {
			log.Println(err)
			if c {
				_ = 1
			} else {
				_ = 2
			}
			err = fmt.Errorf("replaced %d", 1)
		}
	}()
	return do()
}

// ===== Helpers =====

// A nil test written with nil first.
func logIfErrNilFirst(err error) { // want logIfErrNilFirst:"logs p0"
	if nil != err {
		log.Println(err)
	}
}

func callsNilFirst() error {
	err := do()
	logIfErrNilFirst(err) // want `error is logged by logIfErrNilFirst`
	return err
}

type reporter interface{ Error() string }

// A nil test of an error seen as a narrower interface.
func logIfReporter(err error) { // want logIfReporter:"logs p0"
	var r reporter = err
	if r != nil {
		log.Println(r)
	}
}

// A nil test of something received is not a test of the input.
func logReceived(ch chan error) {
	if e := <-ch; e != nil {
		log.Println(e)
	}
}

// A getter reading more fields than a summary keeps carries its receiver.
type wide struct{ a, b, c, d, e, f, g, h, i string }

func (w *wide) all() string { // want all:"carries p0→r0"
	return w.a + w.b + w.c + w.d + w.e + w.f + w.g + w.h + w.i
}

// A write to a constant index is a path of its own.
func setFirst(p *[2]error) { p[0] = do() } // want setFirst:`^writes p0\.\[0\]$`

//errlogreturn:sink
func report(v any) { // want report:"sink"
	_ = v
}

func declaredSinkHere() error {
	err := do()
	report(err) // want `error is logged here`
	return err
}

// Each iteration receives a new struct. The one logged is not the one the
// next iteration returns.
func receivedInLoop(ch chan pair) error {
	for {
		r := <-ch
		if r.id == "" {
			return r.err
		}
		log.Println(r.err)
	}
}

// ===== Bounds on the walk =====

// Past checkMaxSteps blocks the walk is given up: fifteen flags read twice
// after the log make more paths than that, and no path returns err.
func manyPaths(c0, c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11, c12, c13, c14 bool) error {
	err := do()
	log.Println(err)
	if c0 {
		g()
	}
	if c1 {
		g()
	}
	if c2 {
		g()
	}
	if c3 {
		g()
	}
	if c4 {
		g()
	}
	if c5 {
		g()
	}
	if c6 {
		g()
	}
	if c7 {
		g()
	}
	if c8 {
		g()
	}
	if c9 {
		g()
	}
	if c10 {
		g()
	}
	if c11 {
		g()
	}
	if c12 {
		g()
	}
	if c13 {
		g()
	}
	if c14 {
		g()
	}
	if c0 {
		g()
	}
	if c1 {
		g()
	}
	if c2 {
		g()
	}
	if c3 {
		g()
	}
	if c4 {
		g()
	}
	if c5 {
		g()
	}
	if c6 {
		g()
	}
	if c7 {
		g()
	}
	if c8 {
		g()
	}
	if c9 {
		g()
	}
	if c10 {
		g()
	}
	if c11 {
		g()
	}
	if c12 {
		g()
	}
	if c13 {
		g()
	}
	if c14 {
		g()
	}
	return nil
}

func g() {}

// More decided conditions at the log than a walk carries: the walk reads the
// log's own block and stops.
func manyAtLog(c0, c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11, c12, c13, c14, c15, c16, c17, c18, c19, c20, c21, c22, c23, c24, c25, c26, c27, c28, c29, c30, c31, c32 bool) error {
	err := do()
	if c0 {
		if c1 {
			if c2 {
				if c3 {
					if c4 {
						if c5 {
							if c6 {
								if c7 {
									if c8 {
										if c9 {
											if c10 {
												if c11 {
													if c12 {
														if c13 {
															if c14 {
																if c15 {
																	if c16 {
																		if c17 {
																			if c18 {
																				if c19 {
																					if c20 {
																						if c21 {
																							if c22 {
																								if c23 {
																									if c24 {
																										if c25 {
																											if c26 {
																												if c27 {
																													if c28 {
																														if c29 {
																															if c30 {
																																if c31 {
																																	if c32 {
																																		log.Println(err)
																																	}
																																}
																															}
																														}
																													}
																												}
																											}
																										}
																									}
																								}
																							}
																						}
																					}
																				}
																			}
																		}
																	}
																}
															}
														}
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if c0 {
		g()
	}
	if c1 {
		g()
	}
	if c2 {
		g()
	}
	if c3 {
		g()
	}
	if c4 {
		g()
	}
	if c5 {
		g()
	}
	if c6 {
		g()
	}
	if c7 {
		g()
	}
	if c8 {
		g()
	}
	if c9 {
		g()
	}
	if c10 {
		g()
	}
	if c11 {
		g()
	}
	if c12 {
		g()
	}
	if c13 {
		g()
	}
	if c14 {
		g()
	}
	if c15 {
		g()
	}
	if c16 {
		g()
	}
	if c17 {
		g()
	}
	if c18 {
		g()
	}
	if c19 {
		g()
	}
	if c20 {
		g()
	}
	if c21 {
		g()
	}
	if c22 {
		g()
	}
	if c23 {
		g()
	}
	if c24 {
		g()
	}
	if c25 {
		g()
	}
	if c26 {
		g()
	}
	if c27 {
		g()
	}
	if c28 {
		g()
	}
	if c29 {
		g()
	}
	if c30 {
		g()
	}
	if c31 {
		g()
	}
	if c32 {
		g()
	}
	return err
}

// Conditions decided after the log pile up past the bound. The walk that
// records them gives up, and the one that does not reaches the return.
func manyAfterLog(c0, c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11, c12, c13, c14, c15, c16, c17, c18, c19, c20, c21, c22, c23, c24, c25, c26, c27, c28, c29, c30, c31, c32 bool) error {
	err := do()
	log.Println(err) // want `error is logged here`
	if c0 {
		g()
	}
	if c1 {
		g()
	}
	if c2 {
		g()
	}
	if c3 {
		g()
	}
	if c4 {
		g()
	}
	if c5 {
		g()
	}
	if c6 {
		g()
	}
	if c7 {
		g()
	}
	if c8 {
		g()
	}
	if c9 {
		g()
	}
	if c10 {
		g()
	}
	if c11 {
		g()
	}
	if c12 {
		g()
	}
	if c13 {
		g()
	}
	if c14 {
		g()
	}
	if c15 {
		g()
	}
	if c16 {
		g()
	}
	if c17 {
		g()
	}
	if c18 {
		g()
	}
	if c19 {
		g()
	}
	if c20 {
		g()
	}
	if c21 {
		g()
	}
	if c22 {
		g()
	}
	if c23 {
		g()
	}
	if c24 {
		g()
	}
	if c25 {
		g()
	}
	if c26 {
		g()
	}
	if c27 {
		g()
	}
	if c28 {
		g()
	}
	if c29 {
		g()
	}
	if c30 {
		g()
	}
	if c31 {
		g()
	}
	if c32 {
		g()
	}
	if c0 {
		g()
	}
	if c1 {
		g()
	}
	if c2 {
		g()
	}
	if c3 {
		g()
	}
	if c4 {
		g()
	}
	if c5 {
		g()
	}
	if c6 {
		g()
	}
	if c7 {
		g()
	}
	if c8 {
		g()
	}
	if c9 {
		g()
	}
	if c10 {
		g()
	}
	if c11 {
		g()
	}
	if c12 {
		g()
	}
	if c13 {
		g()
	}
	if c14 {
		g()
	}
	if c15 {
		g()
	}
	if c16 {
		g()
	}
	if c17 {
		g()
	}
	if c18 {
		g()
	}
	if c19 {
		g()
	}
	if c20 {
		g()
	}
	if c21 {
		g()
	}
	if c22 {
		g()
	}
	if c23 {
		g()
	}
	if c24 {
		g()
	}
	if c25 {
		g()
	}
	if c26 {
		g()
	}
	if c27 {
		g()
	}
	if c28 {
		g()
	}
	if c29 {
		g()
	}
	if c30 {
		g()
	}
	if c31 {
		g()
	}
	if c32 {
		g()
	}
	return err
}

// A condition built from more φ-nodes than checkKeys reads is kept wherever
// the walk goes, and still decides the return.
func deepCondition(mode int, c0, c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11, c12, c13, c14, c15, c16, c17 bool) error {
	err := do()
	ok := mode == 1
	if ok {
		log.Println(err)
	}
	if !c0 {
		ok = true
	}
	if !c1 {
		ok = true
	}
	if !c2 {
		ok = true
	}
	if !c3 {
		ok = true
	}
	if !c4 {
		ok = true
	}
	if !c5 {
		ok = true
	}
	if !c6 {
		ok = true
	}
	if !c7 {
		ok = true
	}
	if !c8 {
		ok = true
	}
	if !c9 {
		ok = true
	}
	if !c10 {
		ok = true
	}
	if !c11 {
		ok = true
	}
	if !c12 {
		ok = true
	}
	if !c13 {
		ok = true
	}
	if !c14 {
		ok = true
	}
	if !c15 {
		ok = true
	}
	if !c16 {
		ok = true
	}
	if !c17 {
		ok = true
	}
	if ok {
		return nil
	}
	return err
}

// A deferred replacement computed from more values than checkReadsVar reads
// is taken to carry err.
func deferLongReplacement(x int) (err error) {
	defer func() { // want `error is logged by a function literal`
		if err != nil {
			log.Println(err)
			k := x
			k = k*3 + 1
			k = k*3 + 2
			k = k*3 + 3
			k = k*3 + 4
			k = k*3 + 5
			k = k*3 + 6
			k = k*3 + 7
			k = k*3 + 8
			k = k*3 + 9
			k = k*3 + 10
			k = k*3 + 11
			k = k*3 + 12
			k = k*3 + 13
			k = k*3 + 14
			k = k*3 + 15
			k = k*3 + 16
			k = k*3 + 17
			k = k*3 + 18
			k = k*3 + 19
			k = k*3 + 20
			k = k*3 + 21
			k = k*3 + 22
			k = k*3 + 23
			k = k*3 + 24
			k = k*3 + 25
			k = k*3 + 26
			k = k*3 + 27
			k = k*3 + 28
			k = k*3 + 29
			k = k*3 + 30
			k = k*3 + 31
			k = k*3 + 32
			k = k*3 + 33
			k = k*3 + 34
			k = k*3 + 35
			k = k*3 + 36
			k = k*3 + 37
			k = k*3 + 38
			k = k*3 + 39
			k = k*3 + 40
			k = k*3 + 41
			k = k*3 + 42
			k = k*3 + 43
			k = k*3 + 44
			k = k*3 + 45
			k = k*3 + 46
			k = k*3 + 47
			k = k*3 + 48
			k = k*3 + 49
			k = k*3 + 50
			k = k*3 + 51
			k = k*3 + 52
			k = k*3 + 53
			k = k*3 + 54
			k = k*3 + 55
			k = k*3 + 56
			k = k*3 + 57
			k = k*3 + 58
			k = k*3 + 59
			k = k*3 + 60
			k = k*3 + 61
			k = k*3 + 62
			k = k*3 + 63
			k = k*3 + 64
			k = k*3 + 65
			k = k*3 + 66
			k = k*3 + 67
			k = k*3 + 68
			k = k*3 + 69
			k = k*3 + 70
			k = k*3 + 71
			k = k*3 + 72
			k = k*3 + 73
			k = k*3 + 74
			k = k*3 + 75
			k = k*3 + 76
			k = k*3 + 77
			k = k*3 + 78
			k = k*3 + 79
			k = k*3 + 80
			k = k*3 + 81
			k = k*3 + 82
			k = k*3 + 83
			k = k*3 + 84
			k = k*3 + 85
			k = k*3 + 86
			k = k*3 + 87
			k = k*3 + 88
			k = k*3 + 89
			k = k*3 + 90
			k = k*3 + 91
			k = k*3 + 92
			k = k*3 + 93
			k = k*3 + 94
			k = k*3 + 95
			k = k*3 + 96
			k = k*3 + 97
			k = k*3 + 98
			k = k*3 + 99
			k = k*3 + 100
			k = k*3 + 101
			k = k*3 + 102
			k = k*3 + 103
			k = k*3 + 104
			k = k*3 + 105
			k = k*3 + 106
			k = k*3 + 107
			k = k*3 + 108
			k = k*3 + 109
			k = k*3 + 110
			k = k*3 + 111
			k = k*3 + 112
			k = k*3 + 113
			k = k*3 + 114
			k = k*3 + 115
			k = k*3 + 116
			k = k*3 + 117
			k = k*3 + 118
			k = k*3 + 119
			k = k*3 + 120
			k = k*3 + 121
			k = k*3 + 122
			k = k*3 + 123
			k = k*3 + 124
			k = k*3 + 125
			k = k*3 + 126
			k = k*3 + 127
			k = k*3 + 128
			k = k*3 + 129
			k = k*3 + 130
			k = k*3 + 131
			k = k*3 + 132
			k = k*3 + 133
			k = k*3 + 134
			k = k*3 + 135
			k = k*3 + 136
			k = k*3 + 137
			k = k*3 + 138
			k = k*3 + 139
			k = k*3 + 140
			k = k*3 + 141
			k = k*3 + 142
			k = k*3 + 143
			k = k*3 + 144
			k = k*3 + 145
			k = k*3 + 146
			k = k*3 + 147
			k = k*3 + 148
			k = k*3 + 149
			k = k*3 + 150
			k = k*3 + 151
			k = k*3 + 152
			k = k*3 + 153
			k = k*3 + 154
			k = k*3 + 155
			k = k*3 + 156
			k = k*3 + 157
			k = k*3 + 158
			k = k*3 + 159
			k = k*3 + 160
			k = k*3 + 161
			k = k*3 + 162
			k = k*3 + 163
			k = k*3 + 164
			k = k*3 + 165
			k = k*3 + 166
			k = k*3 + 167
			k = k*3 + 168
			k = k*3 + 169
			k = k*3 + 170
			k = k*3 + 171
			k = k*3 + 172
			k = k*3 + 173
			k = k*3 + 174
			k = k*3 + 175
			k = k*3 + 176
			k = k*3 + 177
			k = k*3 + 178
			k = k*3 + 179
			k = k*3 + 180
			k = k*3 + 181
			k = k*3 + 182
			k = k*3 + 183
			k = k*3 + 184
			k = k*3 + 185
			k = k*3 + 186
			k = k*3 + 187
			k = k*3 + 188
			k = k*3 + 189
			k = k*3 + 190
			k = k*3 + 191
			k = k*3 + 192
			k = k*3 + 193
			k = k*3 + 194
			k = k*3 + 195
			k = k*3 + 196
			k = k*3 + 197
			k = k*3 + 198
			k = k*3 + 199
			k = k*3 + 200
			k = k*3 + 201
			k = k*3 + 202
			k = k*3 + 203
			k = k*3 + 204
			k = k*3 + 205
			k = k*3 + 206
			k = k*3 + 207
			k = k*3 + 208
			k = k*3 + 209
			k = k*3 + 210
			k = k*3 + 211
			k = k*3 + 212
			k = k*3 + 213
			k = k*3 + 214
			k = k*3 + 215
			k = k*3 + 216
			k = k*3 + 217
			k = k*3 + 218
			k = k*3 + 219
			k = k*3 + 220
			k = k*3 + 221
			k = k*3 + 222
			k = k*3 + 223
			k = k*3 + 224
			k = k*3 + 225
			k = k*3 + 226
			k = k*3 + 227
			k = k*3 + 228
			k = k*3 + 229
			k = k*3 + 230
			k = k*3 + 231
			k = k*3 + 232
			k = k*3 + 233
			k = k*3 + 234
			k = k*3 + 235
			k = k*3 + 236
			k = k*3 + 237
			k = k*3 + 238
			k = k*3 + 239
			k = k*3 + 240
			k = k*3 + 241
			k = k*3 + 242
			k = k*3 + 243
			k = k*3 + 244
			k = k*3 + 245
			k = k*3 + 246
			k = k*3 + 247
			k = k*3 + 248
			k = k*3 + 249
			k = k*3 + 250
			k = k*3 + 251
			k = k*3 + 252
			k = k*3 + 253
			k = k*3 + 254
			k = k*3 + 255
			k = k*3 + 256
			k = k*3 + 257
			k = k*3 + 258
			k = k*3 + 259
			k = k*3 + 260
			k = k*3 + 261
			k = k*3 + 262
			k = k*3 + 263
			k = k*3 + 264
			k = k*3 + 265
			k = k*3 + 266
			k = k*3 + 267
			k = k*3 + 268
			k = k*3 + 269
			k = k*3 + 270
			k = k*3 + 271
			k = k*3 + 272
			k = k*3 + 273
			k = k*3 + 274
			k = k*3 + 275
			k = k*3 + 276
			k = k*3 + 277
			k = k*3 + 278
			k = k*3 + 279
			k = k*3 + 280
			k = k*3 + 281
			k = k*3 + 282
			k = k*3 + 283
			k = k*3 + 284
			k = k*3 + 285
			k = k*3 + 286
			k = k*3 + 287
			k = k*3 + 288
			k = k*3 + 289
			k = k*3 + 290
			k = k*3 + 291
			k = k*3 + 292
			k = k*3 + 293
			k = k*3 + 294
			k = k*3 + 295
			k = k*3 + 296
			k = k*3 + 297
			k = k*3 + 298
			k = k*3 + 299
			k = k*3 + 300
			err = fmt.Errorf("replaced %d", k)
		}
	}()
	return do()
}

// A decided condition computed from more values than checkDependsOn reads is
// forgotten on entering the next block.
func longCondition(x int) error {
	err := do()
	k := x
	k = k*3 + 1
	k = k*3 + 2
	k = k*3 + 3
	k = k*3 + 4
	k = k*3 + 5
	k = k*3 + 6
	k = k*3 + 7
	k = k*3 + 8
	k = k*3 + 9
	k = k*3 + 10
	k = k*3 + 11
	k = k*3 + 12
	k = k*3 + 13
	k = k*3 + 14
	k = k*3 + 15
	k = k*3 + 16
	k = k*3 + 17
	k = k*3 + 18
	k = k*3 + 19
	k = k*3 + 20
	k = k*3 + 21
	k = k*3 + 22
	k = k*3 + 23
	k = k*3 + 24
	k = k*3 + 25
	k = k*3 + 26
	k = k*3 + 27
	k = k*3 + 28
	k = k*3 + 29
	k = k*3 + 30
	k = k*3 + 31
	k = k*3 + 32
	k = k*3 + 33
	k = k*3 + 34
	k = k*3 + 35
	k = k*3 + 36
	k = k*3 + 37
	k = k*3 + 38
	k = k*3 + 39
	k = k*3 + 40
	k = k*3 + 41
	k = k*3 + 42
	k = k*3 + 43
	k = k*3 + 44
	k = k*3 + 45
	k = k*3 + 46
	k = k*3 + 47
	k = k*3 + 48
	k = k*3 + 49
	k = k*3 + 50
	k = k*3 + 51
	k = k*3 + 52
	k = k*3 + 53
	k = k*3 + 54
	k = k*3 + 55
	k = k*3 + 56
	k = k*3 + 57
	k = k*3 + 58
	k = k*3 + 59
	k = k*3 + 60
	k = k*3 + 61
	k = k*3 + 62
	k = k*3 + 63
	k = k*3 + 64
	k = k*3 + 65
	k = k*3 + 66
	k = k*3 + 67
	k = k*3 + 68
	k = k*3 + 69
	k = k*3 + 70
	k = k*3 + 71
	k = k*3 + 72
	k = k*3 + 73
	k = k*3 + 74
	k = k*3 + 75
	k = k*3 + 76
	k = k*3 + 77
	k = k*3 + 78
	k = k*3 + 79
	k = k*3 + 80
	k = k*3 + 81
	k = k*3 + 82
	k = k*3 + 83
	k = k*3 + 84
	k = k*3 + 85
	k = k*3 + 86
	k = k*3 + 87
	k = k*3 + 88
	k = k*3 + 89
	k = k*3 + 90
	k = k*3 + 91
	k = k*3 + 92
	k = k*3 + 93
	k = k*3 + 94
	k = k*3 + 95
	k = k*3 + 96
	k = k*3 + 97
	k = k*3 + 98
	k = k*3 + 99
	k = k*3 + 100
	k = k*3 + 101
	k = k*3 + 102
	k = k*3 + 103
	k = k*3 + 104
	k = k*3 + 105
	k = k*3 + 106
	k = k*3 + 107
	k = k*3 + 108
	k = k*3 + 109
	k = k*3 + 110
	k = k*3 + 111
	k = k*3 + 112
	k = k*3 + 113
	k = k*3 + 114
	k = k*3 + 115
	k = k*3 + 116
	k = k*3 + 117
	k = k*3 + 118
	k = k*3 + 119
	k = k*3 + 120
	k = k*3 + 121
	k = k*3 + 122
	k = k*3 + 123
	k = k*3 + 124
	k = k*3 + 125
	k = k*3 + 126
	k = k*3 + 127
	k = k*3 + 128
	k = k*3 + 129
	k = k*3 + 130
	k = k*3 + 131
	k = k*3 + 132
	k = k*3 + 133
	k = k*3 + 134
	k = k*3 + 135
	k = k*3 + 136
	k = k*3 + 137
	k = k*3 + 138
	k = k*3 + 139
	k = k*3 + 140
	k = k*3 + 141
	k = k*3 + 142
	k = k*3 + 143
	k = k*3 + 144
	k = k*3 + 145
	k = k*3 + 146
	k = k*3 + 147
	k = k*3 + 148
	k = k*3 + 149
	k = k*3 + 150
	k = k*3 + 151
	k = k*3 + 152
	k = k*3 + 153
	k = k*3 + 154
	k = k*3 + 155
	k = k*3 + 156
	k = k*3 + 157
	k = k*3 + 158
	k = k*3 + 159
	k = k*3 + 160
	k = k*3 + 161
	k = k*3 + 162
	k = k*3 + 163
	k = k*3 + 164
	k = k*3 + 165
	k = k*3 + 166
	k = k*3 + 167
	k = k*3 + 168
	k = k*3 + 169
	k = k*3 + 170
	k = k*3 + 171
	k = k*3 + 172
	k = k*3 + 173
	k = k*3 + 174
	k = k*3 + 175
	k = k*3 + 176
	k = k*3 + 177
	k = k*3 + 178
	k = k*3 + 179
	k = k*3 + 180
	k = k*3 + 181
	k = k*3 + 182
	k = k*3 + 183
	k = k*3 + 184
	k = k*3 + 185
	k = k*3 + 186
	k = k*3 + 187
	k = k*3 + 188
	k = k*3 + 189
	k = k*3 + 190
	k = k*3 + 191
	k = k*3 + 192
	k = k*3 + 193
	k = k*3 + 194
	k = k*3 + 195
	k = k*3 + 196
	k = k*3 + 197
	k = k*3 + 198
	k = k*3 + 199
	k = k*3 + 200
	k = k*3 + 201
	k = k*3 + 202
	k = k*3 + 203
	k = k*3 + 204
	k = k*3 + 205
	k = k*3 + 206
	k = k*3 + 207
	k = k*3 + 208
	k = k*3 + 209
	k = k*3 + 210
	k = k*3 + 211
	k = k*3 + 212
	k = k*3 + 213
	k = k*3 + 214
	k = k*3 + 215
	k = k*3 + 216
	k = k*3 + 217
	k = k*3 + 218
	k = k*3 + 219
	k = k*3 + 220
	k = k*3 + 221
	k = k*3 + 222
	k = k*3 + 223
	k = k*3 + 224
	k = k*3 + 225
	k = k*3 + 226
	k = k*3 + 227
	k = k*3 + 228
	k = k*3 + 229
	k = k*3 + 230
	k = k*3 + 231
	k = k*3 + 232
	k = k*3 + 233
	k = k*3 + 234
	k = k*3 + 235
	k = k*3 + 236
	k = k*3 + 237
	k = k*3 + 238
	k = k*3 + 239
	k = k*3 + 240
	k = k*3 + 241
	k = k*3 + 242
	k = k*3 + 243
	k = k*3 + 244
	k = k*3 + 245
	k = k*3 + 246
	k = k*3 + 247
	k = k*3 + 248
	k = k*3 + 249
	k = k*3 + 250
	k = k*3 + 251
	k = k*3 + 252
	k = k*3 + 253
	k = k*3 + 254
	k = k*3 + 255
	k = k*3 + 256
	k = k*3 + 257
	k = k*3 + 258
	k = k*3 + 259
	k = k*3 + 260
	k = k*3 + 261
	k = k*3 + 262
	k = k*3 + 263
	k = k*3 + 264
	k = k*3 + 265
	k = k*3 + 266
	k = k*3 + 267
	k = k*3 + 268
	k = k*3 + 269
	k = k*3 + 270
	k = k*3 + 271
	k = k*3 + 272
	k = k*3 + 273
	k = k*3 + 274
	k = k*3 + 275
	k = k*3 + 276
	k = k*3 + 277
	k = k*3 + 278
	k = k*3 + 279
	k = k*3 + 280
	k = k*3 + 281
	k = k*3 + 282
	k = k*3 + 283
	k = k*3 + 284
	k = k*3 + 285
	k = k*3 + 286
	k = k*3 + 287
	k = k*3 + 288
	k = k*3 + 289
	k = k*3 + 290
	k = k*3 + 291
	k = k*3 + 292
	k = k*3 + 293
	k = k*3 + 294
	k = k*3 + 295
	k = k*3 + 296
	k = k*3 + 297
	k = k*3 + 298
	k = k*3 + 299
	k = k*3 + 300
	if k > 5 {
		log.Println(err) // want `error is logged here`
	}
	g()
	return err
}

// Results past the 64 a summary tracks are not tracked.
func manyResults() (error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error, error) {
	return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil
}

// ===== More shapes =====

func valueError() pathValue { return pathValue{} }

type pathValue struct{ path string }

func (e pathValue) Error() string { return e.path } // want Error:`^carries p0\.0→r0$`

// A method value of a value method taken from a pointer checks it for nil.
func boundValueMethod(pe *pathValue) error { // want boundValueMethod:`^carries p0→r0, logs p0$`
	fn := pe.Error
	log.Println(fn()) // want `error is logged here`
	return pe
}

// A clobber in a loop: the closure assigns err again in the next iteration.
func clobberInLoop(c func() bool) error {
	var err error
	try := func() { err = do() }
	for {
		try()
		if c() {
			return err
		}
		log.Println(err)
	}
}

func h() {}

// The replacement comes after both sides of a branch that call something.
func deferReplacesAfterCalls(c bool) (err error) {
	defer func() {
		if err != nil {
			log.Println(err)
			if c {
				g()
			} else {
				h()
			}
			err = fmt.Errorf("replaced %d", 1)
		}
	}()
	return do()
}

// A guard that converts the counter between named types.
func changedCounter(m int) error {
	var err error
	for i := attempt(0); int(i) < m; i++ {
		err = do()
		if int(i) < m-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

// A received struct, read without going through memory.
func receivedDirect(ch chan pair) error {
	log.Println((<-ch).err)
	return (<-ch).err
}

func receivedArray(ch chan [2]error) error {
	a := <-ch
	log.Println(a[0]) // want `error is logged here`
	return a[0]
}

// A field of a value looked up with ok.
func lookedUpWithOk(m map[string]pair) error { // want lookedUpWithOk:`^carries p0\.\[\]\.1→r0$`
	v, ok := m["a"]
	if !ok {
		return nil
	}
	log.Println(v.err) // want `error is logged here`
	return v.err
}

// A write through a received pointer.
func writeReceived(ch chan *error) {
	p := <-ch
	*p = nil
}

// Nil tests of an input converted to an interface.
func logConcrete(e *pathError) { // want logConcrete:"logs p0"
	if error(e) != nil {
		log.Println(e)
	}
}

func logAsAny(err error) { // want logAsAny:"logs p0"
	if any(err) != nil {
		log.Println(err)
	}
}

// A decided condition read again through a negation, and compared with a
// boolean constant.
func negationDecided(c bool) error {
	err := do()
	if c {
		log.Println(err)
	}
	nc := !c
	if nc {
		return err
	}
	return nil
}

func comparedWithFalse(flag bool) error {
	err := do()
	if flag {
		log.Println(err)
	}
	if flag == false {
		return err
	}
	return nil
}

// A method expression of a value method on a pointer calls a wrapper that
// checks the pointer for nil.
func methodExpression(pe *pathValue) error { // want methodExpression:`^carries p0→r0, logs p0$`
	log.Println((*pathValue).Error(pe)) // want `error is logged here`
	return pe
}

// An element of a received array, read without going through memory.
func receivedElement(ch chan [2]error) error {
	log.Println((<-ch)[0])
	return nil
}
