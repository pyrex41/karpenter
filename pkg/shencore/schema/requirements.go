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

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// requirements emits (<tag> (req <key> <op> (<values>...) (min-values <n>))...).
//
// It defers to Karpenter's own Requirements.NodeSelectorRequirements(), which
// canonically flattens each requirement to one form (In / NotIn / Exists /
// DoesNotExist) or, for a requirement carrying both a lower and an upper numeric
// bound, to two forms (Gte then Lte). Using the scheduler's own serializer means
// the recorded operator preserves the complement encoding exactly as the
// scheduler sees it — NotIn (complement) and DoesNotExist (empty positive set)
// stay distinct.
//
// The flattened forms are then sorted by (key, operator, values) so the output is
// independent of the requirement map's iteration order.
func requirements(tag string, reqs scheduling.Requirements) sexpr.Value {
	return requirementsFrom(tag, reqs.NodeSelectorRequirements())
}

// requirementsExcludingKeys emits requirements with the named keys dropped. Used
// to strip the scheduler's internal hostname placeholder from a new NodeClaim's
// requirements before recording, since that value comes from a process-global
// atomic counter and is not part of the decision.
func requirementsExcludingKeys(tag string, reqs scheduling.Requirements, exclude ...string) sexpr.Value {
	excludeSet := make(map[string]struct{}, len(exclude))
	for _, k := range exclude {
		excludeSet[k] = struct{}{}
	}
	nsrs := reqs.NodeSelectorRequirements()
	kept := nsrs[:0]
	for _, nsr := range nsrs {
		if _, ok := excludeSet[nsr.Key]; ok {
			continue
		}
		kept = append(kept, nsr)
	}
	return requirementsFrom(tag, kept)
}

func requirementsFrom(tag string, nsrs []v1.NodeSelectorRequirementWithMinValues) sexpr.Value {
	forms := make([]sexpr.Value, len(nsrs))
	for i, nsr := range nsrs {
		vals := append([]string(nil), nsr.Values...)
		sort.Strings(vals)
		forms[i] = tagged("req",
			str(nsr.Key),
			sym(string(nsr.Operator)),
			stringList(vals),
			optInt("min-values", nsr.MinValues),
		)
	}
	sortForms(forms)
	return sexpr.List(append([]sexpr.Value{sym(tag)}, forms...))
}
