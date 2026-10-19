package internal

import (
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"math/bits"
	"reflect"
	"slices"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/cond"
	"github.com/mpyw/errlogreturn/internal/loop"
	"github.com/mpyw/errlogreturn/internal/store"
	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// checkBook indexes the pass's files for reporting.
//
//declscope:shared
type checkBook struct {
	// checkFiles maps a file name to its syntax, for the statement a report
	// is anchored on.
	//
	//declscope:private
	checkFiles map[string]*ast.File
	// checkGenerated holds the names of generated files. They are
	// summarized, since code calls into them, but never reported on.
	//
	//declscope:private
	checkGenerated map[string]bool
	// checkReadsOf caches checkReads by function.
	//
	//declscope:private
	checkReadsOf map[*ssa.Function]*checkReadSet
	// checkDeps caches checkDepends.
	//
	//declscope:private
	checkDeps map[checkDep]bool
	// checkUnsettled holds the functions where a walk did not settle.
	//
	//declscope:private
	checkUnsettled map[*ssa.Function]bool
	// checkWrites caches checkWriteHash.
	//
	//declscope:private
	checkWrites map[*ssa.BasicBlock]uint64
	// checkLoops caches, for each function, the blocks inside a loop.
	//
	//declscope:private
	checkLoops map[*ssa.Function]map[*ssa.BasicBlock]bool
}

// check reports every place in fn where an error is logged and then returned
// on the same path.
//
//declscope:shared
func (c *checker) check(fn *ssa.Function) {
	live := checkLive(fn)
	for _, b := range fn.Blocks {
		if !live[b] {
			continue
		}
		for i, instr := range b.Instrs {
			u, ok := c.sinkAt(instr)
			if !ok || !c.checkReportable(instr.Pos()) {
				continue
			}
			if ret := c.checkReturned(fn, b, i, u); ret != nil {
				c.checkReport(instr, ret, u)
			}
		}
	}
}

// checkFile returns the syntax of the named file, and whether it is
// generated. It is nil for a file the pass does not hold. Files are named as
// they are on disk: a //line directive renames every position below it.
func (c *checker) checkFile(name string) (*ast.File, bool) {
	if c.checkFiles == nil {
		c.checkFiles = make(map[string]*ast.File)
		c.checkGenerated = make(map[string]bool)
		for _, f := range c.pass.Files {
			n := c.pass.Fset.PositionFor(f.Pos(), false).Filename
			c.checkFiles[n] = f
			if ast.IsGenerated(f) {
				c.checkGenerated[n] = true
			}
		}
	}
	return c.checkFiles[name], c.checkGenerated[name]
}

// checkReturned finds a return reachable from the logging instruction at
// b.Instrs[i] that returns an error sharing an origin with what was logged.
//
// The walk is forward, and a path enters each block once. Entering a block
// resolves its φ-nodes to the edge the walk came in on, so that a value logged
// on one branch is not matched with a value returned only from another.
//
// The walk does not take a loop's exit that cannot follow the log
// (loop.Exits). In a retry loop that breaks before logging its last attempt,
// the loop cannot exit right after a log, but the walk does not follow the
// counter. It would take the exit, and report the last attempt's error, which
// was never logged. spec/loop_exit.fsl proves what the rule relies on.
//
// A branch whose condition the path has decided takes only the edge that
// agrees. The condition is decided by a branch the log sits under, by a
// branch the walk took, or by a constant, a φ-node resolved to one included.
//
// A block entered again around a loop defines its values afresh: the error a
// call returns in the next iteration is not the one logged in this one, though
// SSA names both with one value. The walk records the blocks it entered, and a
// value defined in one of them is not the value that was logged.
func (c *checker) checkReturned(fn *ssa.Function, b *ssa.BasicBlock, i int, u sinkUse) *ssa.Return {
	logAt := b.Instrs[i]
	known := checkConditions(b)
	if u.deferred {
		u.values = c.checkUncleared(logAt, u.values)
		if len(u.values) == 0 {
			return nil
		}
	}
	var logged map[walkerOrigin]bool
	if !u.deferred {
		logged = c.walkerErrs(fn, u.values, logAt, nil, nil, nil)
		maps.DeleteFunc(logged, func(v walkerOrigin, _ bool) bool { return checkKnownNil(v, known) })
		if len(logged) == 0 {
			return nil
		}
	}

	// A block is taken once for each set of decided conditions it is
	// reached with. Taking it once in all would let the first path to reach
	// it decide which returns the others see. Only conditions a branch
	// ahead can still read are kept, or every independent if after the log
	// would double the sets.
	w := &checkWalk{
		c: c, fn: fn, u: u, logBlock: b, logIndex: i, logged: logged,
		exits:  loop.Exits(b),
		reads:  c.checkReads(fn),
		keysOf: make(map[ssa.Value][]ssa.Value),
		cut:    make(map[ssa.Value]bool),
		seen:   make(map[checkSeen]bool),
		defs:   make(map[*ssa.BasicBlock]bool),
		phis:   make(map[*ssa.Phi]ssa.Value),
		prev:   make(map[*ssa.BasicBlock]*ssa.BasicBlock),
	}
	// With too many conditions decided at the log to carry, only the log's
	// own block is read.
	st, carried := w.checkRelevant(cond.KnownFrom(known), b)
	w.full = !carried
	for o := range logged {
		if in, ok := o.v.(ssa.Instruction); ok && in.Block() != nil {
			w.defs[in.Block()] = true
		}
		if o.clob != nil {
			w.defs[o.clob.Block()] = true
		}
	}
	w.phiVal = make([]ssa.Value, len(w.reads.phiIdx))
	w.seen[w.checkSeenKey(b, nil, st)] = true
	// A function where one walk did not settle is branchy enough that the
	// others will not either, and they try less before giving up.
	w.limit = checkMaxSteps
	if c.checkUnsettled[fn] {
		w.limit = checkMaxSteps / 16
	}
	if ret := w.block(b, i+1, st); ret != nil || w.steps <= w.limit {
		return ret
	}
	if c.checkUnsettled == nil {
		c.checkUnsettled = make(map[*ssa.Function]bool)
	}
	c.checkUnsettled[fn] = true
	w.limit = checkMaxSteps
	// The walk did not settle: independent conditions after the log, each
	// read again later, make more paths than it can follow. It is run
	// again taking each block once for what was logged and written, as if
	// no condition told two paths apart. The path it takes still decides
	// its conditions, so every return it reaches is reached on one
	// consistent path: a log under if verbose is not matched with a return
	// under if !verbose. It only misses what a later path would have seen.
	w.frozen, w.steps = true, 0
	st = st.Without(func(ssa.Value) bool { return false })
	w.seen = map[checkSeen]bool{w.checkSeenKey(b, nil, st): true}
	return w.block(b, i+1, st)
}

// checkWalk is one forward walk from a log call. The path is extended and
// taken back as the walk goes deeper and returns, so a step costs what the
// block it enters defines, not the length of the path.
type checkWalk struct {
	c        *checker
	fn       *ssa.Function
	u        sinkUse
	logBlock *ssa.BasicBlock
	logIndex int
	logged   map[walkerOrigin]bool
	exits    map[[2]*ssa.BasicBlock]bool
	reads    *checkReadSet
	keysOf   map[ssa.Value][]ssa.Value
	cut      map[ssa.Value]bool
	// full is set when the conditions decided at the log are too many to
	// carry. The walk then reads only the log's own block.
	full bool
	// frozen is set on the second walk, whose visited key leaves out what
	// the path decided and resolved.
	frozen bool
	// limit bounds the blocks this walk takes.
	limit int
	// phiVal holds the resolved edge of each φ-node on the path, by its
	// number in reads, for the visited key.
	phiVal []ssa.Value
	// writes is the sum of a hash of each block on the path that writes
	// memory, for the visited key. A read at a return looks back along the
	// path, so two paths that wrote differently see different memory.
	writes uint64
	seen   map[checkSeen]bool
	// defs holds the blocks that define what was logged.
	defs  map[*ssa.BasicBlock]bool
	steps int
	// phis resolves the φ-nodes of the blocks on the path.
	phis map[*ssa.Phi]ssa.Value
	// prev maps each block the path entered to the block it came from. Its
	// keys are the blocks entered, where values are defined afresh.
	prev map[*ssa.BasicBlock]*ssa.BasicBlock
}

// block walks from b.Instrs[from], with known decided, and returns the first
// return that hands back what was logged.
func (w *checkWalk) block(b *ssa.BasicBlock, from int, known cond.Known) *ssa.Return {
	// A walk that has not settled after this many blocks is given up, and
	// the log is not reported.
	if w.steps++; w.steps > w.limit {
		return nil
	}
	for _, in := range b.Instrs[from:] {
		ret, ok := in.(*ssa.Return)
		if !ok {
			continue
		}
		got, fresh := w.logged, w.prev
		along := &store.Along{From: w.logBlock, FromIndex: w.logIndex, Prev: w.prev}
		if w.u.deferred {
			// A deferred call runs at the return, so a variable it
			// captured is read there, and nothing it reads is stale.
			got, fresh = w.c.walkerErrs(w.fn, w.u.values, ret, w.phis, nil, along), nil
		}
		if w.c.checkShares(w.fn, ret, w.phis, w.u.via, got, fresh, along) {
			return ret
		}
	}
	if w.full {
		return nil
	}
	for _, s := range cond.Succs(b, w.phis, known) {
		if w.exits[[2]*ssa.BasicBlock{b, s}] {
			continue
		}
		if _, again := w.prev[s]; again || s == w.logBlock {
			// A path enters a block once. Round a loop, the fresh-value
			// rule has already judged it.
			continue
		}
		if w.frozen {
			if ret := w.enterFrozen(b, s, known); ret != nil {
				return ret
			}
			continue
		}
		// Entering s defines its values again. What was decided about a
		// condition computed from one of them held for an earlier pass.
		next := known.Without(func(v ssa.Value) bool { return w.c.checkDepends(v, s) })
		if iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If); ok && b.Succs[0] != b.Succs[1] && s != b {
			next.Decide(iff.Cond, s == b.Succs[0], w.phis)
		}
		next, carried := w.checkRelevant(next, s)
		if !carried {
			continue
		}
		key := w.checkSeenKey(s, b, next)
		if w.seen[key] {
			continue
		}
		w.seen[key] = true
		if ret := w.enter(b, s, next); ret != nil {
			return ret
		}
	}
	return nil
}

