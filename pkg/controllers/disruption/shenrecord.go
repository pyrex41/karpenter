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

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/shencore/schema"
)

// recordDisrupt captures a disruption pass — the candidates and per-NodePool
// budgets that went in, and the commands the method decided on — as a golden
// corpus record. It runs only when SHENCORE_RECORD_DIR is set (a nil recorder
// no-ops) and never fails the disruption loop.
//
// The projection to schema view structs happens here, in the disruption package,
// because the recorder must not import the disruption types (it already imports
// the scheduler package, which the disruption package builds on). Extracting to
// primitive views keeps the schema/recorder packages free of that dependency.
func (c *Controller) recordDisrupt(ctx context.Context, method Method, candidates []*Candidate, budgets map[string]int, cmds []Command) {
	if !c.shenRecorder.Enabled() {
		return
	}
	input := schema.DisruptInputValue(c.clock.Now(), string(method.Reason()), candidateViews(candidates), budgets)
	output := schema.DisruptResultValue(commandViews(method, cmds))
	if err := c.shenRecorder.WriteRecord("disrupt", input, output); err != nil {
		log.FromContext(ctx).Error(err, "recording shencore disrupt corpus")
	}
}

// candidateViews projects disruption candidates into the schema's primitive view
// form. Shared by the recorder and the shadow-mode comparison so both serialize a
// pass's inputs identically.
func candidateViews(candidates []*Candidate) []schema.CandidateView {
	return lo.Map(candidates, func(cand *Candidate, _ int) schema.CandidateView {
		return schema.CandidateView{
			NodeName:       cand.Name(),
			NodePool:       nodePoolName(cand),
			InstanceType:   cand.Labels()[corev1.LabelInstanceTypeStable],
			Zone:           cand.zone,
			CapacityType:   cand.capacityType,
			Price:          cand.Price,
			DisruptionCost: cand.DisruptionCost,
			Empty:          cand.IsEmpty(),
			ReschedulablePodUIDs: lo.Map(cand.reschedulablePods, func(p *corev1.Pod, _ int) string {
				return string(p.UID)
			}),
		}
	})
}

// commandViews projects disruption commands into the schema's primitive view
// form. Shared by the recorder and the shadow-mode comparison so the Go decision
// serializes identically in both.
func commandViews(method Method, cmds []Command) []schema.CommandView {
	return lo.Map(cmds, func(cmd Command, _ int) schema.CommandView {
		return schema.CommandView{
			Decision:           string(cmd.Decision()),
			Reason:             string(method.Reason()),
			CandidateNodeNames: cmd.SourceNodeNames(),
			Replacements: lo.Map(cmd.Replacements, func(r *Replacement, _ int) schema.ReplacementView {
				return schema.ReplacementView{
					NodePool: r.NodePoolName,
					InstanceTypeNames: lo.Map(r.InstanceTypeOptions, func(it *cloudprovider.InstanceType, _ int) string {
						return it.Name
					}),
				}
			}),
			EstimatedSavings: cmd.EstimatedSavings(),
		}
	})
}

// nodePoolName returns a candidate's NodePool name, tolerating a nil NodePool.
func nodePoolName(cand *Candidate) string {
	if cand.NodePool == nil {
		return ""
	}
	return cand.NodePool.Name
}
