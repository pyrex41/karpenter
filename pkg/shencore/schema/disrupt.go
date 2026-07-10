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

package schema

import (
	"sort"
	"time"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// The disruption converters take primitive "view" structs rather than the live
// disruption.Candidate/Command types. This keeps the schema package free of a
// dependency on the disruption controller (which imports the recorder, which
// imports this package — a cycle otherwise). The disruption controller extracts
// these views from its types at the record site.

// CandidateView is the disruption-relevant projection of a disruption candidate.
type CandidateView struct {
	NodeName             string
	NodePool             string
	InstanceType         string
	Zone                 string
	CapacityType         string
	Price                float64
	DisruptionCost       float64
	Empty                bool
	ReschedulablePodUIDs []string
}

// ReplacementView is the projection of a single replacement NodeClaim in a
// disruption command.
type ReplacementView struct {
	NodePool          string
	InstanceTypeNames []string
}

// CommandView is the disruption-relevant projection of a disruption command.
type CommandView struct {
	Decision           string
	Reason             string
	CandidateNodeNames []string
	Replacements       []ReplacementView
	EstimatedSavings   float64
}

// DisruptInputValue builds the (input ...) form for a disruption pass: the clock,
// the method under evaluation, the candidate nodes, and the per-NodePool budget
// allowances. Candidates are sorted by node name; budgets by NodePool name.
func DisruptInputValue(clk time.Time, method string, candidates []CandidateView, budgets map[string]int) sexpr.Value {
	sortedCands := append([]CandidateView(nil), candidates...)
	sort.Slice(sortedCands, func(i, j int) bool { return sortedCands[i].NodeName < sortedCands[j].NodeName })
	candForms := make([]sexpr.Value, 0, len(sortedCands)+1)
	candForms = append(candForms, sym("candidates"))
	for _, c := range sortedCands {
		candForms = append(candForms, tagged("candidate",
			tagged("node", str(c.NodeName)),
			tagged("nodepool", str(c.NodePool)),
			optStr("instance-type", c.InstanceType),
			optStr("zone", c.Zone),
			optStr("capacity-type", c.CapacityType),
			tagged("price", intv(normalizePrice(c.Price))),
			tagged("disruption-cost", intv(normalizePrice(c.DisruptionCost))),
			tagged("empty", boolv(c.Empty)),
			tagged("reschedulable-pods", sortedStringList(c.ReschedulablePodUIDs)),
		))
	}

	budgetKeys := make([]string, 0, len(budgets))
	for k := range budgets {
		budgetKeys = append(budgetKeys, k)
	}
	sort.Strings(budgetKeys)
	budgetForms := make([]sexpr.Value, 0, len(budgetKeys)+1)
	budgetForms = append(budgetForms, sym("budgets"))
	for _, k := range budgetKeys {
		budgetForms = append(budgetForms, tagged("budget", str(k), intv(int64(budgets[k]))))
	}

	return tagged("input",
		tagged("clock", intv(clk.UnixNano())),
		tagged("method", str(method)),
		sexpr.List(candForms),
		sexpr.List(budgetForms),
	)
}

// DisruptResultValue builds the (output ...) form for a disruption pass: the
// commands the method decided on. Commands are sorted by their canonical
// encoding so an unordered command slice serializes stably.
func DisruptResultValue(commands []CommandView) sexpr.Value {
	cmdForms := make([]sexpr.Value, len(commands))
	for i, c := range commands {
		replForms := make([]sexpr.Value, len(c.Replacements))
		for j, r := range c.Replacements {
			replForms[j] = tagged("replacement",
				tagged("nodepool", str(r.NodePool)),
				tagged("instance-types", sortedStringList(r.InstanceTypeNames)),
			)
		}
		sortForms(replForms)
		cmdForms[i] = tagged("command",
			tagged("decision", sym(c.Decision)),
			optStr("reason", c.Reason),
			tagged("candidates", sortedStringList(c.CandidateNodeNames)),
			sexpr.List(append([]sexpr.Value{sym("replacements")}, replForms...)),
			tagged("estimated-savings", intv(normalizePrice(c.EstimatedSavings))),
		)
	}
	sortForms(cmdForms)
	return tagged("output", sexpr.List(append([]sexpr.Value{sym("commands")}, cmdForms...)))
}