// enterFrozen is the frozen walk's step into s. Its visited key leaves the
// decisions out, so it keeps them in one map, changed on the way in and
// changed back on the way out, instead of a copy per step. Only a block
// inside a loop can be entered again, so only a condition computed from a
// value of a loop block can go stale.
func (w *checkWalk) enterFrozen(b, s *ssa.BasicBlock, known cond.Known) *ssa.Return {
	type condWas struct {
		v        ssa.Value
		was, had bool
	}
	type factWas struct {
		op  cond.Operand
		was cond.Set
		had bool
	}
	var conds []condWas
	var facts []factWas
	// A pass over what is known is paid for only in a loop, where a block
	// can go stale. It also drops what no branch ahead reads, which keeps
	// the map as small as a loop's own conditions.
	if w.c.checkInLoop(s) {
		for v, t := range known.Conds {
			if w.c.checkDepends(v, s) || !w.relevant(v, s) {
				conds = append(conds, condWas{v, t, true})
				delete(known.Conds, v)
			}
		}
		for op, set := range known.Facts {
			if w.c.checkDepends(op.Value(), s) || !w.reads.has(s, op.Value()) {
				facts = append(facts, factWas{op, set, true})
				delete(known.Facts, op)
			}
		}
	}
	if iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If); ok && b.Succs[0] != b.Succs[1] && s != b {
		if c, ok := iff.Cond.(*ssa.BinOp); ok {
			if v, _, ok := cond.FactOf(c); ok {
				if op, ok := cond.OperandOf(v, w.phis); ok {
					was, had := known.Facts[op]
					facts = append(facts, factWas{op, was, had})
				}
			}
		}
		was, had := known.Conds[iff.Cond]
		conds = append(conds, condWas{iff.Cond, was, had})
		known.Decide(iff.Cond, s == b.Succs[0], w.phis)
	}
	var ret *ssa.Return
	if key := w.checkSeenKey(s, b, known); !w.seen[key] {
		w.seen[key] = true
		ret = w.enter(b, s, known)
	}
	for _, f := range slices.Backward(facts) {
		if f.had {
			known.Facts[f.op] = f.was
		} else {
			delete(known.Facts, f.op)
		}
	}
	for _, c := range slices.Backward(conds) {
		if c.had {
			known.Conds[c.v] = c.was
		} else {
			delete(known.Conds, c.v)
		}
	}
	return ret
}

