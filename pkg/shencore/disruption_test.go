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

// Table-driven tests for the disruption decision methods (shen/core/disruption.shen)
// exercised through the shencore engine in interpreter mode: candidate gating,
// method classification + priority, budget arithmetic (with errors surfaced as
// values), Balanced scoring, and the top-level planner. The .shen file also
// type-checks under (tc +) via `make shen-check`.
package shencore_test

import (
	"sync"
	"testing"

	"sigs.k8s.io/karpenter/pkg/shencore"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

var (
	disrEngineOnce sync.Once
	disrEngine     *shencore.Engine
	disrEngineErr  error
)

func disrEng(t *testing.T) *shencore.Engine {
	t.Helper()
	disrEngineOnce.Do(func() {
		disrEngine, disrEngineErr = shencore.New(shencore.Options{
			DefaultStepBudget: 5_000_000,
			Interpret:         []string{"../../shen/core/disruption.shen"},
		})
	})
	if disrEngineErr != nil {
		t.Fatalf("load disruption.shen: %v", disrEngineErr)
	}
	return disrEngine
}

func b(v bool) sexpr.Value {
	if v {
		return sexpr.Symbol("true")
	}
	return sexpr.Symbol("false")
}

// cand builds a candidate value; field order matches the datatype constructor
// [candidate Id Np Static Init Marked Nom Dnd PB Empty Cons Drift Price RCost].
func cand(id, np string, static, init, marked, nom, dnd, pb, empty, cons, drift bool, price, rcost int64) sexpr.Value {
	return sexpr.List{
		sexpr.Symbol("candidate"), sexpr.String(id), sexpr.String(np),
		b(static), b(init), b(marked), b(nom), b(dnd), b(pb),
		b(empty), b(cons), b(drift), sexpr.Int(price), sexpr.Int(rcost),
	}
}

// eligibleCand is a clean, initialized, non-static, empty+consolidatable candidate
// (the emptiness-eligible baseline); flip individual fields per test.
func eligibleCand(id, np string) sexpr.Value {
	return cand(id, np, false, true, false, false, false, false, true, true, false, 10, 1)
}

func TestGate(t *testing.T) {
	e := disrEng(t)
	cases := []struct {
		name string
		c    sexpr.Value
		want string // "eligible" or the block reason
	}{
		{"eligible", eligibleCand("n1", "np-a"), "eligible"},
		{"uninitialized", cand("n", "np-a", false, false, false, false, false, false, true, true, false, 10, 1), "not-initialized"},
		{"marked", cand("n", "np-a", false, true, true, false, false, false, true, true, false, 10, 1), "marked-for-deletion"},
		{"nominated", cand("n", "np-a", false, true, false, true, false, false, true, true, false, 10, 1), "nominated"},
		{"do-not-disrupt", cand("n", "np-a", false, true, false, false, true, false, true, true, false, 10, 1), "do-not-disrupt"},
		{"no-nodepool", cand("n", "", false, true, false, false, false, false, true, true, false, 10, 1), "no-nodepool"},
		{"pods-blocked", cand("n", "np-a", false, true, false, false, false, true, true, true, false, 10, 1), "pods-blocked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := e.Call("gate", c.c)
			if err != nil {
				t.Fatalf("gate: %v", err)
			}
			if c.want == "eligible" {
				if got != sexpr.Symbol("eligible") {
					t.Fatalf("gate = %#v, want eligible", got)
				}
				return
			}
			// blocked: [blocked <reason>]
			l, ok := got.(sexpr.List)
			if !ok || len(l) != 2 || l[0] != sexpr.Symbol("blocked") {
				t.Fatalf("gate = %#v, want [blocked %s]", got, c.want)
			}
			if l[1] != sexpr.Symbol(c.want) {
				t.Fatalf("gate blocked reason = %#v, want %s", l[1], c.want)
			}
		})
	}
}

func TestGateChecksInGoOrder(t *testing.T) {
	// A candidate failing multiple checks reports the first in ValidateNodeDisruptable
	// order: initialized is checked before nomination.
	e := disrEng(t)
	c := cand("n", "np-a", false, false, false, true, false, false, true, true, false, 10, 1)
	got, _ := e.Call("gate", c)
	l := got.(sexpr.List)
	if l[1] != sexpr.Symbol("not-initialized") {
		t.Fatalf("expected not-initialized to win over nominated, got %#v", l[1])
	}
}

