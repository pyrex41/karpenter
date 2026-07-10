/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Table-driven tests for the disruption command ledger state machine
// (command.shen), exercised through the shencore engine in interpreter mode.
// The .shen file loads under (tc +), so a type error there fails these tests at
// load time; these tests add the behavioral and totality (K1-wedge) coverage
// that (tc +) does not.
package command_test

import (
	"sync"
	"testing"

	"sigs.k8s.io/karpenter/pkg/shencore"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

var (
	engineOnce sync.Once
	engine     *shencore.Engine
	engineErr  error
)

// allStates and allEvents are the closed enumerations from command.shen. The
// tests below rely on these being the complete sets.
var (
	allStates = []string{"pending", "tainted", "launching", "awaiting-ready", "deleting", "done", "rolled-back"}
	allEvents = []string{"tick", "taint-ok", "taint-failed", "launched", "launch-failed", "registered", "deleted", "delete-failed"}
)

func nonTerminal(state string) bool { return state != "done" && state != "rolled-back" }

func eng(t *testing.T) *shencore.Engine {
	t.Helper()
	engineOnce.Do(func() {
		engine, engineErr = shencore.New(shencore.Options{
			DefaultStepBudget: 2_000_000,
			Interpret:         []string{"command.shen"},
		})
	})
	if engineErr != nil {
		t.Fatalf("load command.shen: %v", engineErr)
	}
	return engine
}

// step calls command-step's projections and returns (state', actions).
func step(t *testing.T, state, event string, clock, created, retry int64) (string, []string) {
	t.Helper()
	e := eng(t)
	args := []sexpr.Value{sexpr.Symbol(state), sexpr.Symbol(event), sexpr.Int(clock), sexpr.Int(created), sexpr.Int(retry)}
	st, err := e.Call("step-state", args...)
	if err != nil {
		t.Fatalf("step-state(%s,%s): %v", state, event, err)
	}
	acts, err := e.Call("step-actions", args...)
	if err != nil {
		t.Fatalf("step-actions(%s,%s): %v", state, event, err)
	}
	return asSymbol(t, st), asSymbolList(t, acts)
}

func asSymbol(t *testing.T, v sexpr.Value) string {
	t.Helper()
	s, ok := v.(sexpr.Symbol)
	if !ok {
		t.Fatalf("expected symbol, got %#v", v)
	}
	return string(s)
}

func asSymbolList(t *testing.T, v sexpr.Value) []string {
	t.Helper()
	l, ok := v.(sexpr.List)
	if !ok {
		t.Fatalf("expected list, got %#v", v)
	}
	out := make([]string, len(l))
	for i, e := range l {
		out[i] = asSymbol(t, e)
	}
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTransitionGridIsTotal is the K1-wedge property: every state accepts every
// event without a partial-function fault, and always lands in a declared state.
// A missing case in command.shen would surface here as a ShenError.
func TestTransitionGridIsTotal(t *testing.T) {
	valid := map[string]bool{}
	for _, s := range allStates {
		valid[s] = true
	}
	for _, s := range allStates {
		for _, ev := range allEvents {
			got, _ := step(t, s, ev, 1, 0, 100)
			if !valid[got] {
				t.Errorf("%s x %s -> undeclared state %q", s, ev, got)
			}
		}
	}
}

// TestNoNonTerminalDeadEnds asserts that from every non-terminal state there is
// at least one event that makes progress (leaves the state), so no non-terminal
// state is an absorbing trap. Combined with the totality test, this is the
// "no wedge" guarantee.
func TestNoNonTerminalDeadEnds(t *testing.T) {
	for _, s := range allStates {
		if !nonTerminal(s) {
			continue
		}
		progressed := false
		for _, ev := range allEvents {
			// Use a timed-out clock so `tick` can drive rollback where defined.
			got, _ := step(t, s, ev, 1000, 0, 100)
			if got != s {
				progressed = true
				break
			}
		}
		if !progressed {
			t.Errorf("non-terminal state %q has no progressing event (dead end)", s)
		}
	}
}

func TestHappyPath(t *testing.T) {
	cases := []struct {
		state, event string
		wantState    string
		wantActions  []string
	}{
		{"pending", "tick", "tainted", []string{"apply-taint"}},
		{"tainted", "taint-ok", "launching", []string{"launch-replacements"}},
		{"launching", "launched", "awaiting-ready", nil},
		{"awaiting-ready", "registered", "deleting", []string{"delete-candidates"}},
		{"deleting", "deleted", "done", nil},
	}
	for _, c := range cases {
		gotState, gotActs := step(t, c.state, c.event, 1, 0, 100)
		if gotState != c.wantState {
			t.Errorf("%s x %s: state = %q, want %q", c.state, c.event, gotState, c.wantState)
		}
		if !eqStrs(gotActs, c.wantActions) {
			t.Errorf("%s x %s: actions = %v, want %v", c.state, c.event, gotActs, c.wantActions)
		}
	}
}

func TestFailureRollbacks(t *testing.T) {
	cleanup := []string{"remove-taint", "clear-condition"}
	cases := []struct{ state, event string }{
		{"tainted", "taint-failed"},
		{"launching", "launch-failed"},
		{"awaiting-ready", "launch-failed"},
	}
	for _, c := range cases {
		gotState, gotActs := step(t, c.state, c.event, 1, 0, 100)
		if gotState != "rolled-back" {
			t.Errorf("%s x %s: state = %q, want rolled-back", c.state, c.event, gotState)
		}
		if !eqStrs(gotActs, cleanup) {
			t.Errorf("%s x %s: actions = %v, want %v", c.state, c.event, gotActs, cleanup)
		}
	}
}

// TestTimeout covers the creation-timestamp vs clock transition: a recoverable
// wait becomes an unrecoverable rollback once clock-created > retry, and not
// before.
func TestTimeout(t *testing.T) {
	for _, s := range []string{"tainted", "launching", "awaiting-ready"} {
		// Within budget: tick is a self-loop with no actions.
		gotState, gotActs := step(t, s, "tick", 50, 0, 100)
		if gotState != s || len(gotActs) != 0 {
			t.Errorf("%s x tick within budget: (%q,%v), want (%q,[])", s, gotState, gotActs, s)
		}
		// Past budget: rollback with cleanup.
		gotState, gotActs = step(t, s, "tick", 500, 0, 100)
		if gotState != "rolled-back" || !eqStrs(gotActs, []string{"remove-taint", "clear-condition"}) {
			t.Errorf("%s x tick past budget: (%q,%v), want rolled-back+cleanup", s, gotState, gotActs)
		}
	}
}

// TestDeletingNeverRollsBack asserts that once replacements are live, a stuck
// deletion keeps retrying (idempotent) and never un-cordons — a timeout in
// deleting must not roll back.
func TestDeletingNeverRollsBack(t *testing.T) {
	for _, ev := range []string{"tick", "delete-failed"} {
		gotState, gotActs := step(t, "deleting", ev, 100000, 0, 100)
		if gotState != "deleting" || !eqStrs(gotActs, []string{"delete-candidates"}) {
			t.Errorf("deleting x %s (timed out): (%q,%v), want deleting+[delete-candidates]", ev, gotState, gotActs)
		}
	}
}

func TestTerminalStatesAbsorb(t *testing.T) {
	for _, s := range []string{"done", "rolled-back"} {
		for _, ev := range allEvents {
			gotState, gotActs := step(t, s, ev, 500, 0, 100)
			if gotState != s || len(gotActs) != 0 {
				t.Errorf("terminal %s x %s: (%q,%v), want (%q,[])", s, ev, gotState, gotActs, s)
			}
		}
	}
}

// --- ledger tick ----------------------------------------------------------

func entry(id, state string, created int64) sexpr.Value {
	return sexpr.List{sexpr.String(id), sexpr.Symbol(state), sexpr.Int(created)}
}

func idEvent(id, event string) sexpr.Value {
	return sexpr.List{sexpr.String(id), sexpr.Symbol(event)}
}

// ledgerTick returns (ledger', actions) as decoded Go structures.
func ledgerTick(t *testing.T, ledger, events sexpr.List, clock, retry int64) (sexpr.List, sexpr.List) {
	t.Helper()
	e := eng(t)
	args := []sexpr.Value{ledger, events, sexpr.Int(clock), sexpr.Int(retry)}
	l, err := e.Call("ledger-next", args...)
	if err != nil {
		t.Fatalf("ledger-next: %v", err)
	}
	a, err := e.Call("ledger-actions", args...)
	if err != nil {
		t.Fatalf("ledger-actions: %v", err)
	}
	return l.(sexpr.List), a.(sexpr.List)
}

// entryState pulls the state symbol out of a decoded ledger entry.
func entryState(t *testing.T, v sexpr.Value) (id, state string) {
	t.Helper()
	l := v.(sexpr.List)
	return string(l[0].(sexpr.String)), string(l[1].(sexpr.Symbol))
}

func TestLedgerTickMultiEntry(t *testing.T) {
	// c1 is a fresh pending command (quiet -> gets a tick -> kicks taint).
	// c2 has just been reported ready -> advances to deleting.
	ledger := sexpr.List{entry("c1", "pending", 0), entry("c2", "awaiting-ready", 0)}
	events := sexpr.List{idEvent("c2", "registered")}

	nextLedger, actions := ledgerTick(t, ledger, events, 10, 100)

	if len(nextLedger) != 2 {
		t.Fatalf("ledger' has %d entries, want 2", len(nextLedger))
	}
	wantStates := map[string]string{"c1": "tainted", "c2": "deleting"}
	for _, e := range nextLedger {
		id, st := entryState(t, e)
		if wantStates[id] != st {
			t.Errorf("ledger' %s = %q, want %q", id, st, wantStates[id])
		}
	}

	// Actions are tagged with their decision-id: (c1 apply-taint), (c2 delete-candidates).
	wantActions := map[string]string{"c1": "apply-taint", "c2": "delete-candidates"}
	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2 (%v)", len(actions), actions)
	}
	for _, a := range actions {
		id, act := entryState(t, a)
		if wantActions[id] != act {
			t.Errorf("action for %s = %q, want %q", id, act, wantActions[id])
		}
	}
}

// TestLedgerQuietTickDrivesTimeout proves a command with no events this tick
// still has its timeout evaluated (the implicit tick), so a stalled command
// rolls back on its own.
func TestLedgerQuietTickDrivesTimeout(t *testing.T) {
	ledger := sexpr.List{entry("stalled", "awaiting-ready", 0)}
	nextLedger, actions := ledgerTick(t, ledger, sexpr.List{}, 500, 100)

	_, st := entryState(t, nextLedger[0])
	if st != "rolled-back" {
		t.Errorf("stalled command state = %q, want rolled-back", st)
	}
	// Cleanup actions emitted, tagged with the id.
	if len(actions) != 2 {
		t.Fatalf("actions = %v, want two cleanup actions", actions)
	}
	for _, a := range actions {
		id, act := entryState(t, a)
		if id != "stalled" || (act != "remove-taint" && act != "clear-condition") {
			t.Errorf("unexpected action %s/%s", id, act)
		}
	}
}

func TestLedgerGCDropsTerminal(t *testing.T) {
	e := eng(t)
	ledger := sexpr.List{
		entry("a", "done", 0),
		entry("b", "tainted", 0),
		entry("c", "rolled-back", 0),
	}
	got, err := e.Call("ledger-gc", ledger)
	if err != nil {
		t.Fatalf("ledger-gc: %v", err)
	}
	kept := got.(sexpr.List)
	if len(kept) != 1 {
		t.Fatalf("gc kept %d entries, want 1 (%v)", len(kept), kept)
	}
	if id, st := entryState(t, kept[0]); id != "b" || st != "tainted" {
		t.Errorf("gc kept %s/%s, want b/tainted", id, st)
	}
}
