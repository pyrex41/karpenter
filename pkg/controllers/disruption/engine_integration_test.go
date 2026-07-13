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
	"os"
	"testing"
	"time"

	"sigs.k8s.io/karpenter/pkg/shencore/schema"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// TestShencoreDeciderRealRoundTrip drives the real shen-go-backed decider through
// a stub decision source, proving the shadow/shen plumbing loads a .shen file and
// evaluates the shencore.disrupt entrypoint against a live schema snapshot. It is
// skipped unless SHENCORE_DISRUPTION_SOURCE points at a decision source, so the
// ordinary hermetic disruption tests never pay the kl-kernel bootstrap. Run with:
//
//	SHENCORE_DISRUPTION_SOURCE=$(pwd)/pkg/shencore/testdata/disruption_stub.shen \
//	  go test ./pkg/controllers/disruption/ -run TestShencoreDeciderRealRoundTrip
func TestShencoreDeciderRealRoundTrip(t *testing.T) {
	if os.Getenv(envDisruptionSource) == "" {
		t.Skipf("set %s to a .shen decision source to run the real Shen round-trip", envDisruptionSource)
	}
	decider, err := newShencoreDisruptionDecider()
	if err != nil {
		t.Fatalf("building shen decider: %v", err)
	}
	input := schema.DisruptInputValue(time.Unix(0, 0), "Empty", nil, map[string]int{})
	out, err := decider.Decide(context.Background(), input)
	if err != nil {
		t.Fatalf("shen decide: %v", err)
	}
	// The stub returns an empty no-op decision: (output (commands)).
	cmds, ok := field(out, "commands")
	if !ok {
		t.Fatalf("shen output missing commands: %s", mustEncode(t, out))
	}
	if got := len(tail(cmds)); got != 0 {
		t.Fatalf("stub should decide no commands, got %d", got)
	}
	// The decision must reconstruct cleanly (no commands, no error).
	reconstructed, err := reconstructCommands(out, nil)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if len(reconstructed) != 0 {
		t.Fatalf("expected no reconstructed commands, got %d", len(reconstructed))
	}
}

func mustEncode(t *testing.T, v sexpr.Value) string {
	t.Helper()
	b, err := sexpr.Encode(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(b)
}
