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
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/shencore"
	"sigs.k8s.io/karpenter/pkg/shencore/schema"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

const (
	// shenDisruptionEntrypoint is the Shen function the disruption core exposes.
	// It takes the schema (input ...) form and returns the (output ...) form.
	// This is the contract handed off to the Shen disruption decision core.
	shenDisruptionEntrypoint = "shencore.disrupt"
	// envDisruptionSource overrides the path to shen/core/disruption.shen for the
	// interpreter dev-mode loader.
	envDisruptionSource = "SHENCORE_DISRUPTION_SOURCE"
	// defaultDisruptionSource is the in-repo decision source, resolved relative to
	// the working directory in interpreter dev-mode.
	defaultDisruptionSource = "shen/core/disruption.shen"
)

// disruptionSourcePath resolves the Shen disruption decision source.
func disruptionSourcePath() string {
	if p := os.Getenv(envDisruptionSource); p != "" {
		return p
	}
	return defaultDisruptionSource
}

// ShenDecider computes a disruption decision from a serialized snapshot. The
// input is the schema (input ...) form; the return is the schema (output ...)
// form. It is the seam between the disruption controller and the Shen decision
// core: the production implementation drives the shen-go VM, while tests inject a
// stub. Because the kl runtime is a process-global single-threaded VM, a decider
// backed by it must serialize its own calls (the shencore.Engine already does).
type ShenDecider interface {
	Decide(ctx context.Context, input sexpr.Value) (sexpr.Value, error)
}

// computeDisruptionCommands routes a method's decision through the configured
// decision engine. `go` (and the empty default) runs the Go path unchanged.
// `shadow` runs the Go path authoritatively and, alongside it, runs the Shen path
// and diffs the two decisions without ever letting the Shen path affect the
// returned commands. `shen` lets the Shen decision core decide.
func (c *Controller) computeDisruptionCommands(ctx context.Context, method Method, budgets map[string]int, candidates []*Candidate) ([]Command, error) {
	switch options.FromContext(ctx).DisruptionDecisionEngine {
	case options.DecisionEngineShen:
		return c.shenComputeCommands(ctx, method, budgets, candidates)
	case options.DecisionEngineShadow:
		cmds, err := method.ComputeCommands(ctx, budgets, candidates...)
		if err != nil {
			return nil, err
		}
		// The shadow comparison must never influence the authoritative result, so
		// it runs after the Go decision is in hand and its outcome is discarded.
		c.runShadow(ctx, method, budgets, candidates, cmds)
		return cmds, nil
	default: // DecisionEngineGo or unset
		return method.ComputeCommands(ctx, budgets, candidates...)
	}
}

// runShadow computes the Shen decision alongside the authoritative Go decision and
// records any divergence. It is fully contained: a Shen error or panic is counted
// under reason=shen_error and swallowed, and the Go decision (already returned to
// the caller) is untouched.
func (c *Controller) runShadow(ctx context.Context, method Method, budgets map[string]int, candidates []*Candidate, goCmds []Command) {
	reason := "" // set to a non-empty diff reason, or shen_error
	defer func() {
		if r := recover(); r != nil {
			reason = shencore.ShadowReasonShenError
			log.FromContext(ctx).V(1).Info("shencore shadow decision panicked; go decision unaffected", "recover", fmt.Sprint(r))
		}
		if reason != "" {
			shencore.ShadowDiffsTotal.Inc(map[string]string{
				shencore.ControllerLabel:   c.Name(),
				shencore.ShadowReasonLabel: reason,
			})
		}
	}()

	decider := c.resolveShenDecider(ctx)
	if decider == nil {
		reason = shencore.ShadowReasonShenError
		return
	}
	input := schema.DisruptInputValue(c.clock.Now(), string(method.Reason()), candidateViews(candidates), budgets)
	shenOut, err := decider.Decide(ctx, input)
	if err != nil {
		reason = shencore.ShadowReasonShenError
		log.FromContext(ctx).V(1).Info("shencore shadow decision errored; go decision unaffected", "error", err.Error())
		return
	}
	goOut := schema.DisruptResultValue(commandViews(method, goCmds))
	if r, differs := diffDecisions(goOut, shenOut); differs {
		reason = r
		log.FromContext(ctx).V(1).Info("shencore shadow decision diverged from go decision",
			"reason", r, "method", string(method.Reason()))
	}
}

