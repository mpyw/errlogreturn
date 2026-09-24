package internal

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"math/bits"
	"reflect"
	"slices"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/ssa"

	"github.com/mpyw/errlogreturn/internal/store"
	"github.com/mpyw/errlogreturn/internal/typeutil"
)

// checkBook indexes the pass's files for reporting.
//
//declscope:package
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

// checkFile returns the syntax of the named file, and whether it is
// generated. It is nil for a file the pass does not hold.
func (c *checker) checkFile(name string) (*ast.File, bool) {
	if c.checkFiles == nil {
		c.checkFiles = make(map[string]*ast.File)
		c.checkGenerated = make(map[string]bool)
		for _, f := range c.pass.Files {
			n := c.pass.Fset.Position(f.Pos()).Filename
			c.checkFiles[n] = f
			if ast.IsGenerated(f) {
				c.checkGenerated[n] = true
			}
		}
	}
	return c.checkFiles[name], c.checkGenerated[name]
}

// check reports every place in fn where an error is logged and then returned
// on the same path.
//
//declscope:package
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

// checkReturned finds a return reachable from the logging instruction at
// b.Instrs[i] that returns an error sharing an origin with what was logged.
//
// The walk is forward, and a path enters each block once. Entering a block
// resolves its φ-nodes to the edge the walk came in on, so that a value logged
// on one branch is not matched with a value returned only from another.
//
// The walk does not take a loop's exit that cannot follow the log
// (checkExits). In a retry loop that breaks before logging its last attempt,
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
		for v := range logged {
			if checkKnownNil(v, known) {
				delete(logged, v)
			}
		}
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
		exits:  checkExits(b),
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
	st, carried := w.checkRelevant(checkKnownFrom(known), b)
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
	st = st.without(func(ssa.Value) bool { return false })
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
func (w *checkWalk) block(b *ssa.BasicBlock, from int, known checkKnown) *ssa.Return {
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
	for _, s := range checkSuccs(b, w.phis, known) {
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
		next := known.without(func(v ssa.Value) bool { return w.c.checkDepends(v, s) })
		if iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If); ok && b.Succs[0] != b.Succs[1] && s != b {
			next.decide(iff.Cond, s == b.Succs[0], w.phis)
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
func (w *checkWalk) enterFrozen(b, s *ssa.BasicBlock, known checkKnown) *ssa.Return {
	type condWas struct {
		v        ssa.Value
		was, had bool
	}
	type factWas struct {
		op  checkOperand
		was checkSet
		had bool
	}
	var conds []condWas
	var facts []factWas
	// A pass over what is known is paid for only in a loop, where a block
	// can go stale. It also drops what no branch ahead reads, which keeps
	// the map as small as a loop's own conditions.
	if w.c.checkInLoop(s) {
		for v, t := range known.conds {
			if w.c.checkDepends(v, s) || !w.relevant(v, s) {
				conds = append(conds, condWas{v, t, true})
				delete(known.conds, v)
			}
		}
		for op, set := range known.facts {
			if w.c.checkDepends(op.v, s) || !w.reads.has(s, op.v) {
				facts = append(facts, factWas{op, set, true})
				delete(known.facts, op)
			}
		}
	}
	if iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If); ok && b.Succs[0] != b.Succs[1] && s != b {
		if c, ok := iff.Cond.(*ssa.BinOp); ok {
			if v, _, ok := checkFactOf(c); ok {
				if op, ok := checkOperandOf(v, w.phis); ok {
					was, had := known.facts[op]
					facts = append(facts, factWas{op, was, had})
				}
			}
		}
		was, had := known.conds[iff.Cond]
		conds = append(conds, condWas{iff.Cond, was, had})
		known.decide(iff.Cond, s == b.Succs[0], w.phis)
	}
	var ret *ssa.Return
	if key := w.checkSeenKey(s, b, known); !w.seen[key] {
		w.seen[key] = true
		ret = w.enter(b, s, known)
	}
	for i := len(facts) - 1; i >= 0; i-- {
		if f := facts[i]; f.had {
			known.facts[f.op] = f.was
		} else {
			delete(known.facts, f.op)
		}
	}
	for i := len(conds) - 1; i >= 0; i-- {
		if c := conds[i]; c.had {
			known.conds[c.v] = c.was
		} else {
			delete(known.conds, c.v)
		}
	}
	return ret
}

// relevant reports whether a branch reachable from b may read what cond
// reads.
func (w *checkWalk) relevant(cond ssa.Value, b *ssa.BasicBlock) bool {
	keys, ok := w.keysOf[cond]
	if !ok {
		m := make(map[ssa.Value]bool)
		if !checkKeys(cond, m, 0) {
			w.cut[cond] = true
		}
		for k := range m {
			keys = append(keys, k)
		}
		w.keysOf[cond] = keys
	}
	if w.cut[cond] {
		return true
	}
	for _, k := range keys {
		if w.reads.has(b, k) {
			return true
		}
	}
	return false
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
	index, low := make([]int, n), make([]int, n)
	for i := range index {
		index[i] = -1
	}
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
func (w *checkWalk) enter(b, s *ssa.BasicBlock, known checkKnown) *ssa.Return {
	pred := -1
	for j, p := range s.Preds {
		if p == b {
			pred = j
			break
		}
	}
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
			for _, in := range b.Instrs[:at] {
				if in == ssa.Instruction(st) {
					return true
				}
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
	work := append([]*ssa.BasicBlock(nil), fn.Blocks...)
	queued := make([]bool, len(fn.Blocks))
	for i := range queued {
		queued[i] = true
	}
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

// checkKeys adds the values a decision on cond reads to keys, and reports
// whether it read them all. Past a depth it stops, and the caller treats the
// condition as read everywhere. A compared operand is added in the form
// checkSame reads it too, so that two reads of len(s) share a key.
func checkKeys(cond ssa.Value, keys map[ssa.Value]bool, depth int) bool {
	if keys[cond] {
		return true
	}
	if depth > checkMaxDepth {
		return false
	}
	keys[cond] = true
	switch x := cond.(type) {
	case *ssa.UnOp:
		if x.Op == token.NOT {
			return checkKeys(x.X, keys, depth+1)
		}
	case *ssa.BinOp:
		if v, _, ok := checkFactOf(x); ok {
			keys[checkCanon(v)] = true
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

// checkMaxDepth bounds how deep checkKeys and checkEval read a condition.
const checkMaxDepth = 16

// checkRelevant keeps the decided conditions that share a value with what a
// branch reachable from b reads. w.keysOf caches what each condition reads,
// and w.cut holds the conditions checkKeys could not read in full, which are
// kept wherever the walk goes.
//
// It reports false when more than checkMaxKnown are left. Then the path is
// given up: forgetting a condition would let the walk take the very edge
// that condition rules out, and report through it.
func (w *checkWalk) checkRelevant(known checkKnown, b *ssa.BasicBlock) (checkKnown, bool) {
	out := checkKnown{conds: make(map[ssa.Value]bool, len(known.conds)), facts: make(map[checkOperand]checkSet, len(known.facts))}
	for cond, v := range known.conds {
		if w.relevant(cond, b) {
			out.conds[cond] = v
		}
	}
	for op, set := range known.facts {
		if w.reads.has(b, op.v) {
			out.facts[op] = set
		}
	}
	// The frozen walk's key leaves the conditions out, so they cannot
	// multiply its states, and it carries every one.
	return out, w.frozen || len(out.conds)+len(out.facts) <= checkMaxKnown
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
func (w *checkWalk) checkSeenKey(s, pred *ssa.BasicBlock, known checkKnown) checkSeen {
	var h uint64
	mix := func(x uint64) {
		// The sum of a hash per entry is the same in any order.
		x ^= x >> 33
		x *= 0xff51afd7ed558ccd
		x ^= x >> 33
		h += x
	}
	ptr := func(v any) uint64 { return uint64(reflect.ValueOf(v).Pointer()) }
	for v, t := range known.conds {
		if w.frozen {
			break
		}
		x := ptr(v)
		if t {
			x ^= 0x9e3779b97f4a7c15
		}
		mix(x)
	}
	for op, set := range known.facts {
		if w.frozen {
			break
		}
		x := ptr(op.v)*131 + uint64(op.kind)
		for _, r := range set.iv {
			x = x*1000003 ^ uint64(r[0])
			x = x*1000003 ^ uint64(r[1])
		}
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
		for j, p := range s.Preds {
			if p != pred {
				continue
			}
			for _, in := range s.Instrs {
				phi, ok := in.(*ssa.Phi)
				if !ok {
					break
				}
				if w.reads.lives(s, phi) {
					mix(uint64(w.reads.phiIdx[phi])*31 ^ ptr(phi.Edges[j]) ^ 0x2545f4914f6cdd1d)
				}
			}
			break
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

// checkExits lists the loop exits the walk from a log in block b does not
// take. A loop ends by a test of its counter, and each test ends it at one
// value of the counter. The exit is taken right after a log only when the log
// runs in that iteration. So an exit is kept when its value is the last one
// on which the log can run, and every branch that decides the log leads to it
// there. Every other exit of a counted loop is dropped. A retry loop that
// breaks before logging its last attempt never exits right after a log.
func checkExits(b *ssa.BasicBlock) map[[2]*ssa.BasicBlock]bool {
	out := make(map[[2]*ssa.BasicBlock]bool)
	for h := b; h != nil; h = h.Idom() {
		body := checkBody(h)
		if !body[b] {
			continue
		}
		for _, in := range h.Instrs {
			phi, ok := in.(*ssa.Phi)
			if !ok {
				break
			}
			lc, ok := checkCounter(h, b, phi, body)
			if !ok {
				continue
			}
			// A loop with one test ends only by it, on whatever value. A
			// log that no branch on the counter decides runs on that one.
			if len(lc.tests) == 1 && !checkReadsCounter(h, b, phi, lc, body) {
				continue
			}
			for _, t := range lc.tests {
				if !lc.known || t.at.off != lc.last.off || !checkLogsAt(h, b, phi, lc, body) {
					out[t.exit] = true
				}
			}
		}
	}
	return out
}

// checkBody lists the blocks of the loop headed by h: those h dominates that
// lead back to it. It is empty when h heads no loop.
func checkBody(h *ssa.BasicBlock) map[*ssa.BasicBlock]bool {
	body := make(map[*ssa.BasicBlock]bool)
	var stack []*ssa.BasicBlock
	for _, p := range h.Preds {
		if h.Dominates(p) && !body[p] {
			body[p] = true
			stack = append(stack, p)
		}
	}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if x == h {
			continue
		}
		for _, p := range x.Preds {
			if h.Dominates(p) && !body[p] {
				body[p] = true
				stack = append(stack, p)
			}
		}
	}
	if len(body) > 0 {
		body[h] = true
	}
	return body
}

// checkReadsCounter reports whether a branch inside the loop headed by h,
// other than the loop's own tests, reads the counter phi or another counter
// of the loop to decide the log block b.
func checkReadsCounter(h, b *ssa.BasicBlock, phi *ssa.Phi, lc checkLoop, body map[*ssa.BasicBlock]bool) bool {
	for d := b; d != nil && d != h; d = d.Idom() {
		if len(d.Preds) != 1 {
			continue
		}
		p := d.Preds[0]
		iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok || lc.isTest[p] || !h.Dominates(p) || p.Succs[0] == p.Succs[1] {
			continue
		}
		if checkUses(iff.Cond, phi, make(map[ssa.Value]bool)) || checkOtherCounter(h, phi, iff.Cond, body) {
			return true
		}
	}
	return false
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

// checkOtherCounter reports whether cond reads a φ-node of h other than phi
// that steps by a constant on every back edge: another counter of the loop.
func checkOtherCounter(h *ssa.BasicBlock, phi *ssa.Phi, cond ssa.Value, body map[*ssa.BasicBlock]bool) bool {
	for _, in := range h.Instrs {
		other, ok := in.(*ssa.Phi)
		if !ok {
			break
		}
		if other == phi || !checkUses(cond, other, make(map[ssa.Value]bool)) {
			continue
		}
		steps := true
		for j, e := range other.Edges {
			if j < len(h.Preds) && body[h.Preds[j]] {
				if l := checkLinear(e); l.base != other || l.off == 0 {
					steps = false
				}
			}
		}
		if steps {
			return true
		}
	}
	return false
}

// checkLogsAt reports whether the log block b may run on the last iteration
// of the loop headed by h, whose counter is phi: each branch inside the loop
// that decides b and reads the counter, other than the loop's own tests, leads
// to b at the counter's last value, or may for some bound. A branch the rule
// cannot read, such as i%2 == 0, is taken to lead away, which leans toward
// silence.
func checkLogsAt(h, b *ssa.BasicBlock, phi *ssa.Phi, lc checkLoop, body map[*ssa.BasicBlock]bool) bool {
	for d := b; d != nil && d != h; d = d.Idom() {
		if len(d.Preds) != 1 {
			continue
		}
		p := d.Preds[0]
		iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok || lc.isTest[p] || !h.Dominates(p) || p.Succs[0] == p.Succs[1] {
			continue
		}
		// A guard on another counter of the loop, as j > 1 is when j
		// counts down beside i, cannot be read at i's last value.
		if checkOtherCounter(h, phi, iff.Cond, body) {
			return false
		}
		if !checkUses(iff.Cond, phi, make(map[ssa.Value]bool)) {
			continue
		}
		v, ok, open := checkAt(iff.Cond, phi, lc.last)
		if open {
			// The branch leads to the log for some bound. The walk
			// follows it there, as it follows any branch it cannot rule
			// out.
			continue
		}
		if !ok || v != (d == p.Succs[0]) {
			return false
		}
	}
	return true
}

// checkUses reports whether v is computed from phi, through arithmetic,
// comparisons and conversions.
func checkUses(v ssa.Value, phi *ssa.Phi, seen map[ssa.Value]bool) bool {
	if v == ssa.Value(phi) {
		return true
	}
	if seen[v] || len(seen) > 64 {
		return false
	}
	seen[v] = true
	switch x := v.(type) {
	case *ssa.BinOp:
		return checkUses(x.X, phi, seen) || checkUses(x.Y, phi, seen)
	case *ssa.UnOp:
		return x.Op != token.MUL && x.Op != token.ARROW && checkUses(x.X, phi, seen)
	case *ssa.Convert:
		return checkUses(x.X, phi, seen)
	case *ssa.ChangeType:
		return checkUses(x.X, phi, seen)
	case *ssa.Phi:
		// A counter changed in the body is a φ-node made from it.
		for _, e := range x.Edges {
			if checkUses(e, phi, seen) {
				return true
			}
		}
	}
	return false
}

// checkLin is a value in linear form: base plus off, or off alone when base
// is nil.
type checkLin struct {
	base ssa.Value
	off  int64
}

// checkLinear reads v as a base plus a constant.
func checkLinear(v ssa.Value) checkLin {
	switch x := v.(type) {
	case *ssa.Const:
		if x.Value != nil && x.Value.Kind() == constant.Int {
			if n, exact := constant.Int64Val(x.Value); exact {
				return checkLin{off: n}
			}
		}
	case *ssa.Convert:
		return checkLinear(x.X)
	case *ssa.BinOp:
		l, r := checkLinear(x.X), checkLinear(x.Y)
		switch {
		case x.Op == token.ADD && r.base == nil:
			return checkLin{base: l.base, off: l.off + r.off}
		case x.Op == token.ADD && l.base == nil:
			return checkLin{base: r.base, off: l.off + r.off}
		case x.Op == token.SUB && r.base == nil:
			return checkLin{base: l.base, off: l.off - r.off}
		}
	}
	return checkLin{base: v}
}

// checkLoop is what a loop's tests of its counter say: each test, and the
// last value of the counter on which the log can run, when every test can be
// read.
type checkLoop struct {
	tests  []checkTest
	isTest map[*ssa.BasicBlock]bool
	last   checkLin
	known  bool
}

// checkTest is a branch that ends a loop by its counter: the exit edge, and
// the counter's value in the iteration whose log the exit follows.
type checkTest struct {
	exit [2]*ssa.BasicBlock
	at   checkLin
}

// checkCounter finds the tests of phi, a number defined at the head h of a
// loop holding b, by which the loop ends: branches inside the loop with one
// edge out of it, whose condition reads phi. The head's condition, a test at
// the bottom of the body and a break in the middle all count. A φ-node no
// such test reads, such as a count of failures, is not a counter.
//
// Each test is read when phi steps by 1 or -1 and the test compares it with
// one bound. A test before the log in the iteration ends the loop on the next
// iteration, so the log it follows ran on the last value that passes it. A
// test after the log ends the loop in the iteration that logged, on the first
// value that fails. The log runs on no value past the least of these.
func checkCounter(h, b *ssa.BasicBlock, phi *ssa.Phi, body map[*ssa.BasicBlock]bool) (checkLoop, bool) {
	if bt, ok := phi.Type().Underlying().(*types.Basic); !ok || bt.Info()&types.IsNumeric == 0 {
		return checkLoop{}, false
	}
	// Every back edge must step the counter by the same 1 or -1. A body
	// that adds 2 on one path and 1 on another has no last value to read.
	var step int64
	for j, e := range phi.Edges {
		if j >= len(h.Preds) || !body[h.Preds[j]] {
			continue
		}
		l := checkLinear(e)
		if l.base != phi || (l.off != 1 && l.off != -1) || (step != 0 && l.off != step) {
			step = 0
			break
		}
		step = l.off
	}
	lc := checkLoop{isTest: make(map[*ssa.BasicBlock]bool), known: step != 0}
	for _, p := range h.Parent().Blocks {
		if !body[p] {
			continue
		}
		iff, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok || p.Succs[0] == p.Succs[1] {
			continue
		}
		in0, in1 := body[p.Succs[0]], body[p.Succs[1]]
		if in0 == in1 {
			continue
		}
		cmp, ok := iff.Cond.(*ssa.BinOp)
		if !ok || (!checkUses(cmp.X, phi, map[ssa.Value]bool{}) && !checkUses(cmp.Y, phi, map[ssa.Value]bool{})) {
			continue
		}
		exit := p.Succs[1]
		if in1 {
			exit = p.Succs[0]
		}
		t := checkTest{exit: [2]*ssa.BasicBlock{p, exit}}
		if lc.known {
			// A test in the log's own block comes after the log.
			at, ok := checkLastOf(cmp, in0, phi, step, p == b || !p.Dominates(b))
			if ok && (len(lc.tests) == 0 || at.base == lc.last.base) {
				t.at = at
				if len(lc.tests) == 0 || (step > 0 && at.off < lc.last.off) || (step < 0 && at.off > lc.last.off) {
					lc.last = at
				}
			} else {
				lc.known = false
			}
		}
		lc.isTest[p] = true
		lc.tests = append(lc.tests, t)
	}
	if len(lc.tests) == 0 {
		return checkLoop{}, false
	}
	return lc, true
}

// checkLastOf is the counter's value on the last iteration a test cmp lets
// run: stay says whether the loop goes on when cmp holds, after whether the
// test comes after the log in the iteration.
func checkLastOf(cmp *ssa.BinOp, stay bool, phi *ssa.Phi, step int64, after bool) (checkLin, bool) {
	op, lhs, rhs := cmp.Op, checkLinear(cmp.X), checkLinear(cmp.Y)
	if lhs.base != ssa.Value(phi) {
		lhs, rhs = rhs, lhs
		op = checkFlip(op)
	}
	if lhs.base != ssa.Value(phi) || rhs.base == ssa.Value(phi) {
		return checkLin{}, false
	}
	if !stay {
		op = checkNegate(op)
	}
	// Counting toward a bound, going on while it is not reached is going on
	// while it is below it, or above it when counting down.
	if op == token.NEQ {
		op = token.LSS
		if step < 0 {
			op = token.GTR
		}
	}
	// The loop goes on while phi + lhs.off op rhs.
	k := lhs.off
	var last int64
	switch {
	case step > 0 && op == token.LSS:
		last = rhs.off - k - 1
	case step > 0 && op == token.LEQ:
		last = rhs.off - k
	case step < 0 && op == token.GTR:
		last = rhs.off - k + 1
	case step < 0 && op == token.GEQ:
		last = rhs.off - k
	default:
		return checkLin{}, false
	}
	if after {
		last += step
	}
	return checkLin{base: rhs.base, off: last}, true
}

// checkAt evaluates the comparison cond with phi at the value last, when both
// sides are linear over one base. open reports that both sides are linear
// but over different bases, as i > 0 is against a last value of n-1: it holds
// for some bound and fails for another.
func checkAt(cond ssa.Value, phi *ssa.Phi, last checkLin) (v, ok, open bool) {
	cmp, isCmp := cond.(*ssa.BinOp)
	if !isCmp {
		return false, false, false
	}
	sub := func(l checkLin) checkLin {
		if l.base == ssa.Value(phi) {
			return checkLin{base: last.base, off: last.off + l.off}
		}
		return l
	}
	onPhi := checkLinear(cmp.X).base == ssa.Value(phi) || checkLinear(cmp.Y).base == ssa.Value(phi)
	lhs, rhs := sub(checkLinear(cmp.X)), sub(checkLinear(cmp.Y))
	if !onPhi {
		return false, false, false
	}
	if lhs.base != rhs.base && !checkSame(lhs.base, rhs.base, 0) {
		// Against a constant, a guard holds on the last value for some
		// bound: i > 0 does whenever the loop runs twice. Against another
		// bound, as i < *last is, nothing is known.
		open := (lhs.base == nil || rhs.base == nil) && !checkUses(lhs.base, phi, map[ssa.Value]bool{}) && !checkUses(rhs.base, phi, map[ssa.Value]bool{})
		return false, false, open
	}
	d := lhs.off - rhs.off
	switch cmp.Op {
	case token.EQL:
		return d == 0, true, false
	case token.NEQ:
		return d != 0, true, false
	case token.LSS:
		return d < 0, true, false
	case token.LEQ:
		return d <= 0, true, false
	case token.GTR:
		return d > 0, true, false
	case token.GEQ:
		return d >= 0, true, false
	}
	return false, false, false
}

// checkSame reports whether a and b compute the same bound: two reads of
// len(s), or two loads of one field that nothing can write between, which
// SSA names apart. A wrong answer decides a branch the path cannot decide,
// and the walk then leaves out an edge it could take, which misses a report.
func checkSame(a, b ssa.Value, depth int) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || depth > 8 {
		return false
	}
	switch x := a.(type) {
	case *ssa.Call:
		y, ok := b.(*ssa.Call)
		if !ok || x.Call.IsInvoke() || y.Call.IsInvoke() || len(x.Call.Args) != len(y.Call.Args) {
			return false
		}
		bx, ok1 := x.Call.Value.(*ssa.Builtin)
		by, ok2 := y.Call.Value.(*ssa.Builtin)
		if !ok1 || !ok2 || bx.Name() != by.Name() || (bx.Name() != "len" && bx.Name() != "cap") {
			return false
		}
		for i := range x.Call.Args {
			if !checkSame(x.Call.Args[i], y.Call.Args[i], depth+1) {
				return false
			}
		}
		return true
	case *ssa.UnOp:
		y, ok := b.(*ssa.UnOp)
		if !ok || x.Op != token.MUL || y.Op != token.MUL || !checkNothingBetween(x, y) {
			return false
		}
		rx, px := store.Locate(x.X)
		ry, py := store.Locate(y.X)
		return checkSame(rx, ry, depth+1) && slices.Equal(px, py)
	case *ssa.Convert:
		y, ok := b.(*ssa.Convert)
		return ok && checkSame(x.X, y.X, depth+1)
	}
	return false
}

// checkNothingBetween reports whether loads x and y sit in one block with no
// store and no call between them, so that they read one value.
func checkNothingBetween(x, y *ssa.UnOp) bool {
	if x.Block() != y.Block() {
		return false
	}
	instrs := x.Block().Instrs
	i, j := slices.Index(instrs, ssa.Instruction(x)), slices.Index(instrs, ssa.Instruction(y))
	for _, instr := range instrs[min(i, j)+1 : max(i, j)] {
		switch instr.(type) {
		case *ssa.Store, ssa.CallInstruction, *ssa.MapUpdate, *ssa.Send:
			return false
		}
	}
	return true
}

// checkFlip swaps the sides of a comparison.
func checkFlip(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	}
	return op
}

// checkNegate is the comparison that holds when op does not.
func checkNegate(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GEQ
	case token.LEQ:
		return token.GTR
	case token.GTR:
		return token.LEQ
	case token.GEQ:
		return token.LSS
	case token.EQL:
		return token.NEQ
	case token.NEQ:
		return token.EQL
	}
	return op
}

// checkSuccs lists the successors of b that the path may take: both edges of
// a branch whose condition is not decided, and only the agreeing one of a
// branch whose condition is.
func checkSuccs(b *ssa.BasicBlock, phis map[*ssa.Phi]ssa.Value, known checkKnown) []*ssa.BasicBlock {
	iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
	if !ok {
		return b.Succs
	}
	if v, ok := checkEval(iff.Cond, phis, known, 0); ok {
		if v {
			return b.Succs[:1]
		}
		return b.Succs[1:]
	}
	return b.Succs
}

// checkEval decides a boolean on the path: a constant, a condition a branch
// on the path decided, a φ-node the path resolved, or the negation of one.
func checkEval(v ssa.Value, phis map[*ssa.Phi]ssa.Value, known checkKnown, depth int) (bool, bool) {
	if depth > checkMaxDepth {
		return false, false
	}
	if b, ok := known.conds[v]; ok {
		return b, true
	}
	// A boolean compared with a constant on the path decides it: after
	// v == false held, v is false.
	if typeutil.Inert(v.Type()) {
		for cond, taken := range known.conds {
			c, ok := cond.(*ssa.BinOp)
			if !ok {
				continue
			}
			if v2, set, ok := checkFactOf(c); ok && v2 == v && set.eq != nil && set.eq.Kind() == constant.Bool {
				return constant.BoolVal(set.eq) == (taken == set.in), true
			}
		}
	}
	switch x := v.(type) {
	case *ssa.Const:
		if x.Value != nil && x.Value.Kind() == constant.Bool {
			return constant.BoolVal(x.Value), true
		}
	case *ssa.Phi:
		// Following the path's resolutions spends no depth: the chain is
		// as long as the path, and ends at a value that is not one.
		if e := checkResolve(x, phis); e != ssa.Value(x) {
			return checkEval(e, phis, known, depth)
		}
	case *ssa.UnOp:
		if x.Op == token.NOT {
			if b, ok := checkEval(x.X, phis, known, depth+1); ok {
				return !b, true
			}
		}
	case *ssa.BinOp:
		return checkEvalCmp(x, phis, known, depth)
	}
	return false, false
}

// checkEvalCmp decides a comparison of a value with a constant from what the
// path decided about other comparisons of the same value. v == true is v.
// After mode == 1 held, mode == 2 does not. After code < 500 held,
// code >= 500 does not. After err != nil held on a value, the same test of a
// φ-node resolved to that value holds too.
func checkEvalCmp(x *ssa.BinOp, phis map[*ssa.Phi]ssa.Value, known checkKnown, depth int) (bool, bool) {
	v, q, ok := checkFactOf(x)
	if !ok {
		return false, false
	}
	if q.eq != nil && q.eq.Kind() == constant.Bool {
		if b, ok := checkEval(v, phis, known, depth+1); ok {
			return (b == constant.BoolVal(q.eq)) == q.in, true
		}
		return false, false
	}
	// A φ-node the path resolved to a constant is that constant.
	if k, ok := checkResolve(v, phis).(*ssa.Const); ok {
		return q.holds(k)
	}
	if op, ok := checkOperandOf(v, phis); ok && q.eq == nil {
		if set, ok := known.facts[op]; ok {
			if d, ok := set.decides(q); ok {
				return d, true
			}
		}
	}
	for cond, taken := range known.conds {
		c, ok := cond.(*ssa.BinOp)
		if !ok || c == x {
			continue
		}
		v2, set, ok := checkFactOf(c)
		if !ok || !checkSameOperand(v, v2, phis) {
			continue
		}
		if !taken {
			set = set.not()
		}
		if d, ok := set.decides(q); ok {
			return d, true
		}
	}
	return false, false
}

// checkKnown is what a path decided. conds holds each condition a branch on
// the path decided, by its value. facts holds, for each operand compared with
// an integer or nil, the set of values the comparisons leave it: two switch
// statements over one mode decide one fact, not a condition per case.
type checkKnown struct {
	conds map[ssa.Value]bool
	facts map[checkOperand]checkSet
}

// checkOperand names what a comparison compares: a value, or its length or
// capacity.
type checkOperand struct {
	v    ssa.Value
	kind byte
}

// checkKnownFrom is what the conditions conds decide, before the path has
// resolved anything.
func checkKnownFrom(conds map[ssa.Value]bool) checkKnown {
	k := checkKnown{conds: make(map[ssa.Value]bool), facts: make(map[checkOperand]checkSet)}
	for cond, taken := range conds {
		k.decide(cond, taken, nil)
	}
	return k
}

// without is a copy of k that leaves out what drop says of a condition, or
// of an operand.
func (k checkKnown) without(drop func(ssa.Value) bool) checkKnown {
	out := checkKnown{conds: make(map[ssa.Value]bool, len(k.conds)+1), facts: make(map[checkOperand]checkSet, len(k.facts)+1)}
	for cond, v := range k.conds {
		if !drop(cond) {
			out.conds[cond] = v
		}
	}
	for op, set := range k.facts {
		if !drop(op.v) {
			out.facts[op] = set
		}
	}
	return out
}

// decide records that cond is taken, or not. A comparison of an operand with
// an integer or nil narrows the operand's fact. Any other condition is kept
// as it is.
func (k checkKnown) decide(cond ssa.Value, taken bool, phis map[*ssa.Phi]ssa.Value) {
	if c, ok := cond.(*ssa.BinOp); ok {
		if v, set, ok := checkFactOf(c); ok && set.eq == nil {
			if op, ok := checkOperandOf(v, phis); ok {
				if !taken {
					set = set.not()
				}
				if prev, ok := k.facts[op]; ok && prev.isNil == set.isNil {
					set = prev.and(set)
				}
				k.facts[op] = set
				return
			}
		}
	}
	k.conds[cond] = taken
}

// checkOperandOf is the operand v stands for on the path: a value, through
// conversions to an interface and the φ-nodes the path resolved, or the
// length or capacity of one. A load is none: memory may change between two
// reads of it.
func checkOperandOf(v ssa.Value, phis map[*ssa.Phi]ssa.Value) (checkOperand, bool) {
	r := checkResolve(v, phis)
	switch x := r.(type) {
	case *ssa.UnOp:
		if x.Op == token.MUL {
			return checkOperand{}, false
		}
	case *ssa.Call:
		// The length of a map or a channel changes with no new value: an
		// update or a send changes it. Only a slice's, an array's or a
		// string's is fixed.
		if b, ok := x.Call.Value.(*ssa.Builtin); ok && len(x.Call.Args) == 1 && checkFixedLen(x.Call.Args[0].Type()) {
			switch b.Name() {
			case "len":
				return checkOperand{v: checkResolve(x.Call.Args[0], phis), kind: 'l'}, true
			case "cap":
				return checkOperand{v: checkResolve(x.Call.Args[0], phis), kind: 'c'}, true
			}
		}
	}
	return checkOperand{v: r}, true
}

// checkFixedLen reports whether a value of type t has a length no operation
// on it changes: a slice, an array, a pointer to an array, or a string.
func checkFixedLen(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Slice, *types.Array:
		return true
	case *types.Pointer:
		_, ok := u.Elem().Underlying().(*types.Array)
		return ok
	case *types.Basic:
		return u.Info()&types.IsString != 0
	}
	return false
}

// and is the set of values in both s and q, two sets of one kind.
func (s checkSet) and(q checkSet) checkSet {
	var out [][2]int64
	for _, a := range s.iv {
		for _, b := range q.iv {
			lo, hi := max(a[0], b[0]), min(a[1], b[1])
			if lo <= hi {
				out = append(out, [2]int64{lo, hi})
			}
		}
	}
	s.iv = out
	return s
}

// checkSet is what a comparison with a constant says of the value compared.
// For an integer or a nil test, it is a union of intervals within [lo, hi]:
// a nil test reads nil as 0 and anything else as 1. For any other constant,
// it is equality with eq when in holds, or inequality.
type checkSet struct {
	iv    [][2]int64
	lo    int64
	hi    int64
	eq    constant.Value
	in    bool
	isNil bool
}

// checkFactOf splits a comparison of a value with a constant into the value
// and what the comparison says of it.
func checkFactOf(b *ssa.BinOp) (ssa.Value, checkSet, bool) {
	op := b.Op
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
	default:
		return nil, checkSet{}, false
	}
	v, kv := b.X, b.Y
	k, ok := kv.(*ssa.Const)
	if !ok {
		v, kv = b.Y, b.X
		if k, ok = kv.(*ssa.Const); !ok {
			return nil, checkSet{}, false
		}
		op = checkFlip(op)
	}
	if _, isConst := v.(*ssa.Const); isConst {
		return nil, checkSet{}, false
	}
	switch {
	case k.IsNil():
		if op != token.EQL && op != token.NEQ {
			return nil, checkSet{}, false
		}
		s := checkSet{isNil: true, lo: 0, hi: 1, iv: [][2]int64{{1, 1}}}
		if op == token.EQL {
			s.iv = [][2]int64{{0, 0}}
		}
		return v, s, true
	case k.Value == nil:
		return nil, checkSet{}, false
	case k.Value.Kind() == constant.Int:
		n, exact := constant.Int64Val(k.Value)
		if !exact || n == math.MinInt64 || n == math.MaxInt64 {
			return nil, checkSet{}, false
		}
		s := checkSet{lo: math.MinInt64, hi: math.MaxInt64}
		switch op {
		case token.EQL:
			s.iv = [][2]int64{{n, n}}
		case token.NEQ:
			s.iv = [][2]int64{{math.MinInt64, n - 1}, {n + 1, math.MaxInt64}}
		case token.LSS:
			s.iv = [][2]int64{{math.MinInt64, n - 1}}
		case token.LEQ:
			s.iv = [][2]int64{{math.MinInt64, n}}
		case token.GTR:
			s.iv = [][2]int64{{n + 1, math.MaxInt64}}
		case token.GEQ:
			s.iv = [][2]int64{{n, math.MaxInt64}}
		}
		return v, s, true
	}
	if op != token.EQL && op != token.NEQ {
		return nil, checkSet{}, false
	}
	return v, checkSet{eq: k.Value, in: op == token.EQL}, true
}

// not is the set of values s leaves out.
func (s checkSet) not() checkSet {
	if s.eq != nil {
		s.in = !s.in
		return s
	}
	var out [][2]int64
	next, done := s.lo, false
	for _, r := range s.iv {
		if r[0] > next {
			out = append(out, [2]int64{next, r[0] - 1})
		}
		if r[1] == s.hi {
			done = true
			break
		}
		next = r[1] + 1
	}
	if !done {
		out = append(out, [2]int64{next, s.hi})
	}
	s.iv = out
	return s
}

// decides reports what q says of a value known to lie in s: true when every
// value of s satisfies q, false when none does.
func (s checkSet) decides(q checkSet) (bool, bool) {
	if s.eq != nil || q.eq != nil {
		if s.eq == nil || q.eq == nil || !checkComparable(s.eq, q.eq) {
			return false, false
		}
		same := constant.Compare(s.eq, token.EQL, q.eq)
		switch {
		case s.in:
			return same == q.in, true
		case same:
			return !q.in, true
		}
		return false, false
	}
	if s.isNil != q.isNil || len(s.iv) == 0 {
		return false, false
	}
	all, none := true, true
	for _, a := range s.iv {
		covered := false
		for _, b := range q.iv {
			if a[0] <= b[1] && b[0] <= a[1] {
				none = false
			}
			if b[0] <= a[0] && a[1] <= b[1] {
				covered = true
			}
		}
		if !covered {
			all = false
		}
	}
	switch {
	case all:
		return true, true
	case none:
		return false, true
	}
	return false, false
}

// holds evaluates the comparison s stands for on the constant k.
func (s checkSet) holds(k *ssa.Const) (bool, bool) {
	switch {
	case s.isNil:
		if !k.IsNil() {
			return false, false
		}
		return s.iv[0][0] == 0, true
	case k.Value == nil:
		return false, false
	case s.eq != nil:
		if !checkComparable(s.eq, k.Value) {
			return false, false
		}
		return constant.Compare(s.eq, token.EQL, k.Value) == s.in, true
	case k.Value.Kind() != constant.Int:
		return false, false
	}
	n, exact := constant.Int64Val(k.Value)
	if !exact {
		return false, false
	}
	for _, r := range s.iv {
		if r[0] <= n && n <= r[1] {
			return true, true
		}
	}
	return false, true
}

// checkResolve strips conversions from one interface to another from v, and
// follows a φ-node the path resolved to its edge, until a value that is
// neither. A resolution seen before ends it. A conversion of a concrete value
// to an interface is kept: a nil *T in an error is not a nil error.
func checkResolve(v ssa.Value, phis map[*ssa.Phi]ssa.Value) ssa.Value {
	seen := make(map[ssa.Value]bool)
	for !seen[v] {
		seen[v] = true
		switch x := v.(type) {
		case *ssa.ChangeInterface:
			v = x.X
		case *ssa.Phi:
			e, ok := phis[x]
			if !ok {
				return v
			}
			v = e
		default:
			return v
		}
	}
	return v
}

// checkSameOperand reports whether a and b are one value on the path.
func checkSameOperand(a, b ssa.Value, phis map[*ssa.Phi]ssa.Value) bool {
	a, b = checkResolve(a, phis), checkResolve(b, phis)
	return a == b || checkSame(a, b, 0)
}

// checkCanon is the form of v that two reads of one bound share: the operand
// of len or cap, the memory a load reads, or what a conversion converts.
func checkCanon(v ssa.Value) ssa.Value {
	for range checkMaxDepth {
		switch x := v.(type) {
		case *ssa.Call:
			bi, ok := x.Call.Value.(*ssa.Builtin)
			if !ok || (bi.Name() != "len" && bi.Name() != "cap") || len(x.Call.Args) != 1 {
				return v
			}
			v = x.Call.Args[0]
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return v
			}
			v, _ = store.Locate(x.X)
		case *ssa.Convert:
			v = x.X
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeInterface:
			v = x.X
		default:
			return v
		}
	}
	return v
}

// checkComparable reports whether constant.Compare accepts a and b: the same
// kind, or both numeric.
func checkComparable(a, b constant.Value) bool {
	num := func(k constant.Kind) bool {
		return k == constant.Int || k == constant.Float || k == constant.Complex
	}
	return a.Kind() == b.Kind() || (num(a.Kind()) && num(b.Kind()))
}

// checkKnownNil reports whether the decided conditions say that v is nil: a
// branch the log sits under compared it with nil.
func checkKnownNil(o walkerOrigin, known map[ssa.Value]bool) bool {
	if o.path != "" {
		return false
	}
	v := o.v
	for cond, taken := range known {
		cmp, ok := cond.(*ssa.BinOp)
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
		for _, s := range checkSuccs(b, nil, checkKnown{}) {
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
	f, generated := c.checkFile(c.pass.Fset.Position(pos).Filename)
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
	f, _ := c.checkFile(c.pass.Fset.Position(pos).Filename)
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
