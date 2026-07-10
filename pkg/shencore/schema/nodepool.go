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

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// NodePool converts a NodePool into snapshot form: the weight/limits/replicas
// that gate provisioning, the disruption policy and budgets that gate disruption,
// and the template (labels, taints, requirements) a new NodeClaim inherits.
func NodePool(np *v1.NodePool) sexpr.Value {
	var replicas *int
	if np.Spec.Replicas != nil {
		r := int(*np.Spec.Replicas)
		replicas = &r
	}
	var weight int64
	if np.Spec.Weight != nil {
		weight = int64(*np.Spec.Weight)
	}
	return tagged("nodepool",
		tagged("name", str(np.Name)),
		tagged("uuid", str(string(np.UID))),
		tagged("weight", intv(weight)),
		optInt("replicas", replicas),
		resourceList("limits", corev1.ResourceList(np.Spec.Limits)),
		nodePoolDisruption(np),
		nodePoolTemplate(np),
	)
}

// NodePools converts a slice of NodePools, sorted by name.
func NodePools(tag string, nps []*v1.NodePool) sexpr.Value {
	sorted := append([]*v1.NodePool(nil), nps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	entries := make([]sexpr.Value, 0, len(sorted)+1)
	entries = append(entries, sym(tag))
	for _, np := range sorted {
		entries = append(entries, NodePool(np))
	}
	return sexpr.List(entries)
}

func nodePoolDisruption(np *v1.NodePool) sexpr.Value {
	d := np.Spec.Disruption
	var consolidateAfter *int64
	if d.ConsolidateAfter.Duration != nil {
		ca := d.ConsolidateAfter.Duration.Nanoseconds()
		consolidateAfter = &ca
	}
	budgets := append([]v1.Budget(nil), d.Budgets...)
	// Budgets have no natural key; sort by their canonical encoding.
	budgetForms := make([]sexpr.Value, len(budgets))
	for i, b := range budgets {
		reasons := lo.Map(b.Reasons, func(r v1.DisruptionReason, _ int) string { return string(r) })
		sort.Strings(reasons)
		budgetForms[i] = tagged("budget",
			tagged("nodes", str(b.Nodes)),
			optStr("schedule", lo.FromPtr(b.Schedule)),
			budgetDuration(b),
			tagged("reasons", stringList(reasons)),
		)
	}
	sortForms(budgetForms)
	return tagged("disruption",
		tagged("consolidation-policy", str(string(d.ConsolidationPolicy))),
		optInt64("consolidate-after", consolidateAfter),
		sexpr.List(append([]sexpr.Value{sym("budgets")}, budgetForms...)),
	)
}

func budgetDuration(b v1.Budget) sexpr.Value {
	if b.Duration == nil {
		return tagged("duration")
	}
	return tagged("duration", intv(b.Duration.Duration.Nanoseconds()))
}

func nodePoolTemplate(np *v1.NodePool) sexpr.Value {
	tmpl := np.Spec.Template
	// Reproduce the requirement set the scheduler builds from the template: the
	// declared requirements plus label-derived requirements.
	reqs := scheduling.NewRequirements()
	reqs.Add(scheduling.NewNodeSelectorRequirementsWithMinValues(tmpl.Spec.Requirements...).Values()...)
	reqs.Add(scheduling.NewLabelRequirements(tmpl.Labels).Values()...)
	return tagged("template",
		labels(tmpl.Labels),
		taints("taints", tmpl.Spec.Taints),
		taints("startup-taints", tmpl.Spec.StartupTaints),
		requirements("reqs", reqs),
	)
}