// shenComputeCommands lets the Shen decision core decide authoritatively. It
// reconstructs the delete/no-op decisions the Shen output describes; replacement
// (consolidation) decisions require re-running the scheduler to materialize the
// replacement NodeClaims and are not yet reconstructable here, so a replace
// decision is a loud error rather than a silently dropped action.
func (c *Controller) shenComputeCommands(ctx context.Context, method Method, budgets map[string]int, candidates []*Candidate) ([]Command, error) {
	decider := c.resolveShenDecider(ctx)
	if decider == nil {
		return nil, fmt.Errorf("shen decision engine unavailable")
	}
	input := schema.DisruptInputValue(c.clock.Now(), string(method.Reason()), candidateViews(candidates), budgets)
	out, err := decider.Decide(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("shen disruption decision, %w", err)
	}
	return reconstructCommands(out, candidates)
}

// resolveShenDecider returns the decider to use: the test-injected one if set,
// otherwise the lazily-built process singleton backed by the shen-go VM. A build
// failure is logged once and surfaces as a nil decider (contained by callers).
func (c *Controller) resolveShenDecider(ctx context.Context) ShenDecider {
	if c.shenDecider != nil {
		return c.shenDecider
	}
	d, err := sharedShenDisruptionDecider()
	if err != nil {
		log.FromContext(ctx).V(1).Info("shencore disruption decider unavailable", "error", err.Error())
		return nil
	}
	return d
}

// reconstructCommands turns a schema (output (commands ...)) form back into
// disruption Commands, mapping candidate node names to the live *Candidate values
// that were passed in. Only delete and no-op decisions are reconstructed.
func reconstructCommands(out sexpr.Value, candidates []*Candidate) ([]Command, error) {
	byName := make(map[string]*Candidate, len(candidates))
	for _, c := range candidates {
		byName[c.Name()] = c
	}
	commands, ok := field(out, "commands")
	if !ok {
		return nil, fmt.Errorf("shen output missing commands")
	}
	var cmds []Command
	for _, cmdForm := range tail(commands) {
		decision := symField(cmdForm, "decision")
		switch Decision(decision) {
		case NoOpDecision:
			continue
		case ReplaceDecision:
			return nil, fmt.Errorf("shen-authoritative replace decisions are not yet supported")
		case DeleteDecision:
			names := stringsField(cmdForm, "candidates")
			matched := make([]*Candidate, 0, len(names))
			for _, n := range names {
				cand, ok := byName[n]
				if !ok {
					return nil, fmt.Errorf("shen decision names unknown candidate %q", n)
				}
				matched = append(matched, cand)
			}
			if len(matched) > 0 {
				cmds = append(cmds, Command{Candidates: matched})
			}
		default:
			return nil, fmt.Errorf("shen decision has unknown decision %q", decision)
		}
	}
	return cmds, nil
}

// diffDecisions compares the authoritative Go decision to the Shen decision, both
// as schema (output ...) forms. It returns ("", false) when they match, else a
// low-cardinality reason (for the metric label) and true. The comparison is over
// the canonically-ordered command list the schema produces, so it is order-stable.
func diffDecisions(goOut, shenOut sexpr.Value) (string, bool) {
	goBytes, err1 := sexpr.Encode(goOut)
	shenBytes, err2 := sexpr.Encode(shenOut)
	if err1 == nil && err2 == nil && string(goBytes) == string(shenBytes) {
		return "", false
	}
	goCmds, _ := field(goOut, "commands")
	shenCmds, _ := field(shenOut, "commands")
	g, s := tail(goCmds), tail(shenCmds)
	if len(g) != len(s) {
		return shencore.ShadowReasonCommandCount, true
	}
	for i := range g {
		if symField(g[i], "decision") != symField(s[i], "decision") {
			return shencore.ShadowReasonDecisions, true
		}
		if !equalStrings(stringsField(g[i], "candidates"), stringsField(s[i], "candidates")) {
			return shencore.ShadowReasonCandidates, true
		}
		if gr, sr := encodeField(g[i], "replacements"), encodeField(s[i], "replacements"); gr != sr {
			return shencore.ShadowReasonReplacements, true
		}
		if intField(g[i], "estimated-savings") != intField(s[i], "estimated-savings") {
			return shencore.ShadowReasonSavings, true
		}
	}
	return shencore.ShadowReasonOther, true
}