// relevant reports whether a branch reachable from b may read what test
// reads.
func (w *checkWalk) relevant(test ssa.Value, b *ssa.BasicBlock) bool {
	keys, ok := w.keysOf[test]
	if !ok {
		m := make(map[ssa.Value]bool)
		if !checkKeys(test, m, 0) {
			w.cut[test] = true
		}
		keys = slices.Collect(maps.Keys(m))
		w.keysOf[test] = keys
	}
	if w.cut[test] {
		return true
	}
	return slices.ContainsFunc(keys, func(k ssa.Value) bool { return w.reads.has(b, k) })
}

// checkInLoop reports whether b is on a cycle: it can reach itself. A cycle
// a goto makes into the middle of a loop has no header that dominates it,
// so natural loops are not enough: a block of one is entered again all the
// same.
func (c *checker) checkInLoop(b *ssa.BasicBlock) bool {
	if c.checkLoops == nil {
		c.checkLoops = make(map[*ssa.Function]map[*ssa.BasicBlock]bool)
	}
	in, ok := c.checkLoops[b.Parent()]
	if !ok {
		in = checkCycles(b.Parent())
		c.checkLoops[b.Parent()] = in
	}
	return in[b]
}

// checkCycles lists the blocks of fn that lie on a cycle, by Tarjan's
// strongly connected components over the successor graph: a block is on one
// when its component has two blocks or more, or it jumps to itself.
func checkCycles(fn *ssa.Function) map[*ssa.BasicBlock]bool {
	out := make(map[*ssa.BasicBlock]bool)
	n := len(fn.Blocks)
	index, low := slices.Repeat([]int{-1}, n), make([]int, n)
	onStack := make([]bool, n)
	var stack []*ssa.BasicBlock
	next := 0
	type frame struct {
		b *ssa.BasicBlock
		i int
	}
	for _, root := range fn.Blocks {
		if index[root.Index] >= 0 {
			continue
		}
		index[root.Index], low[root.Index] = next, next
		next++
		stack = append(stack, root)
		onStack[root.Index] = true
		frames := []frame{{b: root}}
		for len(frames) > 0 {
			f := &frames[len(frames)-1]
			if f.i < len(f.b.Succs) {
				s := f.b.Succs[f.i]
				f.i++
				if s == f.b {
					out[s] = true
				}
				if index[s.Index] < 0 {
					index[s.Index], low[s.Index] = next, next
					next++
					stack = append(stack, s)
					onStack[s.Index] = true
					frames = append(frames, frame{b: s})
				} else if onStack[s.Index] {
					low[f.b.Index] = min(low[f.b.Index], index[s.Index])
				}
				continue
			}
			v := f.b
			frames = frames[:len(frames)-1]
			if len(frames) > 0 {
				p := frames[len(frames)-1].b
				low[p.Index] = min(low[p.Index], low[v.Index])
			}
			if low[v.Index] != index[v.Index] {
				continue
			}
			var members []*ssa.BasicBlock
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w.Index] = false
				members = append(members, w)
				if w == v {
					break
				}
			}
			if len(members) > 1 {
				for _, m := range members {
					out[m] = true
				}
			}
		}
	}
	return out
}

