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

package scheduling

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/karpenter/pkg/scheduling"
)

var _ = Describe("TopologyGroup Internals", func() {
	Describe("nextDomainAffinity", func() {
		var tg *TopologyGroup
		var pod *corev1.Pod
		var zones []string

		BeforeEach(func() {
			pod = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Labels:    map[string]string{"app": "test"},
			}}
			zones = []string{}
			domainGroup := NewTopologyDomainGroup()
			for i := range 10 {
				zone := fmt.Sprintf("zone-%d", i)
				zones = append(zones, zone)
				domainGroup.Insert(zone)
			}
			tg = NewTopologyGroup(
				TopologyTypePodAffinity,
				corev1.LabelTopologyZone,
				pod,
				sets.New("default"),
				&metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
				1,
				nil,
				nil,
				nil,
				domainGroup,
			)
		})
		It("should bootstrap self-affinity to a single domain when pod and node domains intersect", func() {
			// Prior to the fix, the bootstrap fallback loop ran unconditionally and could insert a second random
			// domain alongside the intersected one. A two-value requirement is never recorded by Topology.Record,
			// so the group's domain was never pinned. Iterate to guard against random map ordering hiding a regression.
			podDomains := scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zones...)
			nodeDomains := scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "zone-0")
			for range 10 {
				options := tg.nextDomainAffinity(pod, podDomains, nodeDomains)
				Expect(options.Values()).To(ConsistOf("zone-0"))
			}
		})
		It("should bootstrap self-affinity to a single domain when pod and node domains don't intersect", func() {
			podDomains := scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zones...)
			nodeDomains := scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "other-zone")
			for range 10 {
				options := tg.nextDomainAffinity(pod, podDomains, nodeDomains)
				Expect(options.Len()).To(Equal(1))
				Expect(zones).To(ContainElement(options.Values()[0]))
			}
		})
	})
})
