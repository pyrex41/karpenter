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

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/clock"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	pscheduling "sigs.k8s.io/karpenter/pkg/controllers/provisioning/scheduling"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// SolveInput is the deep-read snapshot handed to the scheduler for a single
// provisioning loop. It owns no live pointers.
type SolveInput struct {
	Clock         time.Time
	Pods          []*corev1.Pod
	Nodes         []*state.StateNode
	NodePools     []*v1.NodePool
	InstanceTypes map[string][]*cloudprovider.InstanceType
}

// SolveInputValue builds the (input ...) form and, separately, the
// content-addressed catalog value and its hash. The catalog is returned apart
// from the input so the recorder can write it once and reference it by hash,
// rather than inlining the (large) instance-type set into every record.
func SolveInputValue(in SolveInput, clk clock.Clock) (input sexpr.Value, catalog sexpr.Value, catalogHash string) {
	catalog, catalogHash = Catalog(in.InstanceTypes)
	input = tagged("input",
		tagged("clock", intv(in.Clock.UnixNano())),
		tagged("catalog-ref", str(catalogHash)),
		Pods("pods", in.Pods),
		Nodes("nodes", in.Nodes, clk),
		NodePools("nodepools", in.NodePools),
	)
	return input, catalog, catalogHash
}

// SolveResultValue converts a scheduler Results into the (output ...) decision
// form. The scheduler's placeholder hostnames (a process-global atomic counter)
// are normalized away: each new NodeClaim is given a stable id "nc-<n>" assigned
// by a deterministic sort over its content, and the corev1.LabelHostname
// requirement is dropped from the serialized requirements.
func SolveResultValue(r pscheduling.Results) sexpr.Value {
	return tagged("output",
		newNodeClaims(r.NewNodeClaims),
		existingPlacements(r.ExistingNodes),
		unschedulable(r.PodErrors),
		// Preference relaxations are applied inside the scheduler and are not
		// surfaced on Results today; emitted empty pending a scheduler hook.
		tagged("relaxations"),
	)
}

func newNodeClaims(ncs []*pscheduling.NodeClaim) sexpr.Value {
	// Sort by content key (nodepool, then sorted pod UIDs) so the stable id
	// assignment is independent of the scheduler's internal ordering.
	sorted := append([]*pscheduling.NodeClaim(nil), ncs...)
	sort.Slice(sorted, func(i, j int) bool { return newNodeClaimKey(sorted[i]) < newNodeClaimKey(sorted[j]) })
	entries := make([]sexpr.Value, 0, len(sorted)+1)
	entries = append(entries, sym("new-nodeclaims"))
	for idx, nc := range sorted {
		entries = append(entries, tagged("new-nodeclaim",
			tagged("id", str(stableNodeClaimID(idx))),
			tagged("nodepool", str(nc.NodePoolName)),
			requirementsExcludingKeys("reqs", nc.Requirements, corev1.LabelHostname),
			instanceTypeNames(nc.InstanceTypeOptions),
			podUIDs("pods", nc.Pods),
			resourceList("resources", resources.RequestsForPods(nc.Pods...)),
		))
	}
	return sexpr.List(entries)
}

func stableNodeClaimID(idx int) string {
	// nc-0000 style so lexical and numeric order agree up to 10k claims/loop.
	const digits = "0123456789"
	n := idx
	buf := []byte{'n', 'c', '-', '0', '0', '0', '0'}
	for i := len(buf) - 1; i >= 3 && n > 0; i-- {
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf)
}

func newNodeClaimKey(nc *pscheduling.NodeClaim) string {
	uids := lo.Map(nc.Pods, func(p *corev1.Pod, _ int) string { return string(p.UID) })
	sort.Strings(uids)
	key := nc.NodePoolName
	for _, u := range uids {
		key += "|" + u
	}
	return key
}

func instanceTypeNames(its cloudprovider.InstanceTypes) sexpr.Value {
	names := lo.Map(its, func(it *cloudprovider.InstanceType, _ int) string { return it.Name })
	return tagged("instance-types", sortedStringList(names))
}

func existingPlacements(nodes []*pscheduling.ExistingNode) sexpr.Value {
	// Only nodes that actually received pods represent a placement decision.
	placed := lo.Filter(nodes, func(n *pscheduling.ExistingNode, _ int) bool { return len(n.Pods) > 0 })
	sort.Slice(placed, func(i, j int) bool { return placed[i].Name() < placed[j].Name() })
	entries := make([]sexpr.Value, 0, len(placed)+1)
	entries = append(entries, sym("existing-placements"))
	for _, n := range placed {
		entries = append(entries, tagged("placement",
			tagged("node", str(n.Name())),
			podUIDs("pods", n.Pods),
		))
	}
	return sexpr.List(entries)
}

// unschedulable emits (unschedulable (pod-error (id ...) (kind ...) (reason ...))...).
// The kind classifies the failure using Karpenter's own error predicates so the
// decision distinguishes a genuine unschedulable pod from a deferred one
// (reserved-offering) or a DRA-unsupported pod. The reason string is the error
// text; it is deterministic for the built-in scheduler messages.
func unschedulable(podErrors map[*corev1.Pod]error) sexpr.Value {
	type row struct {
		uid    string
		kind   string
		reason string
	}
	rows := make([]row, 0, len(podErrors))
	for p, err := range podErrors {
		kind := "other"
		switch {
		case pscheduling.IsReservedOfferingError(err):
			kind = "reserved-offering"
		case pscheduling.IsDRAError(err):
			kind = "dra"
		}
		rows = append(rows, row{uid: string(p.UID), kind: kind, reason: err.Error()})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].uid < rows[j].uid })
	entries := make([]sexpr.Value, 0, len(rows)+1)
	entries = append(entries, sym("unschedulable"))
	for _, r := range rows {
		entries = append(entries, tagged("pod-error",
			tagged("id", str(r.uid)),
			tagged("kind", sym(r.kind)),
			tagged("reason", str(r.reason)),
		))
	}
	return sexpr.List(entries)
}