// enter extends the path from b into s, walks s, and takes the path back.
func (w *checkWalk) enter(b, s *ssa.BasicBlock, known cond.Known) *ssa.Return {
	pred := slices.Index(s.Preds, b)
	// The path has not entered s, so none of its φ-nodes is resolved yet,
	// and taking the path back unresolves them.
	var resolved []*ssa.Phi
	for _, in := range s.Instrs {
		phi, ok := in.(*ssa.Phi)
		if !ok {
			break
		}
		if pred >= 0 && pred < len(phi.Edges) {
			resolved = append(resolved, phi)
			w.phis[phi] = phi.Edges[pred]
			w.phiVal[w.reads.phiIdx[phi]] = phi.Edges[pred]
		}
	}
	wh := w.c.checkWriteHash(s)
	w.prev[s] = b
	w.writes += wh
	ret := w.block(s, 0, known)
	w.writes -= wh
	delete(w.prev, s)
	for _, phi := range resolved {
		delete(w.phis, phi)
		w.phiVal[w.reads.phiIdx[phi]] = nil
	}
	return ret
}

// checkUncleared drops from values the variables a deferred closure clears
// after it logs them. A closure that logs err and then sets err = nil hands
// nothing back, though it runs before the function returns.
func (c *checker) checkUncleared(at ssa.Instruction, values []ssa.Value) []ssa.Value {
	// A deferred sink is always a *ssa.Defer.
	mc, ok := at.(*ssa.Defer).Call.Value.(*ssa.MakeClosure)
	if !ok {
		return values
	}
	fn := mc.Fn.(*ssa.Function)
	cleared := make(map[ssa.Value]bool)
	for j, fv := range fn.FreeVars {
		if j < len(mc.Bindings) && c.checkClears(fn, fv) {
			cleared[mc.Bindings[j]] = true
		}
	}
	var out []ssa.Value
	for _, v := range values {
		if !cleared[v] {
			out = append(out, v)
		}
	}
	return out
}

// checkClears reports whether fn logs and, around every log call, replaces
// the whole of the captured variable fv with something that does not carry
// what it held: on every path to its return after the log, or before the log
// in a block that runs whenever the log does, as e := err; err = nil; log(e)
// does. err = fmt.Errorf("%w", err) hands the error back, and is no clear.
func (c *checker) checkClears(fn *ssa.Function, fv *ssa.FreeVar) bool {
	found := false
	for _, b := range fn.Blocks {
		for i, in := range b.Instrs {
			if _, ok := c.sinkAt(in); !ok {
				continue
			}
			found = true
			if !checkClearedBefore(b, i, fv) && !checkStoresAfter(b, i+1, fv) {
				return false
			}
		}
	}
	return found
}

// checkClearedBefore reports whether a clear of fv runs before
// b.Instrs[at] whenever it runs: in b before it, or in a block that
// dominates b.
func checkClearedBefore(b *ssa.BasicBlock, at int, fv *ssa.FreeVar) bool {
	for _, r := range *fv.Referrers() {
		st, ok := r.(*ssa.Store)
		if !ok || !checkIsClear(st, fv) {
			continue
		}
		if st.Block() == b {
			if slices.Contains(b.Instrs[:at], ssa.Instruction(st)) {
				return true
			}
			continue
		}
		if st.Block().Dominates(b) {
			return true
		}
	}
	return false
}

// checkIsClear reports whether st stores to the whole of fv a value that does
// not carry what fv held.
func checkIsClear(st *ssa.Store, fv *ssa.FreeVar) bool {
	return st.Addr == fv && !checkReadsVar(st.Val, fv, make(map[ssa.Value]bool))
}

// checkReadsVar reports whether v is computed from a load of fv.
func checkReadsVar(v ssa.Value, fv *ssa.FreeVar, seen map[ssa.Value]bool) bool {
	if seen[v] || len(seen) > 256 {
		return len(seen) > 256
	}
	seen[v] = true
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL && u.X == ssa.Value(fv) {
		return true
	}
	// A value stored into a local, as the arguments of fmt.Errorf are
	// stored into the array its variadic slice is cut from, is read too.
	if a, ok := v.(*ssa.Alloc); ok {
		for _, r := range *a.Referrers() {
			if checkStoredInto(r, a, fv, seen) {
				return true
			}
		}
	}
	in, ok := v.(ssa.Instruction)
	if !ok {
		return false
	}
	for _, op := range in.Operands(nil) {
		if *op != nil && checkReadsVar(*op, fv, seen) {
			return true
		}
	}
	return false
}

// checkStoredInto reports whether r, a use of the local a, stores something
// computed from a load of fv into a, directly or through a field or an
// element of it.
func checkStoredInto(r ssa.Instruction, a *ssa.Alloc, fv *ssa.FreeVar, seen map[ssa.Value]bool) bool {
	switch r := r.(type) {
	case *ssa.Store:
		return checkReadsVar(r.Val, fv, seen)
	case *ssa.FieldAddr, *ssa.IndexAddr:
		v := r.(ssa.Value)
		for _, rr := range *v.Referrers() {
			if st, ok := rr.(*ssa.Store); ok && st.Addr == v && checkReadsVar(st.Val, fv, seen) {
				return true
			}
		}
	}
	return false
}

// checkStoresAfter reports whether every path from b.Instrs[from] to a
// return clears fv.
func checkStoresAfter(b *ssa.BasicBlock, from int, fv *ssa.FreeVar) bool {
	seen := make(map[*ssa.BasicBlock]bool)
	var clear func(b *ssa.BasicBlock, from int) bool
	clear = func(b *ssa.BasicBlock, from int) bool {
		for _, in := range b.Instrs[from:] {
			if st, ok := in.(*ssa.Store); ok && checkIsClear(st, fv) {
				return true
			}
			if _, ok := in.(*ssa.Return); ok {
				return false
			}
		}
		// A block with no successor that does not return panics, and
		// hands nothing back.
		for _, s := range b.Succs {
			if seen[s] {
				continue
			}
			seen[s] = true
			if !clear(s, 0) {
				return false
			}
		}
		return true
	}
	return clear(b, from)
}

