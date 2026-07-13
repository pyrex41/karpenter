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

package options_test

import (
	"flag"
	"os"
	"testing"

	"sigs.k8s.io/karpenter/pkg/operator/options"
)

func parseWith(t *testing.T, args ...string) (*options.Options, error) {
	t.Helper()
	// Isolate from process env so a set DISRUPTION_DECISION_ENGINE doesn't leak in.
	// AddFlags reads the env default at definition time, so it must be unset first.
	os.Unsetenv("DISRUPTION_DECISION_ENGINE")
	fs := &options.FlagSet{FlagSet: flag.NewFlagSet("karpenter", flag.ContinueOnError)}
	opts := &options.Options{}
	opts.AddFlags(fs)
	err := opts.Parse(fs, args...)
	return opts, err
}

func TestDisruptionDecisionEngineDefault(t *testing.T) {
	opts, err := parseWith(t)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.DisruptionDecisionEngine != options.DecisionEngineGo {
		t.Fatalf("default should be %q, got %q", options.DecisionEngineGo, opts.DisruptionDecisionEngine)
	}
}

func TestDisruptionDecisionEngineValid(t *testing.T) {
	for _, want := range []options.DecisionEngine{options.DecisionEngineGo, options.DecisionEngineShadow, options.DecisionEngineShen} {
		opts, err := parseWith(t, "--disruption-decision-engine", string(want))
		if err != nil {
			t.Fatalf("parse %q: %v", want, err)
		}
		if opts.DisruptionDecisionEngine != want {
			t.Fatalf("expected %q, got %q", want, opts.DisruptionDecisionEngine)
		}
	}
}

func TestDisruptionDecisionEngineInvalid(t *testing.T) {
	if _, err := parseWith(t, "--disruption-decision-engine", "banana"); err == nil {
		t.Fatal("expected an error for an invalid decision engine")
	}
}
