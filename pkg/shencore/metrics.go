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

package shencore

import (
	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"sigs.k8s.io/karpenter/pkg/metrics"
)

const (
	shencoreSubsystem = "shencore"

	// ShadowReasonLabel categorizes why a shadow comparison diverged (or errored),
	// kept low-cardinality so the counter stays cheap.
	ShadowReasonLabel = "reason"
	// ControllerLabel identifies which controller ran the shadow comparison.
	ControllerLabel = "controller"
)

// Shadow diff reasons. A comparison that matches emits nothing; every non-empty
// value below is a distinct way a Shen decision disagreed with the Go decision,
// or a way the Shen path failed without affecting the authoritative Go decision.
const (
	ShadowReasonCommandCount = "command_count"
	ShadowReasonDecisions    = "decisions"
	ShadowReasonCandidates   = "candidates"
	ShadowReasonReplacements = "replacements"
	ShadowReasonSavings      = "savings"
	ShadowReasonOther        = "other"
	ShadowReasonShenError    = "shen_error"
)

// ShadowDiffsTotal counts divergences between the authoritative Go decision and
// the Shen decision computed alongside it in shadow mode, plus contained Shen
// failures (reason=shen_error). A healthy shadow rollout drives this to zero.
var ShadowDiffsTotal = opmetrics.NewPrometheusCounter(
	crmetrics.Registry,
	prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Subsystem: shencoreSubsystem,
		Name:      "shadow_diffs_total",
		Help:      "Number of shadow-mode decisions where the Shen decision core diverged from the authoritative Go decision, or the Shen path errored. Labeled by controller and reason.",
	},
	[]string{ControllerLabel, ShadowReasonLabel},
)
