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
	"bytes"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"

	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// samplePod builds a pod exercising most of the serialized surface: multi-key
// labels, resource requests, tolerations, a topology spread constraint, and host
// ports — all in an order chosen to differ from sorted order.
func samplePod(uid string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uid),
			Namespace: "team-b",
			Name:      "web",
			Labels:    map[string]string{"zeta": "1", "alpha": "2", "mu": "3"},
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{"kubernetes.io/os": "linux", "topology.kubernetes.io/zone": "us-west-2a"},
			Tolerations: []corev1.Toleration{
				{Key: "z-taint", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
				{Key: "a-taint", Operator: corev1.TolerationOpEqual, Value: "v", Effect: corev1.TaintEffectNoExecute},
			},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
				MaxSkew:           1,
				TopologyKey:       "topology.kubernetes.io/zone",
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			}},
			Containers: []corev1.Container{{
				Name: "c",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				}},
				Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
			}},
		},
	}
}

// TestEncodeIsDeterministic guards the load-bearing property: encoding the same
// logical value repeatedly yields identical bytes. A converter that ranges a map
// or set without sorting would flake this within a handful of iterations.
func TestEncodeIsDeterministic(t *testing.T) {
	v := Pod(samplePod("uid-1"))
	first, err := sexpr.Encode(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := 0; i < 200; i++ {
		// Rebuild from scratch each iteration so any map-order dependence surfaces.
		b, err := sexpr.Encode(Pod(samplePod("uid-1")))
		if err != nil {
			t.Fatalf("encode iter %d: %v", i, err)
		}
		if !bytes.Equal(first, b) {
			t.Fatalf("non-deterministic encoding at iter %d:\n first=%s\n  got=%s", i, first, b)
		}
	}
}

// TestRoundTripIdentity asserts decode∘encode is the identity on schema output —
// the invariant the parity replay harness relies on.
func TestRoundTripIdentity(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(1700000000, 0))
	in := SolveInput{
		Clock: clk.Now(),
		Pods:  []*corev1.Pod{samplePod("uid-2"), samplePod("uid-1")},
	}
	input, _, _ := SolveInputValue(in, clk)
	record := Record("solve", input, tagged("output"))
	encoded, err := EncodeRecord(record)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	decoded, err := DecodeRecord(encoded)
	if err != nil {
		t.Fatalf("decode record: %v", err)
	}
	reencoded, err := EncodeRecord(decoded)
	if err != nil {
		t.Fatalf("re-encode record: %v", err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("round trip not identity:\n orig=%s\n  got=%s", encoded, reencoded)
	}
}

// TestPodOrderIndependence asserts the pod batch order is not an input to the
// serialization: the same pods in a different slice order produce identical bytes.
func TestPodOrderIndependence(t *testing.T) {
	a := Pods("pods", []*corev1.Pod{samplePod("b"), samplePod("a"), samplePod("c")})
	b := Pods("pods", []*corev1.Pod{samplePod("c"), samplePod("b"), samplePod("a")})
	ba, _ := sexpr.Encode(a)
	bb, _ := sexpr.Encode(b)
	if !bytes.Equal(ba, bb) {
		t.Fatalf("pod order leaked into encoding:\n %s\n %s", ba, bb)
	}
}

// TestRequirementComplementEncoding verifies the operator (and thus the
// complement flag) survives serialization for In / NotIn / Exists / DoesNotExist.
func TestRequirementComplementEncoding(t *testing.T) {
	reqs := scheduling.NewRequirements(
		scheduling.NewRequirement("in-key", corev1.NodeSelectorOpIn, "x", "y"),
		scheduling.NewRequirement("notin-key", corev1.NodeSelectorOpNotIn, "z"),
		scheduling.NewRequirement("exists-key", corev1.NodeSelectorOpExists),
		scheduling.NewRequirement("dne-key", corev1.NodeSelectorOpDoesNotExist),
	)
	b, err := sexpr.Encode(requirements("reqs", reqs))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(b)
	for _, want := range []string{"In", "NotIn", "Exists", "DoesNotExist"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("operator %q missing from encoding: %s", want, s)
		}
	}
}
