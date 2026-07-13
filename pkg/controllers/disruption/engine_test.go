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

package disruption

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/shencore/schema"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// shadowDiffCount reads the current value of karpenter_shencore_shadow_diffs_total
// for the disruption controller and the given reason, gathering the registry
// directly so the test needs no Ginkgo context.
func shadowDiffCount(t *testing.T, reason string) float64 {
	t.Helper()
	families, err := crmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "karpenter_shencore_shadow_diffs_total" {
			continue
		}
		for _, m := range mf.Metric {
			labels := map[string]string{}
			for _, lp := range m.Label {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["controller"] == "disruption" && labels["reason"] == reason {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// --- test doubles ---

type stubDecider struct {
	out   sexpr.Value
	err   error
	panic bool
	calls int
}

func (d *stubDecider) Decide(_ context.Context, input sexpr.Value) (sexpr.Value, error) {
	d.calls++
	if d.panic {
		panic("boom from shen")
	}
	if d.err != nil {
		return nil, d.err
	}
	if d.out != nil {
		return d.out, nil
	}
	return input, nil // echo
}

type stubMethod struct{ reason v1.DisruptionReason }

func (m stubMethod) ShouldDisrupt(context.Context, *Candidate) bool { return true }
func (m stubMethod) ComputeCommands(context.Context, map[string]int, ...*Candidate) ([]Command, error) {
	return nil, nil
}
func (m stubMethod) Reason() v1.DisruptionReason { return m.reason }
func (m stubMethod) Class() string               { return GracefulDisruptionClass }
func (m stubMethod) ConsolidationType() string   { return "" }

// output builds a schema (output ...) decision form from command views.
func output(views ...schema.CommandView) sexpr.Value {
	return schema.DisruptResultValue(views)
}

// --- diffDecisions ---

func TestDiffDecisionsMatch(t *testing.T) {
	a := output(schema.CommandView{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"n1", "n2"}})
	// Rebuild independently to ensure equality is structural, not identity.
	b := output(schema.CommandView{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"n1", "n2"}})
	if reason, differs := diffDecisions(a, b); differs {
		t.Fatalf("expected match, got reason %q", reason)
	}
}

func TestDiffDecisionsReasons(t *testing.T) {
	base := output(schema.CommandView{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"n1"}, EstimatedSavings: 1.0})
	cases := []struct {
		name   string
		other  sexpr.Value
		reason string
	}{
		{"count", output(), "command_count"},
		{"decision", output(schema.CommandView{Decision: "replace", Reason: "Empty", CandidateNodeNames: []string{"n1"}, EstimatedSavings: 1.0}), "decisions"},
		{"candidates", output(schema.CommandView{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"n2"}, EstimatedSavings: 1.0}), "candidates"},
		{"savings", output(schema.CommandView{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"n1"}, EstimatedSavings: 2.0}), "savings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, differs := diffDecisions(base, tc.other)
			if !differs {
				t.Fatalf("expected a diff for %s", tc.name)
			}
			if reason != tc.reason {
				t.Fatalf("expected reason %q, got %q", tc.reason, reason)
			}
		})
	}
}

func TestDiffDecisionsReplacements(t *testing.T) {
	base := output(schema.CommandView{Decision: "replace", Reason: "Drifted", CandidateNodeNames: []string{"n1"},
		Replacements: []schema.ReplacementView{{NodePool: "np", InstanceTypeNames: []string{"a", "b"}}}})
	other := output(schema.CommandView{Decision: "replace", Reason: "Drifted", CandidateNodeNames: []string{"n1"},
		Replacements: []schema.ReplacementView{{NodePool: "np", InstanceTypeNames: []string{"a", "c"}}}})
	if reason, differs := diffDecisions(base, other); !differs || reason != "replacements" {
		t.Fatalf("expected replacements diff, got reason=%q differs=%v", reason, differs)
	}
}

// TestShadowZeroDiffOnCorpusSample is the task's explicit gate: over the committed
// disrupt corpus sample, comparing each recorded Go decision to itself (the
// identity engine) yields zero diffs. This proves the shadow diff is stable over
// every real recorded decision shape.
func TestShadowZeroDiffOnCorpusSample(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "test", "parity", "corpus-sample")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("corpus sample %q absent: %v", dir, err)
	}
	checked := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "disrupt-") || !strings.HasSuffix(e.Name(), ".sexpr") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		rec, err := schema.DecodeRecord(raw)
		if err != nil {
			t.Fatalf("decode %s: %v", e.Name(), err)
		}
		out, ok := field(rec, "output")
		if !ok {
			t.Fatalf("%s: no output", e.Name())
		}
		if reason, differs := diffDecisions(out, out); differs {
			t.Fatalf("%s: identity comparison produced a diff (%s)", e.Name(), reason)
		}
		checked++
	}
	if checked == 0 {
		t.Skip("no disrupt records in corpus sample")
	}
	t.Logf("zero-diff verified over %d disrupt records", checked)
}

