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

	"k8s.io/utils/clock"

	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// Node converts a StateNode into snapshot form. A StateNode with a NodeClaim but
// no backing Node is an in-flight node (capacity launched, not yet registered);
// it is recorded with role "inflight". All reads go through StateNode's accessors
// (Labels/Taints/Allocatable/...) which already choose between Node and NodeClaim
// data as the scheduler does, so the recorded view matches the scheduler's view.
//
// The clock is passed in (never read from the wall) so that Nominated — a
// time-relative predicate — is a pure function of the snapshot.
func Node(n *state.StateNode, clk clock.Clock) sexpr.Value {
	role := "existing"
	if n.Node == nil && n.NodeClaim != nil {
		role = "inflight"
	}
	return tagged("node",
		tagged("id", str(n.Name())),
		optStr("provider-id", n.ProviderID()),
		tagged("role", sym(role)),
		tagged("managed", boolv(n.Managed())),
		tagged("registered", boolv(n.Registered())),
		tagged("initialized", boolv(n.Initialized())),
		tagged("marked-for-deletion", boolv(n.MarkedForDeletion())),
		tagged("nominated", boolv(n.Nominated(clk))),
		labels(n.Labels()),
		taints("taints", n.Taints()),
		resourceList("capacity", n.Capacity()),
		resourceList("allocatable", n.Allocatable()),
		resourceList("available", n.Available()),
		resourceList("daemonset-requests", n.DaemonSetRequests()),
		resourceList("pod-requests", n.PodRequests()),
	)
}

// Nodes converts a slice of StateNodes, sorted by name for byte-stability.
func Nodes(tag string, nodes []*state.StateNode, clk clock.Clock) sexpr.Value {
	sorted := append([]*state.StateNode(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })
	entries := make([]sexpr.Value, 0, len(sorted)+1)
	entries = append(entries, sym(tag))
	for _, n := range sorted {
		entries = append(entries, Node(n, clk))
	}
	return sexpr.List(entries)
}