func TestClassifyPriority(t *testing.T) {
	e := disrEng(t)
	cases := []struct {
		name string
		c    sexpr.Value
		want string
	}{
		// empty + consolidatable + non-static -> emptiness
		{"emptiness", cand("n", "np", false, true, false, false, false, false, true, true, false, 10, 1), "m-emptiness"},
		// static + drifted -> staticdrift
		{"staticdrift", cand("n", "np", true, true, false, false, false, false, false, false, true, 10, 1), "m-staticdrift"},
		// non-static + drifted (not empty) -> drift
		{"drift", cand("n", "np", false, true, false, false, false, false, false, false, true, 10, 1), "m-drift"},
		// emptiness wins over drift when both apply on a non-static node
		{"emptiness-over-drift", cand("n", "np", false, true, false, false, false, false, true, true, true, 10, 1), "m-emptiness"},
		// static empty node is NOT emptiness (Emptiness excludes static pools); no drift -> none
		{"static-empty-none", cand("n", "np", true, true, false, false, false, false, true, true, false, 10, 1), "m-none"},
		{"none", cand("n", "np", false, true, false, false, false, false, false, false, false, 10, 1), "m-none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := e.Call("classify", c.c)
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if got != sexpr.Symbol(c.want) {
				t.Fatalf("classify = %#v, want %s", got, c.want)
			}
		})
	}
}

func percent(p int64) sexpr.Value { return sexpr.List{sexpr.Symbol("percent"), sexpr.Int(p)} }
func count(n int64) sexpr.Value   { return sexpr.List{sexpr.Symbol("count"), sexpr.Int(n)} }
func malformed(s string) sexpr.Value {
	return sexpr.List{sexpr.Symbol("malformed"), sexpr.String(s)}
}

func TestBudgetAllowed(t *testing.T) {
	e := disrEng(t)
	cases := []struct {
		name     string
		nodes    sexpr.Value
		numNodes int64
		wantTag  string // "allowed" or "budget-error"
		wantN    int64  // when allowed
	}{
		{"percent-rounds-up", percent(5), 10, "allowed", 1},    // ceil(0.5) = 1
		{"percent-exact", percent(20), 10, "allowed", 2},       // 2.0 -> 2
		{"percent-rounds-up-2", percent(21), 10, "allowed", 3}, // ceil(2.1) = 3
		{"percent-zero", percent(0), 10, "allowed", 0},
		{"count", count(3), 10, "allowed", 3},
		{"count-negative", count(-1), 10, "budget-error", 0},
		{"percent-over-100", percent(150), 10, "budget-error", 0},
		{"malformed", malformed("1O%"), 10, "budget-error", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := e.Call("budget-allowed", c.nodes, sexpr.Int(c.numNodes))
			if err != nil {
				t.Fatalf("budget-allowed: %v", err)
			}
			l, ok := got.(sexpr.List)
			if !ok || len(l) != 2 {
				t.Fatalf("budget-allowed = %#v", got)
			}
			if l[0] != sexpr.Symbol(c.wantTag) {
				t.Fatalf("budget tag = %#v, want %s", l[0], c.wantTag)
			}
			if c.wantTag == "allowed" && l[1] != sexpr.Int(c.wantN) {
				t.Fatalf("allowed = %#v, want %d", l[1], c.wantN)
			}
		})
	}
}

func TestBalancedScoring(t *testing.T) {
	e := disrEng(t)
	approved := func(sav, disr, tcost, tdisr, k int64) bool {
		t.Helper()
		// move-approved? composes score-move + approved? in Shen and returns the
		// boolean verdict; the intermediate ScoreResult carries floats the
		// int-only sexpr contract cannot round-trip, so we never decode it.
		res, err := e.Call("move-approved?", sexpr.Int(sav), sexpr.Int(disr), sexpr.Int(tcost), sexpr.Int(tdisr), sexpr.Int(k))
		if err != nil {
			t.Fatalf("move-approved?: %v", err)
		}
		return res == sexpr.Symbol("true")
	}
	// (6/100)/(1/10) = 0.6 >= 0.5 -> approved
	if !approved(6, 1, 100, 10, 2) {
		t.Error("expected 0.6 >= 0.5 approved")
	}
	// (4/100)/(1/10) = 0.4 < 0.5 -> not approved
	if approved(4, 1, 100, 10, 2) {
		t.Error("expected 0.4 < 0.5 not approved")
	}
	// exactly at threshold: (5/100)/(1/10) = 0.5 >= 0.5 -> approved (inclusive)
	if !approved(5, 1, 100, 10, 2) {
		t.Error("expected 0.5 >= 0.5 approved (inclusive threshold)")
	}
	// non-positive savings never approves
	if approved(0, 1, 100, 10, 2) {
		t.Error("expected non-positive savings not approved")
	}
	// zero disruption always approves (Go returns +Inf)
	if !approved(1, 0, 100, 10, 2) {
		t.Error("expected zero disruption approved")
	}
}

// --- planner ---

