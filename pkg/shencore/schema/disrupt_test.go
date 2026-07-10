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

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

func sampleCandidates() []CandidateView {
	return []CandidateView{
		{NodeName: "node-b", NodePool: "np", InstanceType: "m5.large", Zone: "z1", CapacityType: "spot", Price: 0.05, DisruptionCost: 3.5, ReschedulablePodUIDs: []string{"u2", "u1"}},
		{NodeName: "node-a", NodePool: "np", InstanceType: "m5.xlarge", Zone: "z2", CapacityType: "on-demand", Price: 0.1, Empty: true},
	}
}

func sampleCommands() []CommandView {
	return []CommandView{
		{Decision: "delete", Reason: "Empty", CandidateNodeNames: []string{"node-b", "node-a"}, EstimatedSavings: 0.15},
		{Decision: "replace", Reason: "Drifted", CandidateNodeNames: []string{"node-a"}, EstimatedSavings: 0.03,
			Replacements: []ReplacementView{{NodePool: "np", InstanceTypeNames: []string{"m5.large", "c5.large"}}}},
	}
}

// TestDisruptDeterministic guards byte-stability of the disrupt contract across
// candidate/command/budget orderings.
func TestDisruptDeterministic(t *testing.T) {
	clk := time.Unix(1700000000, 0)
	budgets := map[string]int{"np-z": 2, "np-a": 5}
	first, err := sexpr.Encode(DisruptInputValue(clk, "Empty", sampleCandidates(), budgets))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := 0; i < 100; i++ {
		b, _ := sexpr.Encode(DisruptInputValue(clk, "Empty", sampleCandidates(), budgets))
		if !bytes.Equal(first, b) {
			t.Fatalf("disrupt input non-deterministic at iter %d", i)
		}
	}
	firstOut, _ := sexpr.Encode(DisruptResultValue(sampleCommands()))
	for i := 0; i < 100; i++ {
		b, _ := sexpr.Encode(DisruptResultValue(sampleCommands()))
		if !bytes.Equal(firstOut, b) {
			t.Fatalf("disrupt output non-deterministic at iter %d", i)
		}
	}
}

// TestDisruptRecordRoundTrip asserts a disrupt record round-trips byte-for-byte.
func TestDisruptRecordRoundTrip(t *testing.T) {
	clk := time.Unix(1700000000, 0)
	in := DisruptInputValue(clk, "Empty", sampleCandidates(), map[string]int{"np": 1})
	out := DisruptResultValue(sampleCommands())
	encoded, err := EncodeRecord(Record("disrupt", in, out))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeRecord(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	reencoded, _ := EncodeRecord(decoded)
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("disrupt record not byte-stable")
	}
}