// checkIn reports whether the path entered block b.
func checkIn(prev map[*ssa.BasicBlock]*ssa.BasicBlock, b *ssa.BasicBlock) bool {
	_, ok := prev[b]
	return ok
}

// checkReadSet is, for each block of a function, what the walk from there on
// can still depend on. reads holds the values a branch reachable from the
// block reads to decide its condition: the condition itself, what it compares
// with a constant, what it negates, and the edges of a φ-node it is. live
// holds the φ-nodes that an instruction reachable from the block may read,
// directly or through what is computed from them. Values are numbered, and
// each block holds a bit set of each.
type checkReadSet struct {
	idx    map[ssa.Value]int
	reads  [][]uint64
	phiIdx map[*ssa.Phi]int
	live   [][]uint64
}

// has reports whether a branch reachable from b reads v. Bit 0 stands for a
// branch whose condition checkKeys could not read in full, which may read
// anything.
func (r *checkReadSet) has(b *ssa.BasicBlock, v ssa.Value) bool {
	if r.reads[b.Index][0]&1 != 0 {
		return true
	}
	i, ok := r.idx[v]
	return ok && r.reads[b.Index][i/64]&(1<<(i%64)) != 0
}

// lives reports whether an instruction reachable from b may read phi.
func (r *checkReadSet) lives(b *ssa.BasicBlock, phi *ssa.Phi) bool {
	i, ok := r.phiIdx[phi]
	return ok && r.live[b.Index][i/64]&(1<<(i%64)) != 0
}

// checkReads computes the read set of fn, once per function.
func (c *checker) checkReads(fn *ssa.Function) *checkReadSet {
	if c.checkReadsOf == nil {
		c.checkReadsOf = make(map[*ssa.Function]*checkReadSet)
	}
	if r, ok := c.checkReadsOf[fn]; ok {
		return r
	}
	// Index 0 is taken by the bit for a condition read in part.
	r := &checkReadSet{idx: map[ssa.Value]int{nil: 0}, phiIdx: make(map[*ssa.Phi]int)}
	own := make([][]int, len(fn.Blocks))
	for _, b := range fn.Blocks {
		if iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If); ok {
			keys := make(map[ssa.Value]bool)
			if !checkKeys(iff.Cond, keys, 0) {
				own[b.Index] = append(own[b.Index], 0)
			}
			for k := range keys {
				if _, ok := r.idx[k]; !ok {
					r.idx[k] = len(r.idx)
				}
				own[b.Index] = append(own[b.Index], r.idx[k])
			}
		}
	}
	r.reads = checkSpread(fn, len(r.idx), own)

	// deps[v] is the φ-nodes v is computed from. The values that depend on
	// each other, as a loop's φ-nodes do, form a strongly connected
	// component and share one set. Components are met operands first, so
	// each set is built once from finished ones.
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			if phi, ok := in.(*ssa.Phi); ok {
				r.phiIdx[phi] = len(r.phiIdx)
			}
		}
	}
	words := (len(r.phiIdx) + 63) / 64
	deps := checkPhiDeps(fn, r.phiIdx, words)
	depsOf := func(v ssa.Value) []uint64 {
		if d, ok := deps[v]; ok {
			return d
		}
		return make([]uint64, words)
	}
	var rands []*ssa.Value
	ownLive := make([][]int, len(fn.Blocks))
	for _, b := range fn.Blocks {
		seen := make([]uint64, words)
		for _, in := range b.Instrs {
			for _, op := range in.Operands(rands[:0]) {
				if *op == nil {
					continue
				}
				for w, x := range depsOf(*op) {
					seen[w] |= x
				}
			}
		}
		for w, x := range seen {
			for x != 0 {
				i := bits.TrailingZeros64(x)
				ownLive[b.Index] = append(ownLive[b.Index], w*64+i)
				x &= x - 1
			}
		}
	}
	// A φ-node stored into memory is read back by a later load wherever it
	// was stored, so it is live everywhere.
	stored := make([]uint64, words)
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			var v ssa.Value
			switch in := in.(type) {
			case *ssa.Store:
				v = in.Val
			case *ssa.MapUpdate:
				v = in.Value
			case *ssa.Send:
				v = in.X
			default:
				continue
			}
			for w, x := range depsOf(v) {
				stored[w] |= x
			}
		}
	}
	for _, b := range fn.Blocks {
		for w, x := range stored {
			for x != 0 {
				ownLive[b.Index] = append(ownLive[b.Index], w*64+bits.TrailingZeros64(x))
				x &= x - 1
			}
		}
	}
	r.live = checkSpread(fn, len(r.phiIdx), ownLive)
	c.checkReadsOf[fn] = r
	return r
}