func decodeCommands(t *testing.T, v sexpr.Value) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range v.(sexpr.List) {
		l := e.(sexpr.List) // [disrupt-command id method]
		out[string(l[1].(sexpr.String))] = string(l[2].(sexpr.Symbol))
	}
	return out
}

func TestPlanBudgetLimitedSelection(t *testing.T) {
	e := disrEng(t)
	// Three emptiness-eligible candidates in np-a; budget 50% of 4 = 2 allowed.
	cands := sexpr.List{
		eligibleCand("c1", "np-a"),
		eligibleCand("c2", "np-a"),
		eligibleCand("c3", "np-a"),
	}
	budgets := sexpr.List{
		sexpr.List{sexpr.Symbol("pool-budget"), sexpr.String("np-a"), percent(50), sexpr.Int(4), sexpr.Int(0)},
	}
	cmds, err := e.Call("plan-commands", cands, budgets)
	if err != nil {
		t.Fatalf("plan-commands: %v", err)
	}
	got := decodeCommands(t, cmds)
	if len(got) != 2 {
		t.Fatalf("selected %d commands, want 2 (budget cap): %v", len(got), got)
	}
	// First two in order (c1, c2), all emptiness.
	for _, id := range []string{"c1", "c2"} {
		if got[id] != "m-emptiness" {
			t.Errorf("command %s = %q, want m-emptiness", id, got[id])
		}
	}
	if _, ok := got["c3"]; ok {
		t.Error("c3 should be dropped by budget")
	}
}

func TestPlanMethodPriority(t *testing.T) {
	e := disrEng(t)
	// One emptiness-eligible + one staticdrift-eligible: emptiness wins the tick,
	// staticdrift candidate is not selected.
	empty := eligibleCand("e1", "np-a")
	sdrift := cand("s1", "np-b", true, true, false, false, false, false, false, false, true, 10, 1)
	cands := sexpr.List{sdrift, empty} // order shouldn't matter
	budgets := sexpr.List{
		sexpr.List{sexpr.Symbol("pool-budget"), sexpr.String("np-a"), count(10), sexpr.Int(4), sexpr.Int(0)},
		sexpr.List{sexpr.Symbol("pool-budget"), sexpr.String("np-b"), count(10), sexpr.Int(4), sexpr.Int(0)},
	}
	cmds, _ := e.Call("plan-commands", cands, budgets)
	got := decodeCommands(t, cmds)
	if len(got) != 1 || got["e1"] != "m-emptiness" {
		t.Fatalf("expected only e1/m-emptiness, got %v", got)
	}
}

// TestPlanSurfacesBudgetError is the reviewed-bug fix: a malformed budget fails
// closed (no commands from that pool) AND surfaces the error, unlike Go's
// MustGetAllowedDisruptions which silently returns 0.
func TestPlanSurfacesBudgetError(t *testing.T) {
	e := disrEng(t)
	cands := sexpr.List{eligibleCand("c1", "np-a"), eligibleCand("c2", "np-a")}
	budgets := sexpr.List{
		sexpr.List{sexpr.Symbol("pool-budget"), sexpr.String("np-a"), malformed("bogus"), sexpr.Int(4), sexpr.Int(0)},
	}
	cmds, _ := e.Call("plan-commands", cands, budgets)
	if got := decodeCommands(t, cmds); len(got) != 0 {
		t.Fatalf("expected no commands (fail closed), got %v", got)
	}
	errs, err := e.Call("plan-errors", cands, budgets)
	if err != nil {
		t.Fatalf("plan-errors: %v", err)
	}
	reports := errs.(sexpr.List)
	if len(reports) != 1 {
		t.Fatalf("expected 1 budget report, got %v", reports)
	}
	r := reports[0].(sexpr.List) // [budget-report np msg]
	if string(r[1].(sexpr.String)) != "np-a" {
		t.Errorf("budget report pool = %v, want np-a", r[1])
	}
}

// TestPlanRespectsDisrupting: allowed minus already-disrupting nodes.
func TestPlanRespectsDisrupting(t *testing.T) {
	e := disrEng(t)
	cands := sexpr.List{eligibleCand("c1", "np-a"), eligibleCand("c2", "np-a")}
	// count 3 allowed, but 2 already disrupting -> only 1 remaining.
	budgets := sexpr.List{
		sexpr.List{sexpr.Symbol("pool-budget"), sexpr.String("np-a"), count(3), sexpr.Int(10), sexpr.Int(2)},
	}
	cmds, _ := e.Call("plan-commands", cands, budgets)
	if got := decodeCommands(t, cmds); len(got) != 1 {
		t.Fatalf("expected 1 command (3 allowed - 2 disrupting), got %v", got)
	}
}
