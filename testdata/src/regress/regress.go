// Package regress holds the shapes a review of the analysis found reported
// wrongly, and the reports each fix must keep.
package regress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func do() error { return errors.New("boom") }

// ===== A field is read on its own =====

type job struct {
	id     string
	err    error
	logger *slog.Logger
}

// Logging j.id says nothing about the error stored into j.err.
func (j *job) logsOtherField() error { // want logsOtherField:`^writes p0\.1$`
	slog.Info("job started", "id", j.id)
	err := do()
	j.err = err
	return err
}

func (j *job) logsThroughField() error { // want logsThroughField:`^writes p0\.1$`
	j.logger.Info("starting")
	err := do()
	j.err = err
	return err
}

func (j *job) statusFromError() error { // want statusFromError:`^writes p0\.0$`
	slog.Info("job started", "id", j.id)
	err := do()
	j.id = "failed: " + err.Error()
	return err
}

func (j *job) logsTheField() error { // want logsTheField:`^writes p0\.1$`
	j.err = do()
	slog.Error("failed", "err", j.err) // want `error is logged here`
	return j.err
}

func (j *job) fieldReassigned() error { // want fieldReassigned:`^writes p0\.1$`
	j.err = do()
	slog.Error("failed", "err", j.err)
	j.err = do()
	return j.err
}

type state struct {
	name string
	err  error
}

func literalField() error {
	st := state{name: "x", err: do()}
	slog.Info("state", "name", st.name)
	return st.err
}

func literalFieldLogged() error {
	st := state{name: "x", err: do()}
	slog.Error("state", "err", st.err) // want `error is logged here`
	return st.err
}

// ===== A comparison or a predicate is logged =====

func okFlag(l *slog.Logger) (err error) {
	defer func() { l.Info("request", "ok", err == nil) }()
	return do()
}

func failedFlag() error {
	err := do()
	slog.Info("call", "failed", err != nil)
	return err
}

func isCanceled() error {
	err := do()
	slog.Warn("stop", "canceled", errors.Is(err, context.Canceled))
	return err
}

func containsTimeout() error {
	err := do()
	slog.Info("call", "timeout", strings.Contains(err.Error(), "timeout"))
	return err
}

func messageLength() error {
	err := do()
	slog.Info("call", "n", len(err.Error()))
	return err
}

// ===== fmt's own errors =====

func writeError(w io.Writer) error {
	err := do()
	log.Printf("%v", err)
	_, werr := fmt.Fprintf(w, "error: %v", err)
	return werr
}

func errorfCarries() error {
	err := do()
	log.Println(err) // want `error is logged here`
	return fmt.Errorf("x: %w", err)
}

// ===== The variable is assigned again after the log =====

func closureAssigns() error {
	var err error
	try := func() { err = do() }
	try()
	log.Println(err)
	try()
	return err
}

func fill(p *error) { *p = do() } // want fill:`^writes p0$`

func outParameter() error {
	var err error
	fill(&err)
	log.Println(err)
	fill(&err)
	return err
}

var lastErr error

func globalAssigned() error {
	lastErr = do()
	log.Println(lastErr)
	lastErr = do()
	return lastErr
}

func globalSame() error {
	lastErr = do()
	log.Println(lastErr) // want `error is logged here`
	return lastErr
}

// The walk passes the second store on its way to the return. The branch that
// skips it never logged.
func (j *job) fieldRetried() error { // want fieldRetried:`^writes p0\.1$`
	j.err = do()
	if j.err != nil {
		slog.Warn("retrying", "err", j.err)
		j.err = do()
	}
	return j.err
}

func globalRetried() error {
	lastErr = do()
	if lastErr != nil {
		slog.Warn("retrying", "err", lastErr)
		lastErr = do()
	}
	return lastErr
}

func (j *job) fieldKept() error { // want fieldKept:`^writes p0\.1$`
	j.err = do()
	if j.err != nil {
		slog.Warn("failed", "err", j.err) // want `error is logged here`
	}
	return j.err
}

var errIllegal = errors.New("illegal")

func check(bool) bool { return true }

// A sentinel is logged and returned wrapped. The call between them cannot
// have replaced it: nothing in the function assigns it.
func sentinel(ok bool) error {
	if !check(ok) {
		slog.Error("refused", "err", errIllegal) // want `error is logged here`
		return fmt.Errorf("%w: id=%d", errIllegal, 1)
	}
	return nil
}

// ===== A path the program cannot take =====

// The last attempt breaks before the log, so the error logged is never the
// one returned. The walk would reach the return through the loop condition.
func retryBreaks(max int) error {
	var err error
	for attempt := 1; attempt <= max; attempt++ {
		err = do()
		if err == nil {
			return nil
		}
		if attempt == max {
			break
		}
		slog.Warn("retrying", "err", err)
		time.Sleep(time.Millisecond)
	}
	return err
}