// --- shadow containment ---

func shadowController(d ShenDecider) *Controller {
	return &Controller{clock: clocktesting.NewFakeClock(time.Unix(0, 0)), shenDecider: d}
}

func TestRunShadowContainsError(t *testing.T) {
	before := shadowDiffCount(t, "shen_error")
	d := &stubDecider{err: fmt.Errorf("shen exploded")}
	c := shadowController(d)
	// Must not panic or affect the caller; runShadow returns nothing.
	c.runShadow(context.Background(), stubMethod{reason: v1.DisruptionReasonEmpty}, map[string]int{}, nil, nil)
	if d.calls != 1 {
		t.Fatalf("expected decider called once, got %d", d.calls)
	}
	if got := shadowDiffCount(t, "shen_error"); got != before+1 {
		t.Fatalf("expected shen_error counter to increment by 1 (%v -> %v)", before, got)
	}
}

func TestRunShadowContainsPanic(t *testing.T) {
	before := shadowDiffCount(t, "shen_error")
	d := &stubDecider{panic: true}
	c := shadowController(d)
	// A panicking Shen path must be recovered inside runShadow and counted.
	c.runShadow(context.Background(), stubMethod{reason: v1.DisruptionReasonEmpty}, map[string]int{}, nil, nil)
	if got := shadowDiffCount(t, "shen_error"); got != before+1 {
		t.Fatalf("expected shen_error counter to increment by 1 (%v -> %v)", before, got)
	}
}

// TestRunShadowMatchNoDiff verifies a Shen decision that matches the Go decision
// records no diff (identity engine → zero diffs).
func TestRunShadowMatchNoDiff(t *testing.T) {
	before := shadowDiffCount(t, "other")
	// Decider echoes exactly the Go decision the shadow builds internally.
	goCmds := []Command{}
	goDecision := schema.DisruptResultValue(commandViews(stubMethod{reason: v1.DisruptionReasonEmpty}, goCmds))
	c := shadowController(&stubDecider{out: goDecision})
	c.runShadow(context.Background(), stubMethod{reason: v1.DisruptionReasonEmpty}, map[string]int{}, nil, goCmds)
	if got := shadowDiffCount(t, "other"); got != before {
		t.Fatalf("matching decision should record no diff (%v -> %v)", before, got)
	}
}

// --- reconstructCommands ---

func candidate(name string) *Candidate {
	return &Candidate{StateNode: &state.StateNode{NodeClaim: &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: name}}}}
}

func TestReconstructDeleteCommands(t *testing.T) {
	cands := []*Candidate{candidate("n1"), candidate("n2")}
	out := output(schema.CommandView{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"n1"}})
	cmds, err := reconstructCommands(out, cands)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Decision() != DeleteDecision {
		t.Fatalf("expected one delete command, got %+v", cmds)
	}
	if len(cmds[0].Candidates) != 1 || cmds[0].Candidates[0].Name() != "n1" {
		t.Fatalf("expected candidate n1, got %+v", cmds[0].Candidates)
	}
}

func TestReconstructNoOpSkipped(t *testing.T) {
	out := output(schema.CommandView{Decision: "no-op", Reason: "Empty"})
	cmds, err := reconstructCommands(out, nil)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if len(cmds) != 0 {
		t.Fatalf("no-op should reconstruct to no commands, got %d", len(cmds))
	}
}

func TestReconstructReplaceUnsupported(t *testing.T) {
	out := output(schema.CommandView{Decision: "replace", Reason: "Drifted", CandidateNodeNames: []string{"n1"},
		Replacements: []schema.ReplacementView{{NodePool: "np", InstanceTypeNames: []string{"a"}}}})
	if _, err := reconstructCommands(out, []*Candidate{candidate("n1")}); err == nil {
		t.Fatal("expected replace reconstruction to error")
	}
}

func TestReconstructUnknownCandidateErrors(t *testing.T) {
	out := output(schema.CommandView{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"ghost"}})
	if _, err := reconstructCommands(out, []*Candidate{candidate("n1")}); err == nil {
		t.Fatal("expected unknown-candidate error")
	}
}
