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
	"math"
	"sort"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// priceScale converts a floating point hourly price into an integer of
// nano-dollars. Offering prices are float64; embedding them as formatted floats
// would make byte-stability depend on float printing, so every price is rounded
// to a fixed-precision integer here. Nanos give ample resolution for real prices
// while collapsing float noise below 1e-9.
const priceScale = 1e9

func normalizePrice(p float64) int64 {
	if math.IsNaN(p) || math.IsInf(p, 0) {
		return 0
	}
	return int64(math.Round(p * priceScale))
}

// InstanceType converts one instance type into snapshot form. The offering list
// is sorted by (capacity-type, zone, price) so a shuffled offering slice produces
// identical bytes.
func InstanceType(it *cloudprovider.InstanceType) sexpr.Value {
	offeringForms := make([]sexpr.Value, len(it.Offerings))
	for i, o := range it.Offerings {
		offeringForms[i] = tagged("offering",
			tagged("capacity-type", str(o.CapacityType())),
			tagged("zone", str(o.Zone())),
			tagged("price", intv(normalizePrice(o.Price))),
			tagged("available", boolv(o.Available)),
			tagged("reservation-capacity", intv(int64(o.ReservationCapacity))),
		)
	}
	sortForms(offeringForms)
	fields := []sexpr.Value{
		tagged("name", str(it.Name)),
		resourceList("capacity", it.Capacity),
		requirements("reqs", it.Requirements),
		sexpr.List(append([]sexpr.Value{sym("offerings")}, offeringForms...)),
	}
	if it.Overhead != nil {
		fields = append(fields, tagged("overhead",
			resourceList("kube-reserved", it.Overhead.KubeReserved),
			resourceList("system-reserved", it.Overhead.SystemReserved),
			resourceList("eviction-threshold", it.Overhead.EvictionThreshold),
		))
	} else {
		fields = append(fields, tagged("overhead"))
	}
	return tagged("instance-type", fields...)
}

// Catalog builds the full content-addressable instance-type catalog for a
// scheduling loop: for each NodePool, the instance types offered to it. The same
// instance type can appear under multiple NodePools with different overlays
// (price/capacity), so entries are keyed by NodePool rather than deduplicated by
// name. The catalog is serialized once and referenced by hash from each record,
// which keeps the corpus small (the catalog rarely changes between records).
//
// Returns the catalog value and its content hash.
func Catalog(instanceTypes map[string][]*cloudprovider.InstanceType) (sexpr.Value, string) {
	npNames := make([]string, 0, len(instanceTypes))
	for name := range instanceTypes {
		npNames = append(npNames, name)
	}
	sort.Strings(npNames)
	entries := make([]sexpr.Value, 0, len(npNames)+1)
	entries = append(entries, sym("catalog"))
	for _, np := range npNames {
		its := append([]*cloudprovider.InstanceType(nil), instanceTypes[np]...)
		sort.Slice(its, func(i, j int) bool { return its[i].Name < its[j].Name })
		itForms := make([]sexpr.Value, 0, len(its)+2)
		itForms = append(itForms, sym("nodepool-instance-types"), str(np))
		for _, it := range its {
			itForms = append(itForms, InstanceType(it))
		}
		entries = append(entries, sexpr.List(itForms))
	}
	catalog := sexpr.List(entries)
	return catalog, hashValue(catalog)
}