// checkPhiDeps finds, for each value of fn, the φ-nodes it is computed from,
// by Tarjan's strongly connected components over the operand graph. Tarjan
// finishes a component after every component its members use, so each
// component's set is the union of its own φ-nodes and of finished sets.
func checkPhiDeps(fn *ssa.Function, phiIdx map[*ssa.Phi]int, words int) map[ssa.Value][]uint64 {
	deps := make(map[ssa.Value][]uint64)
	index := make(map[ssa.Value]int)
	low := make(map[ssa.Value]int)
	onStack := make(map[ssa.Value]bool)
	var stack []ssa.Value
	next := 0
	operands := func(v ssa.Value) []ssa.Value {
		in, ok := v.(ssa.Instruction)
		if !ok {
			return nil
		}
		var out []ssa.Value
		for _, op := range in.Operands(nil) {
			if *op != nil {
				out = append(out, *op)
			}
		}
		return out
	}
	type frame struct {
		v   ssa.Value
		ops []ssa.Value
		i   int
	}
	visit := func(root ssa.Value) {
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true
		frames := []frame{{v: root, ops: operands(root)}}
		for len(frames) > 0 {
			f := &frames[len(frames)-1]
			if f.i < len(f.ops) {
				w := f.ops[f.i]
				f.i++
				if _, seen := index[w]; !seen {
					index[w], low[w] = next, next
					next++
					stack = append(stack, w)
					onStack[w] = true
					frames = append(frames, frame{v: w, ops: operands(w)})
				} else if onStack[w] {
					low[f.v] = min(low[f.v], index[w])
				}
				continue
			}
			v := f.v
			frames = frames[:len(frames)-1]
			if len(frames) > 0 {
				p := frames[len(frames)-1].v
				low[p] = min(low[p], low[v])
			}
			if low[v] != index[v] {
				continue
			}
			// v roots a component. Pop it, and build its one set.
			var members []ssa.Value
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				members = append(members, w)
				if w == v {
					break
				}
			}
			set := make([]uint64, words)
			for _, m := range members {
				if phi, ok := m.(*ssa.Phi); ok {
					i := phiIdx[phi]
					set[i/64] |= 1 << (i % 64)
				}
				for _, op := range operands(m) {
					if d, ok := deps[op]; ok {
						for w, x := range d {
							set[w] |= x
						}
					}
				}
			}
			for _, m := range members {
				deps[m] = set
			}
		}
	}
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			if v, ok := in.(ssa.Value); ok {
				if _, seen := index[v]; !seen {
					visit(v)
				}
			}
		}
	}
	return deps
}

// checkSpread builds, for each block of fn, the bit set of what its own list
// holds and what every block it reaches holds. A change to one block's set is
// pushed to its predecessors until nothing changes.
func checkSpread(fn *ssa.Function, n int, own [][]int) [][]uint64 {
	words := (n + 63) / 64
	bits := make([][]uint64, len(fn.Blocks))
	for _, b := range fn.Blocks {
		bits[b.Index] = make([]uint64, words)
		for _, i := range own[b.Index] {
			bits[b.Index][i/64] |= 1 << (i % 64)
		}
	}
	work := slices.Clone(fn.Blocks)
	queued := slices.Repeat([]bool{true}, len(fn.Blocks))
	for len(work) > 0 {
		s := work[len(work)-1]
		work = work[:len(work)-1]
		queued[s.Index] = false
		for _, p := range s.Preds {
			changed := false
			for w := range words {
				if x := bits[p.Index][w] | bits[s.Index][w]; x != bits[p.Index][w] {
					bits[p.Index][w] = x
					changed = true
				}
			}
			if changed && !queued[p.Index] {
				queued[p.Index] = true
				work = append(work, p)
			}
		}
	}
	return bits
}

// checkKeys adds the values a decision on test reads to keys, and reports
// whether it read them all. Past a depth it stops, and the caller treats the
// condition as read everywhere. A compared operand is added in the form
// cond.Same reads it too, so that two reads of len(s) share a key.
func checkKeys(test ssa.Value, keys map[ssa.Value]bool, depth int) bool {
	if keys[test] {
		return true
	}
	if depth > cond.MaxDepth {
		return false
	}
	keys[test] = true
	switch x := test.(type) {
	case *ssa.UnOp:
		if x.Op == token.NOT {
			return checkKeys(x.X, keys, depth+1)
		}
	case *ssa.BinOp:
		if v, _, ok := cond.FactOf(x); ok {
			keys[cond.Canon(v)] = true
			return checkKeys(v, keys, depth+1)
		}
	case *ssa.Phi:
		all := true
		for _, e := range x.Edges {
			all = checkKeys(e, keys, depth+1) && all
		}
		return all
	}
	return true
}

// checkRelevant keeps the decided conditions that share a value with what a
// branch reachable from b reads. w.keysOf caches what each condition reads,
// and w.cut holds the conditions checkKeys could not read in full, which are
// kept wherever the walk goes.
//
// It reports false when more than checkMaxKnown are left. Then the path is
// given up: forgetting a condition would let the walk take the very edge
// that condition rules out, and report through it.
func (w *checkWalk) checkRelevant(known cond.Known, b *ssa.BasicBlock) (cond.Known, bool) {
	out := cond.Known{Conds: make(map[ssa.Value]bool, len(known.Conds)), Facts: make(map[cond.Operand]cond.Set, len(known.Facts))}
	for test, v := range known.Conds {
		if w.relevant(test, b) {
			out.Conds[test] = v
		}
	}
	for op, set := range known.Facts {
		if w.reads.has(b, op.Value()) {
			out.Facts[op] = set
		}
	}
	// The frozen walk's key leaves the conditions out, so they cannot
	// multiply its states, and it carries every one.
	return out, w.frozen || len(out.Conds)+len(out.Facts) <= checkMaxKnown
}

// checkMaxKnown bounds the values the decided conditions of a walk test.
const checkMaxKnown = 32