func retryGuarded(max int) error {
	var err error
	for attempt := 0; attempt < max; attempt++ {
		if err = do(); err == nil {
			return nil
		}
		if attempt < max-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

// A return in the same iteration is still reached.
func retryGivesUp(max int) error {
	for attempt := 0; attempt < max; attempt++ {
		err := do()
		if err == nil {
			return nil
		}
		slog.Warn("attempt failed", "err", err) // want `error is logged here`
		if attempt == max-1 {
			return err
		}
	}
	return nil
}

// Each error is logged and also collected, and all of them are returned.
func collects(n int) error {
	var errs []error
	for range n {
		if err := do(); err != nil {
			slog.Error("failed", "err", err) // want `error is logged here`
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func verboseFlag(verbose bool) error {
	err := do()
	if verbose {
		log.Println(err)
	}
	if !verbose {
		return err
	}
	return nil
}

func loggedFlag(cond bool) error {
	err := do()
	logged := false
	if cond {
		log.Println(err)
		logged = true
	}
	if !logged {
		return err
	}
	return nil
}

func sameFlag(verbose bool) error {
	err := do()
	if verbose {
		log.Println(err) // want `error is logged here`
	}
	if verbose {
		return err
	}
	return nil
}

const debug = false

func debugOnly() error {
	err := do()
	if debug {
		fmt.Fprintf(os.Stderr, "err: %v\n", err)
	}
	return err
}

// ===== A deferred log that swallows the error =====

func deferSwallows() (err error) {
	defer func() {
		if err != nil {
			slog.Error("failed", "err", err)
			err = nil
		}
	}()
	return do()
}

// ===== A nil error is logged =====

func logsNil() error {
	err := do()
	if err == nil {
		slog.Info("done", "err", err)
		return err
	}
	return nil
}

// ===== A method value names its method =====

type lg struct{}

func (lg) Err(err error) { // want Err:"logs p1"
	slog.Error("failed", "err", err)
}

func methodValue(l lg) error {
	err := do()
	f := l.Err
	f(err) // want `error is logged by \(lg\)\.Err`
	return err
}

func methodExpression(l lg) error {
	err := do()
	lg.Err(l, err) // want `error is logged by \(lg\)\.Err`
	return err
}

// ===== Round two: calls between the log and the return =====

type counter struct {
	err error
	n   int
}

func (c *counter) bump() { c.n++ } // want bump:`^writes p0\.1$`

func (c *counter) reset() { c.err = nil } // want reset:`^writes p0\.0$`

// bump writes another field, so c.err still holds what was logged.
func (c *counter) callWritesOtherField() error { // want callWritesOtherField:`^writes p0\.0, writes p0\.1$`
	c.err = do()
	log.Println(c.err) // want `error is logged here`
	c.bump()
	return c.err
}

func (c *counter) callWritesTheField() error { // want callWritesTheField:`^writes p0\.0$`
	c.err = do()
	log.Println(c.err)
	c.reset()
	return c.err
}

func localCallWritesOtherField() error {
	var c counter
	c.err = do()
	log.Println(c.err) // want `error is logged here`
	c.bump()
	return c.err
}

func fillField(p *error) { *p = do() } // want fillField:`^writes p0$`

func (c *counter) outParameterField() error { // want outParameterField:`^writes p0\.0$`
	c.err = do()
	log.Println(c.err)
	fillField(&c.err)
	return c.err
}

func (c *counter) closureAssignsField() error { // want closureAssignsField:`^writes p0\.0$`
	c.err = do()
	log.Println(c.err)
	retry := func() { c.err = do() }
	retry()
	return c.err
}

// ===== Round two: memory the function did not write =====

func (c *counter) readsTwice() error { // want readsTwice:`^carries p0\.0→r0$`
	log.Println(c.err) // want `error is logged here`
	return c.err
}

type outer struct{ inner *counter }

func (o *outer) nested() error { // want nested:`^writes p0\.0\.\*\.0$`
	o.inner.err = do()
	log.Println(o.inner.err) // want `error is logged here`
	return o.inner.err
}

// ===== Round two: the walked path, and conditions =====

func (c *counter) storedOnLoggingPath(verbose bool) error { // want storedOnLoggingPath:`^writes p0\.0$`
	c.err = do()
	if verbose {
		log.Println(c.err)
	}
	if verbose {
		c.err = errors.New("other")
	}
	return c.err
}

// The walk reaches the merge with a decided condition, and must still reach
// the return after it.
func mergeThenReturn(cond bool) error {
	err := do()
	log.Println(err) // want `error is logged here`
	x := 0
	if !cond {
		x = 1
	} else {
		x = 2
	}
	slog.Info("x", "x", x)
	if !cond {
		return nil
	}
	return err
}

// ===== Round two: numeric error types =====

type code int

func (c code) Error() string { return "code" }

func mkCode() code { return 3 }

func numericError() error {
	c := mkCode()
	log.Println(c) // want `error is logged here`
	return c
}

// ===== Round two: a field of what a call returned =====

type result struct {
	id  string
	err error
}

func newResult(id string, err error) *result { return &result{id: id, err: err} } // want newResult:`^carries p0→r0, carries p1→r0$`

func resultValue(id string, err error) result { return result{id: id, err: err} } // want resultValue:`^carries p0→r0, carries p1→r0$`

func (r *result) ID() string { return r.id } // want ID:`^carries p0\.0→r0$`

func fieldOfPointerResult() error {
	err := do()
	r := newResult("x", err)
	slog.Info("result", "id", r.id)
	return err
}

func fieldOfValueResult() error {
	err := do()
	r := resultValue("x", err)
	slog.Info("result", "id", r.id)
	return err
}

func getterOfResult() error {
	err := do()
	r := newResult("x", err)
	slog.Info("result", "id", r.ID())
	return err
}

func getterOfLocal() error {
	err := do()
	r := &result{id: "x", err: err}
	slog.Info("result", "id", r.ID())
	return err
}

// ===== Round two, in practice =====

// The deferred Close sets err only when err is nil, so the error logged is
// the one returned.
func deferCloseKeeps(c io.Closer) (err error) {
	defer func() {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if err = do(); err != nil {
		slog.Error("query failed", "err", err) // want `error is logged here`
		return err
	}
	return nil
}

// A deferred closure that always replaces err hands back a new error.
func deferAlwaysReplaces() (err error) {
	defer func() { err = errors.New("replaced") }()
	if err = do(); err != nil {
		slog.Error("query failed", "err", err)
		return err
	}
	return nil
}

// The closure assigns err before the log. The log and the return read the
// same error.
func closureBefore() error {
	var err error
	try := func() { err = do() }
	try()
	if err != nil {
		slog.Error("failed", "err", err) // want `error is logged here`
		return err
	}
	return nil
}

func mapField(m map[string]result) error { // want mapField:`^carries p0\.\[\]\.1→r0, writes p0\.\[\]$`
	m["a"] = result{id: "a", err: do()}
	slog.Info("checked", "id", m["a"].id)
	return m["a"].err
}

func mapFieldLogged(m map[string]result) error { // want mapFieldLogged:`^carries p0\.\[\]\.1→r0, writes p0\.\[\]$`
	m["a"] = result{id: "a", err: do()}
	slog.Error("checked", "err", m["a"].err) // want `error is logged here`
	return m["a"].err
}

func rangeMapField(m map[string]result) error { // want rangeMapField:`^carries p0\.\[\]\.1→r0$`
	for _, r := range m {
		slog.Info("checked", "id", r.id)
		return r.err
	}
	return nil
}

func sliceLiteralField() error {
	rs := []result{{id: "a", err: do()}}
	for _, r := range rs {
		slog.Info("checked", "id", r.id)
		return r.err
	}
	return nil
}

func constantIndexes() error {
	var errs [2]error
	errs[0], errs[1] = do(), do()
	slog.Error("first", "err", errs[0])
	return errs[1]
}

func constantIndexSame() error {
	var errs [2]error
	errs[0] = do()
	slog.Error("first", "err", errs[0]) // want `error is logged here`
	return errs[0]
}

type holder struct{ p *error }

// A store through a pointer held in memory may write err.
func throughHeldPointer() error {
	var err error
	h := holder{p: &err}
	err = do()
	log.Println(err)
	*h.p = errors.New("other")
	return err
}

func clearedMap(m map[string]error) error { // want clearedMap:`^writes p0$`
	m["a"] = do()
	log.Println(m["a"])
	clear(m)
	return m["a"]
}

func switchTwice(mode int) error {
	err := do()
	switch mode {
	case 1:
		log.Println(err)
	}
	switch mode {
	case 2:
		return err
	}
	return nil
}

func boolCompared(v bool) error {
	err := do()
	if v == false {
		log.Println(err)
	}
	if v {
		return err
	}
	return nil
}

// A list walked through its own pointer. Reading it must end.
type node struct {
	next *node
	err  error
}

func walkList(n *node) error { // want walkList:`^carries p0→r0$`
	for p := n; p != nil; p = p.next {
		if p.err != nil {
			log.Println(p.err) // want `error is logged here`
			return p.err
		}
	}
	return nil
}

// ===== Round two: which counter tests the loop rule reads =====

// The last attempt still logs and then leaves the loop.
func counterAfterFirst() error {
	var err error
	for i := 0; i < 3; i++ {
		err = do()
		if err != nil && i > 0 {
			slog.Warn("attempt failed", "err", err) // want `error is logged here`
		}
	}
	return err
}

// A test of the counter that cannot be read at its last value is taken to
// exclude it.
func counterParity(n int) error {
	var err error
	for i := 0; i < n; i++ {
		err = do()
		if i%2 == 0 {
			slog.Error("attempt failed", "err", err)
		}
	}
	return err
}

func backoff() error {
	d := time.Millisecond
	var err error
	for d < time.Second {
		if err = do(); err == nil {
			return nil
		}
		if d < 500*time.Millisecond {
			slog.Warn("retrying", "err", err)
		}
		d *= 2
	}
	return err
}

func decrementedFirst() error {
	attempts := 3
	var err error
	for attempts > 0 {
		if err = do(); err == nil {
			return nil
		}
		attempts--
		if attempts > 0 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

// for range 3 tests at the bottom. The last attempt, 2, does not log.
func rangeBeforeLast() error {
	var err error
	for i := range 3 {
		if err = do(); err == nil {
			return nil
		}
		if i < 2 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

func rangeOnLast() error {
	var err error
	for i := range 3 {
		if err = do(); err == nil {
			return nil
		}
		if i == 2 {
			slog.Warn("giving up", "err", err) // want `error is logged here`
		}
	}
	return err
}

func countdown(attempts int) error {
	var err error
	for ; attempts > 0; attempts-- {
		if err = do(); err == nil {
			return nil
		}
		if attempts == 1 {
			break
		}
		slog.Warn("retrying", "err", err)
	}
	return err
}

// ===== Round three =====

func resetGeneric[T any](p *T) { var z T; *p = z } // want resetGeneric:`^writes p0$`

// An instance of a generic function converts its pointer argument.
func genericLocal() error {
	err := do()
	log.Println(err)
	resetGeneric(&err)
	return err
}

func clearErr(p *error) { *p = nil } // want clearErr:`^writes p0$`

func deferredClear() (err error) {
	defer clearErr(&err)
	if err = do(); err != nil {
		log.Println(err)
		return err
	}
	return nil
}

// A count of failures is not the loop's counter. Its test does not stop the
// walk at the loop's exit.
func failureCount(items []string) error {
	var lastErr error
	failures := 0
	for range items {
		if err := do(); err != nil {
			failures++
			if failures > 3 {
				log.Println("repeated failure", err) // want `error is logged here`
			}
			lastErr = err
		}
	}
	return lastErr
}

func more() bool { return true }

// The error logged is returned early in the next iteration. Only the loop's
// exit is not taken after the log.
func nextIteration(n int) error {
	var last error
	for i := 0; i < n; i++ {
		if last != nil && more() {
			return last
		}
		if i == n-1 {
			break
		}
		if err := do(); err != nil {
			last = err
			log.Println(err) // want `error is logged here`
		}
	}
	return nil
}

// An interface parameter that a callee asserts is written through.
func fillAny(v any) { *v.(*error) = nil } // want fillAny:`^writes p0$`

func throughAny() error {
	err := do()
	log.Println(err)
	fillAny(&err)
	return err
}

// A long function of independent branches after a log. The walk must stay
// linear in them.
func manyBranches() error {
	err := do()
	log.Println(err) // want `error is logged here`
	if more() {
		_ = 1
	}
	if more() {
		_ = 2
	}
	if more() {
		_ = 3
	}
	if more() {
		_ = 4
	}
	if more() {
		_ = 5
	}
	if more() {
		_ = 6
	}
	if more() {
		_ = 7
	}
	if more() {
		_ = 8
	}
	if more() {
		_ = 9
	}
	if more() {
		_ = 10
	}
	if more() {
		_ = 11
	}
	if more() {
		_ = 12
	}
	if more() {
		_ = 13
	}
	if more() {
		_ = 14
	}
	if more() {
		_ = 15
	}
	if more() {
		_ = 16
	}
	if more() {
		_ = 17
	}
	if more() {
		_ = 18
	}
	if more() {
		_ = 19
	}
	if more() {
		_ = 20
	}
	return err
}

// ===== Round three, in practice =====

// In a for range n loop the head is the first block of the body, and here
// its branch is the guard.
func rangeGuardFirst(n int) error {
	var err error
	for attempt := range n {
		err = do()
		if attempt < n-1 {
			slog.Warn("retrying", "err", err)
			time.Sleep(time.Millisecond)
		}
	}
	return err
}

func rangeGuardNotLast() error {
	var err error
	for attempt := range 5 {
		err = do()
		if attempt != 4 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

func rangeGuardLast() error {
	var err error
	for attempt := range 5 {
		err = do()
		if attempt == 4 {
			slog.Warn("giving up", "err", err) // want `error is logged here`
		}
	}
	return err
}

var errIgnorable = errors.New("ignorable")

var errInternal = errors.New("internal")

// The deferred closure replaces err on both branches after it logs it.
func deferTranslates() (err error) {
	defer func() {
		if err != nil {
			slog.Error("failed", "err", err)
			if errors.Is(err, errIgnorable) {
				err = nil
			} else {
				err = errInternal
			}
		}
	}()
	return do()
}

// Only one branch replaces it. The other hands the logged error back.
func deferTranslatesSome() (err error) {
	defer func() { // want `error is logged by a function literal`
		if err != nil {
			slog.Error("failed", "err", err)
			if errors.Is(err, errIgnorable) {
				err = nil
			}
		}
	}()
	return do()
}

// x is 2 on the path from the log, so x == 1 fails there.
func constantAfterReassign(x int) error {
	err := do()
	if x == 1 {
		log.Println(err)
		x = 2
	}
	if x == 1 {
		return err
	}
	return nil
}

// A condition decided right after the log is read again at the end, past
// many unrelated branches. The walk must still get there.
func decidedFarAhead(c0 bool) error {
	err := do()
	log.Println(err) // want `error is logged here`
	if c0 {
		_ = 0
	}
	if more() {
		_ = 1
	}
	if more() {
		_ = 2
	}
	if more() {
		_ = 3
	}
	if more() {
		_ = 4
	}
	if more() {
		_ = 5
	}
	if more() {
		_ = 6
	}
	if more() {
		_ = 7
	}
	if more() {
		_ = 8
	}
	if more() {
		_ = 9
	}
	if more() {
		_ = 10
	}
	if more() {
		_ = 11
	}
	if more() {
		_ = 12
	}
	if more() {
		_ = 13
	}
	if more() {
		_ = 14
	}
	if more() {
		_ = 15
	}
	if more() {
		_ = 16
	}
	if c0 {
		return err
	}
	return nil
}

// The counter steps by more than 1, so its last value is not read. Nothing
// on the counter decides the log, which then runs on the last chunk too.
func chunks(items []int) error {
	var errs error
	for i := 0; i < len(items); i += 100 {
		if err := do(); err != nil {
			slog.Error("could not store", "err", err) // want `error is logged here`
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// A guard over the counter and a constant, against a bound the function is
// handed: it holds on the last iteration for some bound.
func guardAfterFirst(n int) error {
	var err error
	for i := 0; i < n; i++ {
		err = do()
		if i > 0 {
			slog.Warn("attempt failed", "err", err) // want `error is logged here`
		}
	}
	return err
}

func lastOfSlice(s []int) error {
	var err error
	for i := range s {
		err = do()
		if i == len(s)-1 {
			slog.Warn("giving up", "err", err) // want `error is logged here`
		}
	}
	return err
}

// len(s) read twice is one bound.
func beforeLastOfSlice(s []int) error {
	var err error
	for i := range s {
		err = do()
		if i < len(s)-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

type limits struct{ max int }

func boundInField(c *limits) error {
	var err error
	for i := 0; i < c.max; i++ {
		err = do()
		if i < c.max-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

// The guard reads the counter as the body changed it.
func counterChangedInBody(n int) error {
	var err error
	for i := 0; i < n; i++ {
		err = do()
		if err != nil {
			i++
		}
		if i < n-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

// ===== Round four =====

var errSkip = errors.New("skip")

// The walk that takes err = nil first must not keep the other path from the
// return: the φ-node each resolves differs.
func phiKeepsErr(verbose bool) error {
	err := do()
	log.Printf("sync failed: %v", err) // want `error is logged here`
	if errors.Is(err, errSkip) {
		err = nil
	}
	if verbose {
		_ = 1
	}
	return err
}

// A deferred closure that wraps err after logging it hands the error back.
func deferWraps() (err error) {
	defer func() { // want `error is logged by a function literal`
		if err != nil {
			log.Printf("failed: %v", err)
			err = fmt.Errorf("wrapped: %w", err)
		}
	}()
	return do()
}

// It copies err and clears it before logging the copy.
func deferCopiesThenClears() (err error) {
	defer func() {
		if err != nil {
			e := err
			err = nil
			slog.Error("failed", "err", e)
		}
	}()
	return do()
}

// Two back edges step the counter by 2 and by 1, so its last value is not
// read, and the loop's exits are dropped.
func mixedStep(c func() bool) error {
	var err error
	i := 0
	for i < 10 {
		err = do()
		if i == 9 {
			break
		}
		log.Println(err)
		if c() {
			i += 2
			continue
		}
		i++
	}
	return err
}

// What was decided about the counter in one iteration does not hold after
// the head defines it again.
func countDownLast(n int) error {
	var err error
	for i := n; i > 0; i-- {
		err = do()
		if i == 1 {
			slog.Warn("last", "err", err) // want `error is logged here`
		}
	}
	return err
}

// The head also tests err. The path decided err != nil on the value the head
// φ-node resolves to.
func headTestsErr(n int) error {
	err := errors.New("init")
	for i := 0; i < n && err != nil; i++ {
		err = do()
		if err != nil && i < n-1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

func otherCounter(n int) error {
	var err error
	for i, j := 0, n; i < n; i, j = i+1, j-1 {
		err = do()
		if j > 1 {
			slog.Warn("retrying", "err", err)
		}
	}
	return err
}

func againstLoadedBound(s []int) error {
	last := new(int)
	*last = len(s) - 1
	var err error
	for i := 0; i < len(s); i++ {
		if err = do(); err == nil {
			return nil
		}
		if i < *last {
			slog.Warn("next", "err", err)
		}
	}
	return err
}

// Related comparisons of one value.
func sameComparisonTwice(x int) error {
	err := do()
	if x > 0 {
		log.Println(err)
	}
	if x > 0 {
		return nil
	}
	return err
}

func rangesDisjoint(code int) error {
	err := do()
	if code < 500 {
		log.Println(err)
	}
	if code >= 500 {
		return err
	}
	return nil
}

func pointOutsideRange(code int) error {
	err := do()
	if code == 404 {
		log.Println(err)
	}
	if code >= 500 {
		return err
	}
	return nil
}

func lenTwice(s []int) error {
	err := do()
	if len(s) == 0 {
		log.Println(err)
	}
	if len(s) > 0 {
		return err
	}
	return nil
}

func rangesOverlap(code int) error {
	err := do()
	if code < 500 {
		log.Println(err) // want `error is logged here`
	}
	if code >= 400 {
		return err
	}
	return nil
}

// ===== Round five =====

type found struct {
	fatal bool
	err   error
}

// Each key looks up its own element. The one logged is not the one returned.
func lookupPerKey(m map[string]found, keys []string) error { // want lookupPerKey:`^carries p0\.\[\]\.1→r0$`
	for _, k := range keys {
		r, ok := m[k]
		if !ok {
			continue
		}
		if r.fatal {
			return r.err
		}
		log.Print(r.err)
	}
	return nil
}

func rangePerElement(m map[string]error, fatal func(string) bool) error { // want rangePerElement:`^carries p0\.\[\]→r0$`
	for k, err := range m {
		if fatal(k) {
			return err
		}
		log.Print(k, err)
	}
	return nil
}

func twoKeys(m map[string]found) error { // want twoKeys:`^carries p0\.\[\]\.1→r0$`
	log.Print(m["a"].err)
	return m["b"].err
}

func twoIndexes(errs []error, i, j int) error { // want twoIndexes:`^carries p0\.\[\]→r0$`
	log.Print(errs[i])
	return errs[j]
}

func twoIndexesArray(a [4]error, i, j int) error { // want twoIndexesArray:`^carries p0\.\[\]→r0$`
	log.Print(a[i])
	return a[j]
}

// A struct received once, and implementing error: its fields are two errors.
type both struct{ first, second error }

func (both) Error() string { return "both" }

func receivedPair(c chan both) error {
	p := <-c
	log.Print(p.first)
	return p.second
}

// Allocated again each iteration: the next iteration's value is zero.
type holderT struct{ err error }

func freshEachIteration(items []int) error {
	for _, it := range items {
		t := &holderT{}
		if it == 0 {
			t.err = do()
			log.Print(t.err)
			continue
		}
		return t.err
	}
	return nil
}

func freshArrayEachIteration(items []int) error {
	for _, it := range items {
		var errs [2]error
		if it == 0 {
			errs[0] = do()
			log.Print(errs[0])
			continue
		}
		return errs[0]
	}
	return nil
}

type twoErrs struct{ first, second error }

// A struct passed by value holds two errors. Logging one returns nothing of
// the other.
func fieldsOfValue(p twoErrs) error { // want fieldsOfValue:`^carries p0\.1→r0$`
	log.Print(p.first)
	return p.second
}

func sameFieldOfValue(p twoErrs) error { // want sameFieldOfValue:`^carries p0\.0→r0$`
	log.Print(p.first) // want `error is logged here`
	return p.first
}

func (twoErrs) Error() string { return "two" }

// A struct asserted out of an error: its fields are two errors, and the
// error it came from is neither.
func assertedFields(e error) error { // want assertedFields:`^carries nothing$`
	log.Print(e.(twoErrs).first)
	return e.(twoErrs).second
}

func assertedSameField(e error) error { // want assertedSameField:`^carries nothing$`
	p := e.(twoErrs)
	log.Print(p.first) // want `error is logged here`
	return p.first
}

type sometimes struct{ err error }

// One side of a branch after the log clears the field, the other keeps it.
// A path is told apart by the writes on it.
func (s *sometimes) clearedOnOneSide(c bool) error { // want clearedOnOneSide:`^writes p0\.0$`
	s.err = do()
	if s.err != nil {
		slog.Error("failed", "err", s.err) // want `error is logged here`
		if c {
			s.err = nil
		}
	}
	return s.err
}

// Many logs, each followed by conditions that are read again: the walk that
// records them does not settle, and the one that does not record them
// reaches the return.
func manyReadAgain(c0, c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11, c12, c13 bool) error {
	err := do()
	if c0 {
		log.Println(err) // want `error is logged here`
	}
	if c1 {
		log.Println(err) // want `error is logged here`
	}
	if c2 {
		g0()
	}
	if c3 {
		g0()
	}
	if c4 {
		g0()
	}
	if c5 {
		g0()
	}
	if c6 {
		g0()
	}
	if c7 {
		g0()
	}
	if c8 {
		g0()
	}
	if c9 {
		g0()
	}
	if c10 {
		g0()
	}
	if c11 {
		g0()
	}
	if c12 {
		g0()
	}
	if c13 {
		g0()
	}
	if c2 && c3 && c4 && c5 && c6 && c7 && c8 && c9 && c10 && c11 && c12 && c13 {
		g0()
	}
	return err
}

func g0() {}

// Two logs, each followed by more paths than a walk follows, and no return
// of the error: the second log's walk tries less before giving up.
func twoUnsettled(c0, c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11, c12, c13 bool) error {
	err := do()
	if c0 {
		log.Println(err)
	}
	if c1 {
		log.Println(err)
	}
	if c2 {
		g0()
	}
	if c3 {
		g0()
	}
	if c4 {
		g0()
	}
	if c5 {
		g0()
	}
	if c6 {
		g0()
	}
	if c7 {
		g0()
	}
	if c8 {
		g0()
	}
	if c9 {
		g0()
	}
	if c10 {
		g0()
	}
	if c11 {
		g0()
	}
	if c12 {
		g0()
	}
	if c13 {
		g0()
	}
	if c2 {
		g0()
	}
	if c3 {
		g0()
	}
	if c4 {
		g0()
	}
	if c5 {
		g0()
	}
	if c6 {
		g0()
	}
	if c7 {
		g0()
	}
	if c8 {
		g0()
	}
	if c9 {
		g0()
	}
	if c10 {
		g0()
	}
	if c11 {
		g0()
	}
	if c12 {
		g0()
	}
	if c13 {
		g0()
	}
	return nil
}

// ===== Round six =====

// A walk this branchy is run again with a key that leaves decisions out. It
// still decides c on its path, so the return under !c never hands back the
// error it logged under c.
func frozenCorrelated(c, b0, b1, b2, b3, b4, b5, b6, b7, b8, b9, b10, b11, b12 bool) error {
	err := do()
	if err == nil {
		return nil
	}
	log.Println(err)
	var ret error
	if c {
		ret = err
	} else {
		ret = nil
	}
	if b0 {
		g0()
	}
	if b1 {
		g0()
	}
	if b2 {
		g0()
	}
	if b3 {
		g0()
	}
	if b4 {
		g0()
	}
	if b5 {
		g0()
	}
	if b6 {
		g0()
	}
	if b7 {
		g0()
	}
	if b8 {
		g0()
	}
	if b9 {
		g0()
	}
	if b10 {
		g0()
	}
	if b11 {
		g0()
	}
	if b12 {
		g0()
	}
	if b0 {
		h0()
	}
	if b1 {
		h0()
	}
	if b2 {
		h0()
	}
	if b3 {
		h0()
	}
	if b4 {
		h0()
	}
	if b5 {
		h0()
	}
	if b6 {
		h0()
	}
	if b7 {
		h0()
	}
	if b8 {
		h0()
	}
	if b9 {
		h0()
	}
	if b10 {
		h0()
	}
	if b11 {
		h0()
	}
	if b12 {
		h0()
	}
	if !c {
		return ret
	}
	return nil
}

func h0() {}

type boxed struct{ err error }

var boxSink *boxed

// The error reaches memory through a φ-node. The key tells the two sides
// apart though no later instruction reads the φ-node itself.
func phiIntoMemory(c bool) error {
	err := do()
	if err == nil {
		return nil
	}
	log.Println(err) // want `error is logged here`
	h := &boxed{}
	boxSink = h
	var e error
	if c {
		e = nil
	} else {
		e = err
	}
	h.err = e
	if more() {
		g0()
	}
	return h.err
}

type typedErr struct{}

func (*typedErr) Error() string { return "typed" }

func getTyped() *typedErr { return nil }

// A nil *T converted to error is not a nil error.
func typedNil(err error) error { // want typedNil:`^carries p0→r0$`
	p := getTyped()
	if p == nil {
		log.Println(err) // want `error is logged here`
		var e error = p
		if e != nil {
			return err
		}
	}
	return nil
}

// The walk after the log does not settle, and the frozen one walks a loop
// whose conditions it decides and then leaves: they go stale round the loop,
// and no branch after the loop reads them. Nothing hands the error back.
func frozenThroughLoop(n int, b0, b1, b2, b3, b4, b5, b6, b7, b8, b9, b10, b11, b12 bool) error {
	err := do()
	log.Println(err)
	if b0 {
		g0()
	}
	if b1 {
		g0()
	}
	if b2 {
		g0()
	}
	if b3 {
		g0()
	}
	if b4 {
		g0()
	}
	if b5 {
		g0()
	}
	if b6 {
		g0()
	}
	if b7 {
		g0()
	}
	if b8 {
		g0()
	}
	if b9 {
		g0()
	}
	if b10 {
		g0()
	}
	if b11 {
		g0()
	}
	if b12 {
		g0()
	}
	for i := 0; i < n; i++ {
		if i == 3 {
			g0()
		}
		if more() {
			h0()
		}
	}
	if b0 {
		h0()
	}
	if b1 {
		h0()
	}
	if b2 {
		h0()
	}
	if b3 {
		h0()
	}
	if b4 {
		h0()
	}
	if b5 {
		h0()
	}
	if b6 {
		h0()
	}
	if b7 {
		h0()
	}
	if b8 {
		h0()
	}
	if b9 {
		h0()
	}
	if b10 {
		h0()
	}
	if b11 {
		h0()
	}
	if b12 {
		h0()
	}
	return nil
}

// The error reaches a channel through a φ-node, and comes back out.
func phiIntoChannel(c bool, ch chan error) error { // want phiIntoChannel:`^writes p1\.\[\]$`
	err := do()
	if err == nil {
		return nil
	}
	log.Println(err)
	var e error
	if c {
		e = nil
	} else {
		e = err
	}
	ch <- e
	if more() {
		g0()
	}
	return <-ch
}

type clearable struct{ err error }

func (s *clearable) clear() { s.err = nil } // want clear:`^writes p0\.0$`

// The path through the call that clears the field must not hide the path
// that keeps it: a call that writes is a write on the path.
func clearedByCall(c bool) error {
	s := &clearable{}
	s.err = do()
	slog.Error("failed", "err", s.err) // want `error is logged here`
	if c {
		s.clear()
	}
	return s.err
}

// A map's length changes with no new value, so two tests of it are not one.
func mapLenChanges(m map[string]int) error { // want mapLenChanges:`^writes p0\.\[\]$`
	err := do()
	if len(m) == 0 {
		log.Println(err) // want `error is logged here`
		m["seed"] = 1
	}
	if len(m) != 0 {
		return err
	}
	return nil
}

// A config loader: each field is parsed, a bad one warned about and skipped,
// a good one written. Each branch writes, so the walks do not settle, and
// the frozen walk must not multiply by the writes.

type config struct {
	f0 int
	f1 int
	f2 int
	f3 int
	f4 int
	f5 int
	f6 int
	f7 int
	f8 int
	f9 int
	f10 int
	f11 int
	f12 int
	f13 int
	f14 int
	f15 int
	f16 int
	f17 int
	f18 int
	f19 int
	f20 int
	f21 int
	f22 int
	f23 int
	f24 int
	f25 int
	f26 int
	f27 int
	f28 int
	f29 int
	f30 int
	f31 int
	f32 int
	f33 int
	f34 int
	f35 int
	f36 int
	f37 int
	f38 int
	f39 int
	err error
}

func (l *config) load(in map[string]string) error { // want load:`writes p0`
	if v, err := strconv.Atoi(in["f0"]); err != nil {
		slog.Warn("bad f0", "err", err)
	} else {
		l.f0 = v
	}
	if v, err := strconv.Atoi(in["f1"]); err != nil {
		slog.Warn("bad f1", "err", err)
	} else {
		l.f1 = v
	}
	if v, err := strconv.Atoi(in["f2"]); err != nil {
		slog.Warn("bad f2", "err", err)
	} else {
		l.f2 = v
	}
	if v, err := strconv.Atoi(in["f3"]); err != nil {
		slog.Warn("bad f3", "err", err)
	} else {
		l.f3 = v
	}
	if v, err := strconv.Atoi(in["f4"]); err != nil {
		slog.Warn("bad f4", "err", err)
	} else {
		l.f4 = v
	}
	if v, err := strconv.Atoi(in["f5"]); err != nil {
		slog.Warn("bad f5", "err", err)
	} else {
		l.f5 = v
	}
	if v, err := strconv.Atoi(in["f6"]); err != nil {
		slog.Warn("bad f6", "err", err)
	} else {
		l.f6 = v
	}
	if v, err := strconv.Atoi(in["f7"]); err != nil {
		slog.Warn("bad f7", "err", err)
	} else {
		l.f7 = v
	}
	if v, err := strconv.Atoi(in["f8"]); err != nil {
		slog.Warn("bad f8", "err", err)
	} else {
		l.f8 = v
	}
	if v, err := strconv.Atoi(in["f9"]); err != nil {
		slog.Warn("bad f9", "err", err)
	} else {
		l.f9 = v
	}
	if v, err := strconv.Atoi(in["f10"]); err != nil {
		slog.Warn("bad f10", "err", err)
	} else {
		l.f10 = v
	}
	if v, err := strconv.Atoi(in["f11"]); err != nil {
		slog.Warn("bad f11", "err", err)
	} else {
		l.f11 = v
	}
	if v, err := strconv.Atoi(in["f12"]); err != nil {
		slog.Warn("bad f12", "err", err)
	} else {
		l.f12 = v
	}
	if v, err := strconv.Atoi(in["f13"]); err != nil {
		slog.Warn("bad f13", "err", err)
	} else {
		l.f13 = v
	}
	if v, err := strconv.Atoi(in["f14"]); err != nil {
		slog.Warn("bad f14", "err", err)
	} else {
		l.f14 = v
	}
	if v, err := strconv.Atoi(in["f15"]); err != nil {
		slog.Warn("bad f15", "err", err)
	} else {
		l.f15 = v
	}
	if v, err := strconv.Atoi(in["f16"]); err != nil {
		slog.Warn("bad f16", "err", err)
	} else {
		l.f16 = v
	}
	if v, err := strconv.Atoi(in["f17"]); err != nil {
		slog.Warn("bad f17", "err", err)
	} else {
		l.f17 = v
	}
	if v, err := strconv.Atoi(in["f18"]); err != nil {
		slog.Warn("bad f18", "err", err)
	} else {
		l.f18 = v
	}
	if v, err := strconv.Atoi(in["f19"]); err != nil {
		slog.Warn("bad f19", "err", err)
	} else {
		l.f19 = v
	}
	if v, err := strconv.Atoi(in["f20"]); err != nil {
		slog.Warn("bad f20", "err", err)
	} else {
		l.f20 = v
	}
	if v, err := strconv.Atoi(in["f21"]); err != nil {
		slog.Warn("bad f21", "err", err)
	} else {
		l.f21 = v
	}
	if v, err := strconv.Atoi(in["f22"]); err != nil {
		slog.Warn("bad f22", "err", err)
	} else {
		l.f22 = v
	}
	if v, err := strconv.Atoi(in["f23"]); err != nil {
		slog.Warn("bad f23", "err", err)
	} else {
		l.f23 = v
	}
	if v, err := strconv.Atoi(in["f24"]); err != nil {
		slog.Warn("bad f24", "err", err)
	} else {
		l.f24 = v
	}
	if v, err := strconv.Atoi(in["f25"]); err != nil {
		slog.Warn("bad f25", "err", err)
	} else {
		l.f25 = v
	}
	if v, err := strconv.Atoi(in["f26"]); err != nil {
		slog.Warn("bad f26", "err", err)
	} else {
		l.f26 = v
	}
	if v, err := strconv.Atoi(in["f27"]); err != nil {
		slog.Warn("bad f27", "err", err)
	} else {
		l.f27 = v
	}
	if v, err := strconv.Atoi(in["f28"]); err != nil {
		slog.Warn("bad f28", "err", err)
	} else {
		l.f28 = v
	}
	if v, err := strconv.Atoi(in["f29"]); err != nil {
		slog.Warn("bad f29", "err", err)
	} else {
		l.f29 = v
	}
	if v, err := strconv.Atoi(in["f30"]); err != nil {
		slog.Warn("bad f30", "err", err)
	} else {
		l.f30 = v
	}
	if v, err := strconv.Atoi(in["f31"]); err != nil {
		slog.Warn("bad f31", "err", err)
	} else {
		l.f31 = v
	}
	if v, err := strconv.Atoi(in["f32"]); err != nil {
		slog.Warn("bad f32", "err", err)
	} else {
		l.f32 = v
	}
	if v, err := strconv.Atoi(in["f33"]); err != nil {
		slog.Warn("bad f33", "err", err)
	} else {
		l.f33 = v
	}
	if v, err := strconv.Atoi(in["f34"]); err != nil {
		slog.Warn("bad f34", "err", err)
	} else {
		l.f34 = v
	}
	if v, err := strconv.Atoi(in["f35"]); err != nil {
		slog.Warn("bad f35", "err", err)
	} else {
		l.f35 = v
	}
	if v, err := strconv.Atoi(in["f36"]); err != nil {
		slog.Warn("bad f36", "err", err)
	} else {
		l.f36 = v
	}
	if v, err := strconv.Atoi(in["f37"]); err != nil {
		slog.Warn("bad f37", "err", err)
	} else {
		l.f37 = v
	}
	if v, err := strconv.Atoi(in["f38"]); err != nil {
		slog.Warn("bad f38", "err", err)
	} else {
		l.f38 = v
	}
	if v, err := strconv.Atoi(in["f39"]); err != nil {
		slog.Warn("bad f39", "err", err)
	} else {
		l.f39 = v
	}
	return l.err
}

// ===== Round seven =====

// A goto into the middle of a loop makes a cycle no header dominates. The
// frozen walk enters it again after the log, where pending is false.
func gotoCycle(p bool, err error, more func() bool, a0, a1, a2, a3, a4, a5, a6, a7, a8, a9, a10, a11, a12 bool) error { // want gotoCycle:`^carries p1→r0$`
	pending := true
	var e error
	n := 0
	if p {
		goto Y
	}
L:
	if pending {
		if more() {
			log.Print(err)
			pending = false
			if a0 {
				n++
			}
			if a1 {
				n++
			}
			if a2 {
				n++
			}
			if a3 {
				n++
			}
			if a4 {
				n++
			}
			if a5 {
				n++
			}
			if a6 {
				n++
			}
			if a7 {
				n++
			}
			if a8 {
				n++
			}
			if a9 {
				n++
			}
			if a10 {
				n++
			}
			if a11 {
				n++
			}
			if a12 {
				n++
			}
			if a0 {
				n--
			}
			if a1 {
				n--
			}
			if a2 {
				n--
			}
			if a3 {
				n--
			}
			if a4 {
				n--
			}
			if a5 {
				n--
			}
			if a6 {
				n--
			}
			if a7 {
				n--
			}
			if a8 {
				n--
			}
			if a9 {
				n--
			}
			if a10 {
				n--
			}
			if a11 {
				n--
			}
			if a12 {
				n--
			}
			goto Y
		}
		e = err
	} else {
		e = nil
	}
	_ = n
	return e
Y:
	g0()
	goto L
}