// --- small sexpr navigation helpers over the fixed schema shapes ---

// field returns the (tag ...) sub-form of a keyword-headed list v.
func field(v sexpr.Value, tag string) (sexpr.Value, bool) {
	list, ok := v.(sexpr.List)
	if !ok {
		return nil, false
	}
	for _, e := range list {
		el, ok := e.(sexpr.List)
		if !ok || len(el) == 0 {
			continue
		}
		if s, ok := el[0].(sexpr.Symbol); ok && string(s) == tag {
			return el, true
		}
	}
	return nil, false
}

// tail returns the elements of a list after its head symbol.
func tail(v sexpr.Value) []sexpr.Value {
	list, ok := v.(sexpr.List)
	if !ok || len(list) == 0 {
		return nil
	}
	return list[1:]
}

// symField returns the symbol value of (tag <sym>) within v.
func symField(v sexpr.Value, tag string) string {
	f, ok := field(v, tag)
	if !ok {
		return ""
	}
	if t := tail(f); len(t) == 1 {
		if s, ok := t[0].(sexpr.Symbol); ok {
			return string(s)
		}
	}
	return ""
}

// intField returns the integer value of (tag <int>) within v.
func intField(v sexpr.Value, tag string) int64 {
	f, ok := field(v, tag)
	if !ok {
		return 0
	}
	if t := tail(f); len(t) == 1 {
		if n, ok := t[0].(sexpr.Int); ok {
			return int64(n)
		}
	}
	return 0
}

// stringsField returns the string members of (tag (<str>...)) within v.
func stringsField(v sexpr.Value, tag string) []string {
	f, ok := field(v, tag)
	if !ok {
		return nil
	}
	t := tail(f)
	if len(t) != 1 {
		return nil
	}
	inner, ok := t[0].(sexpr.List)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(inner))
	for _, e := range inner {
		if s, ok := e.(sexpr.String); ok {
			out = append(out, string(s))
		}
	}
	return out
}

// encodeField returns the canonical encoding of the (tag ...) sub-form, for
// coarse equality of a nested structure.
func encodeField(v sexpr.Value, tag string) string {
	f, ok := field(v, tag)
	if !ok {
		return ""
	}
	b, _ := sexpr.Encode(f)
	return string(b)
}

func equalStrings(a, b []string) bool {
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

// --- production Shen decider (process singleton) ---

// sharedShenDisruptionDecider lazily builds the process-wide Shen decider backed
// by the shen-go VM. The kl runtime is a single global; one decider wraps it and
// serializes calls. Build failure is memoized so a missing decision source does
// not retry the (expensive) kernel bootstrap on every pass.
func sharedShenDisruptionDecider() (ShenDecider, error) {
	shenDeciderOnce.Do(func() {
		shenDeciderInst, shenDeciderErr = newShencoreDisruptionDecider()
	})
	return shenDeciderInst, shenDeciderErr
}

//nolint:gochecknoglobals // process-global by design: mirrors the single kl VM.
var (
	shenDeciderOnce sync.Once
	shenDeciderInst ShenDecider
	shenDeciderErr  error
)

// shencoreDisruptionDecider drives shen/core/disruption.shen through the shencore
// Engine. It is the handoff point for the Shen disruption decision core: it loads
// the decision source and calls its entrypoint with the schema snapshot.
type shencoreDisruptionDecider struct {
	engine     *shencore.Engine
	entrypoint string
}

func newShencoreDisruptionDecider() (ShenDecider, error) {
	src := disruptionSourcePath()
	engine, err := shencore.New(shencore.Options{Interpret: []string{src}})
	if err != nil {
		return nil, fmt.Errorf("loading shen disruption source %q: %w", src, err)
	}
	return &shencoreDisruptionDecider{engine: engine, entrypoint: shenDisruptionEntrypoint}, nil
}

func (d *shencoreDisruptionDecider) Decide(_ context.Context, input sexpr.Value) (sexpr.Value, error) {
	return d.engine.Call(d.entrypoint, input)
}