// checkWriteHash is a hash of b when it writes memory, itself or through a
// call, and 0 otherwise. A store into the array a variadic call is passed is
// not a write that matters: nothing reads it but the call.
func (c *checker) checkWriteHash(b *ssa.BasicBlock) uint64 {
	if c.checkWrites == nil {
		c.checkWrites = make(map[*ssa.BasicBlock]uint64)
	}
	if h, ok := c.checkWrites[b]; ok {
		return h
	}
	var h uint64
	for _, in := range b.Instrs {
		switch in := in.(type) {
		case *ssa.Store:
			if a, ok := checkRootAlloc(in.Addr); ok && a.Comment == "varargs" {
				continue
			}
		case *ssa.MapUpdate, *ssa.Send:
		case ssa.CallInstruction:
			// A call that writes through what it is handed, as a
			// method that clears a field does.
			if len(c.calleeWrites(in.Common())) == 0 {
				continue
			}
		default:
			continue
		}
		x := uint64(reflect.ValueOf(b).Pointer()) ^ 0x94d049bb133111eb
		x ^= x >> 31
		x *= 0xbf58476d1ce4e5b9
		h = x ^ x>>29
		break
	}
	c.checkWrites[b] = h
	return h
}

// checkRootAlloc is the local an address starts from, if it is one.
func checkRootAlloc(addr ssa.Value) (*ssa.Alloc, bool) {
	root, _ := store.Locate(addr)
	a, ok := root.(*ssa.Alloc)
	return a, ok
}

// checkSeen names a block and a hash of what the walk from there depends on:
// the decided conditions and facts, the φ-nodes a later instruction may read
// with the edges the path resolved them to, the block's own φ-nodes with the
// edges entering it resolves, and the blocks defining what was logged that
// the path entered. Two paths that agree on all of these see the same
// returns, whichever block they came in from. Two sets that hash alike are
// taken as one, which can only cut a walk short and leave a report out.
type checkSeen struct {
	b    *ssa.BasicBlock
	hash uint64
}

// checkSeenKey is the checkSeen of the walk entering s from pred with known.
// pred is nil at the log's own block.
func (w *checkWalk) checkSeenKey(s, pred *ssa.BasicBlock, known cond.Known) checkSeen {
	var h uint64
	mix := func(x uint64) {
		// The sum of a hash per entry is the same in any order.
		x ^= x >> 33
		x *= 0xff51afd7ed558ccd
		x ^= x >> 33
		h += x
	}
	ptr := func(v any) uint64 { return uint64(reflect.ValueOf(v).Pointer()) }
	for v, t := range known.Conds {
		if w.frozen {
			break
		}
		x := ptr(v)
		if t {
			x ^= 0x9e3779b97f4a7c15
		}
		mix(x)
	}
	for op, set := range known.Facts {
		if w.frozen {
			break
		}
		x := set.Mix(ptr(op.Value())*131 + uint64(op.Kind()))
		mix(x ^ 0x7c3b1f2e9a4d6581)
	}
	// Only the φ-nodes live at s are read, from its bit set. The frozen
	// walk leaves them out with the decisions.
	for word, x := range w.reads.live[s.Index] {
		if w.frozen {
			break
		}
		for x != 0 {
			i := word*64 + bits.TrailingZeros64(x)
			x &= x - 1
			if e := w.phiVal[i]; e != nil {
				mix(uint64(i)*31 ^ ptr(e) ^ 0x2545f4914f6cdd1d)
			}
		}
	}
	if pred != nil && !w.frozen {
		if j := slices.Index(s.Preds, pred); j >= 0 {
			for _, in := range s.Instrs {
				phi, ok := in.(*ssa.Phi)
				if !ok {
					break
				}
				if w.reads.lives(s, phi) {
					mix(uint64(w.reads.phiIdx[phi])*31 ^ ptr(phi.Edges[j]) ^ 0x2545f4914f6cdd1d)
				}
			}
		}
	}
	for d := range w.defs {
		if _, ok := w.prev[d]; ok || d == s {
			mix(ptr(d) ^ 0x632be59bd9b4e019)
		}
	}
	// The frozen walk takes each block once for what was logged, as the
	// first version of the walk did: the writes on the path multiply like
	// the decisions do.
	if !w.frozen {
		h += w.writes + w.c.checkWriteHash(s)
	}
	return checkSeen{b: s, hash: h}
}

// checkDep keys what checkDepends has found.
type checkDep struct {
	v ssa.Value
	b *ssa.BasicBlock
}

// checkDepends reports whether v is computed from a value that block s
// defines, once for each pair.
func (c *checker) checkDepends(v ssa.Value, s *ssa.BasicBlock) bool {
	if c.checkDeps == nil {
		c.checkDeps = make(map[checkDep]bool)
	}
	k := checkDep{v, s}
	d, ok := c.checkDeps[k]
	if !ok {
		d = checkDependsOn(v, s, make(map[ssa.Value]bool))
		c.checkDeps[k] = d
	}
	return d
}

// checkDependsOn reports whether v is computed from a value that block s
// defines. Past a bound it answers yes, which forgets the condition.
func checkDependsOn(v ssa.Value, s *ssa.BasicBlock, seen map[ssa.Value]bool) bool {
	if seen[v] {
		return false
	}
	if len(seen) > 256 {
		return true
	}
	seen[v] = true
	in, ok := v.(ssa.Instruction)
	if !ok {
		return false
	}
	if in.Block() == s {
		return true
	}
	for _, op := range in.Operands(nil) {
		if *op != nil && checkDependsOn(*op, s, seen) {
			return true
		}
	}
	return false
}

// checkKnownNil reports whether the decided conditions say that v is nil: a
// branch the log sits under compared it with nil.
func checkKnownNil(o walkerOrigin, known map[ssa.Value]bool) bool {
	if o.path != "" {
		return false
	}
	v := o.v
	for test, taken := range known {
		cmp, ok := test.(*ssa.BinOp)
		if !ok || (cmp.Op != token.EQL && cmp.Op != token.NEQ) {
			continue
		}
		x := cmp.X
		if checkIsNil(x) {
			x = cmp.Y
		} else if !checkIsNil(cmp.Y) {
			continue
		}
		if (cmp.Op == token.EQL) == taken && checkStrip(x) == checkStrip(v) {
			return true
		}
	}
	return false
}

// checkStrip strips conversions to an interface, which a comparison with nil
// puts around an error of a concrete type.
func checkStrip(v ssa.Value) ssa.Value {
	for {
		switch x := v.(type) {
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeInterface:
			v = x.X
		default:
			return v
		}
	}
}

func checkIsNil(v ssa.Value) bool {
	k, ok := v.(*ssa.Const)
	return ok && k.IsNil()
}

// checkLive lists the blocks of fn that can run: those reached from the entry
// without taking a branch whose condition is a constant the other way. Code
// under if debug, with debug a false constant, never runs.
func checkLive(fn *ssa.Function) map[*ssa.BasicBlock]bool {
	live := make(map[*ssa.BasicBlock]bool)
	if len(fn.Blocks) == 0 {
		return live
	}
	stack := []*ssa.BasicBlock{fn.Blocks[0]}
	live[fn.Blocks[0]] = true
	for len(stack) > 0 {
		b := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, s := range cond.Succs(b, nil, cond.Known{}) {
			if !live[s] {
				live[s] = true
				stack = append(stack, s)
			}
		}
	}
	// A function's recover block is entered by a panic, not by an edge.
	if r := fn.Recover; r != nil && !live[r] {
		live[r] = true
	}
	return live
}

// checkShares reports whether an error result of ret shares an origin with
// logged. A value defined in a block of fresh was defined again after it was
// logged, and does not count.
func (c *checker) checkShares(fn *ssa.Function, ret *ssa.Return, phis map[*ssa.Phi]ssa.Value, via ssa.Value, logged map[walkerOrigin]bool, fresh map[*ssa.BasicBlock]*ssa.BasicBlock, along *store.Along) bool {
	if len(logged) == 0 {
		return false
	}
	for _, r := range ret.Results {
		if !typeutil.IsError(r.Type()) {
			continue
		}
		for v := range c.walkerErrs(fn, []ssa.Value{r}, ret, phis, via, along) {
			if !logged[v] {
				continue
			}
			// An origin a block the path entered defines again is new:
			// a value, or the memory of a local declared there.
			if in, ok := v.v.(ssa.Instruction); ok && v.clob == nil && checkIn(fresh, in.Block()) {
				continue
			}
			if v.clob != nil && checkIn(fresh, v.clob.Block()) {
				continue
			}
			return true
		}
	}
	return false
}

// checkReportable reports whether a diagnostic at pos would be shown: it is in
// a file of this pass that is not generated.
func (c *checker) checkReportable(pos token.Pos) bool {
	if !pos.IsValid() {
		return false
	}
	f, generated := c.checkFile(c.pass.Fset.PositionFor(pos, false).Filename)
	return f != nil && !generated
}

// checkReport reports the logging instruction, anchored on the statement that
// holds it, so that an ignore directive on the line above a multi-line chain
// covers it.
func (c *checker) checkReport(instr ssa.Instruction, ret *ssa.Return, u sinkUse) {
	pos := c.checkStmt(instr.Pos())
	if c.directives.Ignored(pos) {
		return
	}
	// Adjusted, as the driver prints the report's own position.
	line := c.pass.Fset.Position(ret.Pos()).Line
	msg := "error is logged here and also returned at line %d; log it or return it, not both"
	args := []any{line}
	if u.by != "" {
		msg = "error is logged by %s and also returned at line %d; log it or return it, not both"
		args = []any{u.by, line}
	}
	d := analysis.Diagnostic{Pos: pos, Message: fmt.Sprintf(msg, args...)}
	if ret.Pos().IsValid() {
		d.Related = []analysis.RelatedInformation{{Pos: ret.Pos(), Message: "returned here"}}
	}
	c.pass.Report(d)
}

// checkStmt is the start of the statement enclosing pos.
func (c *checker) checkStmt(pos token.Pos) token.Pos {
	f, _ := c.checkFile(c.pass.Fset.PositionFor(pos, false).Filename)
	if f == nil {
		return pos
	}
	path, _ := astutil.PathEnclosingInterval(f, pos, pos)
	for _, n := range path {
		if s, ok := n.(ast.Stmt); ok {
			if _, block := s.(*ast.BlockStmt); !block {
				return s.Pos()
			}
		}
	}
	return pos
}

// checkMaxSteps bounds the blocks one walk from a log takes.
const checkMaxSteps = 1 << 12

// checkConditions lists the conditions decided at b by the branches it sits
// under: a block reached only from one edge of a branch, and dominating b,
// fixes that branch's condition.
func checkConditions(b *ssa.BasicBlock) map[ssa.Value]bool {
	known := make(map[ssa.Value]bool)
	for d := b; d != nil; d = d.Idom() {
		if len(d.Preds) != 1 {
			continue
		}
		p := d.Preds[0]
		iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok || p.Succs[0] == p.Succs[1] {
			continue
		}
		if _, ok := known[iff.Cond]; !ok {
			known[iff.Cond] = d == p.Succs[0]
		}
	}
	return known
}
