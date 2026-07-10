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

package disruption_test

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"sigs.k8s.io/karpenter/pkg/controllers/provisioning/scheduling"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	disruptionevents "sigs.k8s.io/karpenter/pkg/controllers/disruption/events"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var (
	nodeClaim1, nodeClaim2 *v1.NodeClaim
	nodePool               *v1.NodePool
	node1, node2           *corev1.Node
)

var _ = Describe("Queue", func() {
	BeforeEach(func() {
		nodePool = test.NodePool()
		nodeClaim1, node1 = test.NodeClaimAndNode(
			v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: cloudProvider.InstanceTypes[0].Name,
						v1.CapacityTypeLabelKey:        cloudProvider.InstanceTypes[0].Offerings.Cheapest().Requirements.Get(v1.CapacityTypeLabelKey).Any(),
						corev1.LabelTopologyZone:       cloudProvider.InstanceTypes[0].Offerings.Cheapest().Requirements.Get(corev1.LabelTopologyZone).Any(),
					},
				},
				Status: v1.NodeClaimStatus{
					ProviderID:  test.RandomProviderID(),
					Allocatable: map[corev1.ResourceName]resource.Quantity{corev1.ResourceCPU: resource.MustParse("32")},
				},
			},
		)
		nodeClaim2, node2 = test.NodeClaimAndNode(
			v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: cloudProvider.InstanceTypes[0].Name,
						v1.CapacityTypeLabelKey:        cloudProvider.InstanceTypes[0].Offerings.Cheapest().Requirements.Get(v1.CapacityTypeLabelKey).Any(),
						corev1.LabelTopologyZone:       cloudProvider.InstanceTypes[0].Offerings.Cheapest().Requirements.Get(corev1.LabelTopologyZone).Any(),
					},
				},
				Status: v1.NodeClaimStatus{
					ProviderID:  test.RandomProviderID(),
					Allocatable: map[corev1.ResourceName]resource.Quantity{corev1.ResourceCPU: resource.MustParse("32")},
				},
			},
		)
		node1.Spec.Taints = append(node1.Spec.Taints, v1.DisruptedNoScheduleTaint)
		node2.Spec.Taints = append(node2.Spec.Taints, v1.DisruptedNoScheduleTaint)
	})
	Context("Reconcile", func() {
		It("should keep nodes tainted when replacements haven't finished initialization", func() {
			ExpectApplied(ctx, env.Client, nodeClaim1, node1, nodePool)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})

			nct := scheduling.NewNodeClaimTemplate(nodePool)
			nct.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements := []*disruption.Replacement{
				{
					NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct},
				},
			}

			stateNode := ExpectStateNodeExists(cluster, node1)
			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      replacements,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())

			node1 = ExpectNodeExists(ctx, env.Client, node1.Name)
			Expect(node1.Spec.Taints).To(ContainElement(v1.DisruptedNoScheduleTaint))

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)

			// Update state
			ExpectReconcileSucceeded(ctx, nodeStateController, client.ObjectKeyFromObject(node1))
			Expect(ExpectNodeClaims(ctx, env.Client)).To(HaveLen(2))
			node1 = ExpectNodeExists(ctx, env.Client, node1.Name)
			Expect(node1.Spec.Taints).To(ContainElement(v1.DisruptedNoScheduleTaint))
		})
		It("should not return an error when handling commands before the timeout", func() {
			ExpectApplied(ctx, env.Client, nodeClaim1, node1, nodePool)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})
			stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)

			nct := scheduling.NewNodeClaimTemplate(nodePool)
			nct.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements := []*disruption.Replacement{
				{
					NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct},
				},
			}

			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      replacements,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())
			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			Expect(queue.HasAny(stateNode.ProviderID())).To(BeTrue()) // Expect the command to still be in the queue
		})
		It("should not return an error when the NodeClaim doesn't exist but the NodeCliam is in cluster state", func() {
			ExpectApplied(ctx, env.Client, nodeClaim1, node1, nodePool)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})
			stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)

			nct := scheduling.NewNodeClaimTemplate(nodePool)
			nct.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements := []*disruption.Replacement{
				{
					NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct},
				},
			}

			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      replacements,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())

			replacementNodeClaim := &v1.NodeClaim{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: cmd.Replacements[0].Name}, replacementNodeClaim))
			replacementNodeClaim, _ = ExpectNodeClaimDeployedAndStateUpdated(ctx, env.Client, cluster, cloudProvider, replacementNodeClaim)

			cluster.UpdateNodeClaim(replacementNodeClaim)
			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			Expect(queue.HasAny(stateNode.ProviderID())).To(BeTrue()) // Expect the command to still be in the queue
		})
		It("should untaint nodes when a command times out", func() {
			ExpectApplied(ctx, env.Client, nodeClaim1, node1, nodePool)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})
			stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)

			nct := scheduling.NewNodeClaimTemplate(nodePool)
			nct.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements := []*disruption.Replacement{
				{
					NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct},
				},
			}

			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      replacements,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())

			// Step the clock to trigger the timeout.
			env.Clock.Step(11 * time.Minute)

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			node1 = ExpectNodeExists(ctx, env.Client, node1.Name)
			Expect(node1.Spec.Taints).ToNot(ContainElement(v1.DisruptedNoScheduleTaint))
		})
		It("should fully handle a command when replacements are initialized", func() {
			ExpectApplied(ctx, env.Client, nodeClaim1, node1, nodePool)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})
			stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)

			nct := scheduling.NewNodeClaimTemplate(nodePool)
			nct.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements := []*disruption.Replacement{
				{
					NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct},
				},
			}

			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      replacements,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())

			replacementNodeClaim := &v1.NodeClaim{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: cmd.Replacements[0].Name}, replacementNodeClaim))
			replacementNodeClaim, replacementNode := ExpectNodeClaimDeployedAndStateUpdated(ctx, env.Client, cluster, cloudProvider, replacementNodeClaim)

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			// Get the command
			Expect(cmd.Replacements[0].Initialized).To(BeFalse())

			Expect(recorder.DetectedEvent(disruptionevents.Launching(replacementNodeClaim, string(cmd.Reason())).Message)).To(BeTrue())
			Expect(recorder.DetectedEvent(disruptionevents.WaitingOnReadiness(replacementNodeClaim).Message)).To(BeTrue())

			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController,
				[]*corev1.Node{replacementNode}, []*v1.NodeClaim{replacementNodeClaim})

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeTrue())

			terminatingEvents := disruptionevents.Terminating(node1, nodeClaim1, string(cmd.Reason()))
			Expect(recorder.DetectedEvent(terminatingEvents[0].Message)).To(BeTrue())
			Expect(recorder.DetectedEvent(terminatingEvents[1].Message)).To(BeTrue())

			ExpectNodeClaimsCascadeDeletion(ctx, env.Client, nodeClaim1)
			// And expect the nodeClaim and node to be deleted
			ExpectNotFound(ctx, env.Client, nodeClaim1, node1)
		})
		It("should only finish a command when all replacements are initialized", func() {
			ExpectApplied(ctx, env.Client, nodePool, nodeClaim1, node1)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})
			stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)

			nct := scheduling.NewNodeClaimTemplate(nodePool)
			nct.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			nct2 := scheduling.NewNodeClaimTemplate(nodePool)
			nct2.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements := []*disruption.Replacement{
				{
					NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct},
				},
				{
					NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct2},
				},
			}

			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      replacements,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())

			replacementNodeClaim1 := &v1.NodeClaim{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: cmd.Replacements[0].Name}, replacementNodeClaim1))
			replacementNodeClaim1, replacementNode1 := ExpectNodeClaimDeployedAndStateUpdated(ctx, env.Client, cluster, cloudProvider, replacementNodeClaim1)
			replacementNodeClaim2 := &v1.NodeClaim{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: cmd.Replacements[1].Name}, replacementNodeClaim2))
			replacementNodeClaim2, replacementNode2 := ExpectNodeClaimDeployedAndStateUpdated(ctx, env.Client, cluster, cloudProvider, replacementNodeClaim2)

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeFalse())
			Expect(recorder.DetectedEvent(disruptionevents.WaitingOnReadiness(nodeClaim1).Message)).To(BeTrue())
			Expect(cmd.Replacements[1].Initialized).To(BeFalse())

			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{replacementNode1}, []*v1.NodeClaim{replacementNodeClaim1})

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeTrue())
			Expect(cmd.Replacements[1].Initialized).To(BeFalse())
			Expect(recorder.DetectedEvent(disruptionevents.WaitingOnReadiness(nodeClaim1).Message)).To(BeTrue())

			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{replacementNode2}, []*v1.NodeClaim{replacementNodeClaim2})

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeTrue())
			Expect(cmd.Replacements[1].Initialized).To(BeTrue())

			ExpectNodeClaimsCascadeDeletion(ctx, env.Client, nodeClaim1)
			// And expect the nodeClaim and node to be deleted
			ExpectNotFound(ctx, env.Client, nodeClaim1, node1)
		})
		It("should not wait for replacements when none are needed", func() {
			ExpectApplied(ctx, env.Client, nodeClaim1, node1, nodePool)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})
			stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)

			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      nil,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())

			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)

			terminatingEvents := disruptionevents.Terminating(node1, nodeClaim1, string(cmd.Reason()))
			Expect(recorder.DetectedEvent(terminatingEvents[0].Message)).To(BeTrue())
			Expect(recorder.DetectedEvent(terminatingEvents[1].Message)).To(BeTrue())

			ExpectNodeClaimsCascadeDeletion(ctx, env.Client, nodeClaim1)
			// And expect the nodeClaim and node to be deleted
			ExpectNotFound(ctx, env.Client, nodeClaim1, node1)
		})
		It("should finish two commands in order as replacements are initialized", func() {
			ExpectApplied(ctx, env.Client, nodePool, nodeClaim1, node1, nodeClaim2, node2)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1, node2}, []*v1.NodeClaim{nodeClaim1, nodeClaim2})
			stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)
			stateNode2 := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim2)

			nct := scheduling.NewNodeClaimTemplate(nodePool)
			nct.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements := []*disruption.Replacement{{
				NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct},
			}}
			nct2 := scheduling.NewNodeClaimTemplate(nodePool)
			nct2.InstanceTypeOptions = append([]*cloudprovider.InstanceType{}, cloudProvider.InstanceTypes...)
			replacements2 := []*disruption.Replacement{{
				NodeClaim: &scheduling.NodeClaim{NodeClaimTemplate: *nct2},
			}}

			cmd := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				Replacements:      replacements,
			}
			Expect(queue.StartCommand(ctx, cmd)).To(BeNil())
			cmd2 := &disruption.Command{
				Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
				CreationTimestamp: env.Clock.Now(),
				ID:                uuid.New(),
				Results:           scheduling.Results{},
				Candidates:        []*disruption.Candidate{{StateNode: stateNode2, NodePool: nodePool}},
				Replacements:      replacements2,
			}
			Expect(queue.StartCommand(ctx, cmd2)).To(BeNil())

			replacementNodeClaim1 := &v1.NodeClaim{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: cmd.Replacements[0].Name}, replacementNodeClaim1))
			replacementNodeClaim2 := &v1.NodeClaim{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: cmd2.Replacements[0].Name}, replacementNodeClaim2))

			replacementNodeClaim1, replacementNode1 := ExpectNodeClaimDeployedAndStateUpdated(ctx, env.Client, cluster, cloudProvider, replacementNodeClaim1)
			replacementNodeClaim2, replacementNode2 := ExpectNodeClaimDeployedAndStateUpdated(ctx, env.Client, cluster, cloudProvider, replacementNodeClaim2)

			// Reconcile the first command and expect nothing to be initialized
			ExpectObjectReconciled(ctx, env.Client, queue, stateNode.NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeFalse())
			Expect(recorder.DetectedEvent(disruptionevents.WaitingOnReadiness(nodeClaim1).Message)).To(BeTrue())
			Expect(cmd2.Replacements[0].Initialized).To(BeFalse())
			Expect(recorder.DetectedEvent(disruptionevents.WaitingOnReadiness(nodeClaim2).Message)).To(BeTrue())

			// Make the first command's node initialized
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{replacementNode1}, []*v1.NodeClaim{replacementNodeClaim1})
			// Reconcile the second command and expect nothing to be initialized
			ExpectObjectReconciled(ctx, env.Client, queue, cmd2.Candidates[0].NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeFalse())
			Expect(recorder.DetectedEvent(disruptionevents.WaitingOnReadiness(nodeClaim1).Message)).To(BeTrue())
			Expect(cmd2.Replacements[0].Initialized).To(BeFalse())
			Expect(recorder.DetectedEvent(disruptionevents.WaitingOnReadiness(nodeClaim2).Message)).To(BeTrue())

			// Reconcile the first command and expect the replacement to be initialized
			ExpectObjectReconciled(ctx, env.Client, queue, cmd.Candidates[0].NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeTrue())
			Expect(cmd2.Replacements[0].Initialized).To(BeFalse())

			ExpectNodeClaimsCascadeDeletion(ctx, env.Client, nodeClaim1)
			ExpectNotFound(ctx, env.Client, nodeClaim1, node1)

			// Make the second command's node initialized
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{replacementNode2}, []*v1.NodeClaim{replacementNodeClaim2})

			// Reconcile the second command and expect the replacement to be initialized
			ExpectObjectReconciled(ctx, env.Client, queue, cmd2.Candidates[0].NodeClaim)
			Expect(cmd.Replacements[0].Initialized).To(BeTrue())
			Expect(cmd2.Replacements[0].Initialized).To(BeTrue())

			ExpectNodeClaimsCascadeDeletion(ctx, env.Client, nodeClaim2)
			// And expect the nodeClaim and node to be deleted
			ExpectNotFound(ctx, env.Client, nodeClaim2, node2)
		})
		Context("StartCommand", func() {
			It("should log and proceed with the marked candidates when marking fails for a subset of a delete-only command", func() {
				ExpectApplied(ctx, env.Client, nodePool, nodeClaim1, node1, nodeClaim2, node2)
				ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1, node2}, []*v1.NodeClaim{nodeClaim1, nodeClaim2})
				stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)
				stateNode2 := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim2)

				// Fail the status patch that adds the Disrupted condition to the second candidate so that
				// only the first candidate is successfully marked
				failingQueue := disruption.NewQueue(&failingStatusClient{Client: env.Client, failedNames: sets.New(nodeClaim2.Name)}, recorder, cluster, env.Clock, prov)
				var logs []string
				logCtx := log.IntoContext(ctx, funcr.New(func(_, args string) { logs = append(logs, args) }, funcr.Options{}))

				cmd := &disruption.Command{
					Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
					CreationTimestamp: env.Clock.Now(),
					ID:                uuid.New(),
					Results:           scheduling.Results{},
					Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}, {StateNode: stateNode2, NodePool: nodePool}},
					Replacements:      nil,
				}
				Expect(failingQueue.StartCommand(logCtx, cmd)).To(BeNil())

				// The command should only proceed with the successfully marked candidate
				Expect(cmd.Candidates).To(HaveLen(1))
				Expect(cmd.Candidates[0].ProviderID()).To(Equal(stateNode.ProviderID()))
				Expect(failingQueue.HasAny(stateNode.ProviderID())).To(BeTrue())
				Expect(failingQueue.HasAny(stateNode2.ProviderID())).To(BeFalse())
				// And the partial failure should be surfaced in the logs
				Expect(logs).To(ContainElement(ContainSubstring("failed marking candidates as disrupted")))
			})
			It("should return an error when marking fails for all candidates of a delete-only command", func() {
				ExpectApplied(ctx, env.Client, nodePool, nodeClaim1, node1, nodeClaim2, node2)
				ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1, node2}, []*v1.NodeClaim{nodeClaim1, nodeClaim2})
				stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)
				stateNode2 := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim2)

				// Fail the status patch that adds the Disrupted condition to both candidates so that no candidate is successfully marked
				failingQueue := disruption.NewQueue(&failingStatusClient{Client: env.Client, failedNames: sets.New(nodeClaim1.Name, nodeClaim2.Name)}, recorder, cluster, env.Clock, prov)

				cmd := &disruption.Command{
					Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
					CreationTimestamp: env.Clock.Now(),
					ID:                uuid.New(),
					Results:           scheduling.Results{},
					Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}, {StateNode: stateNode2, NodePool: nodePool}},
					Replacements:      nil,
				}
				Expect(failingQueue.StartCommand(ctx, cmd)).ToNot(Succeed())
				Expect(failingQueue.HasAny(stateNode.ProviderID(), stateNode2.ProviderID())).To(BeFalse())
			})
		})
		Context("MarkDisrupted", func() {
			It("should not clobber status conditions written concurrently by other controllers", func() {
				ExpectApplied(ctx, env.Client, nodeClaim1, node1, nodePool)
				ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, []*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})
				stateNode := ExpectStateNodeExistsForNodeClaim(cluster, nodeClaim1)

				// Use a client that writes a status condition to the NodeClaim between the queue's Get and
				// Status().Patch calls to simulate another controller updating the status conditions concurrently
				racingClient := newConditionRacingClient(env.Client)
				q := disruption.NewQueue(racingClient, recorder, cluster, env.Clock, prov)
				cmd := &disruption.Command{
					Method:            disruption.NewDrift(env.Client, cluster, prov, recorder, env.Clock),
					CreationTimestamp: env.Clock.Now(),
					ID:                uuid.New(),
					Results:           scheduling.Results{},
					Candidates:        []*disruption.Candidate{{StateNode: stateNode, NodePool: nodePool}},
				}
				Expect(q.StartCommand(ctx, cmd)).To(BeNil())

				nodeClaim1 = ExpectExists(ctx, env.Client, nodeClaim1)
				Expect(nodeClaim1.StatusConditions().Get(v1.ConditionTypeDisruptionReason).IsTrue()).To(BeTrue())
				// The condition written by the other controller should not have been clobbered by the queue's patch
				Expect(nodeClaim1.StatusConditions().Get(v1.ConditionTypeConsolidatable).IsTrue()).To(BeTrue())
			})
		})
		Context("CalculateRetryDuration", func() {
			DescribeTable("should calculate correct timeout based on queue length",
				func(numCommands int, expectedDuration time.Duration) {
					q := disruption.NewQueue(env.Client, recorder, cluster, env.Clock, prov)
					q.Lock()
					for i := range numCommands {
						q.ProviderIDToCommand[strconv.Itoa(i)] = &disruption.Command{}
					}
					q.Unlock()
					actualDuration := q.GetMaxRetryDuration()
					Expect(actualDuration).To(Equal(expectedDuration))
				},
				Entry("very small queue - 100 commands", 100, 10*time.Minute),                  // max(100*80ms, 10min) = 10min
				Entry("small queue - 4000 commands", 4000, 10*time.Minute),                     // max(4000*80ms, 10min) = 10min
				Entry("medium queue - 10000 commands", 10000, 13*time.Minute+20*time.Second),   // 10000*80ms = 13min 20sec
				Entry("large queue - 40000 commands", 40000, 53*time.Minute+20*time.Second),    // 40000*80ms = 53min 20sec
				Entry("very large queue - 80000 commands (capped)", 80000, 1*time.Hour),        // min(80000*80ms, 1hr) = 1hr
				Entry("extremely large queue - 100000 commands (capped)", 100000, 1*time.Hour), // min(100000*80ms, 1hr) = 1hr
			)
		})
	})
})

// failingStatusClient wraps a client.Client and fails status sub-resource patches for objects with the given
// names to simulate partial failures when marking candidates as disrupted
type failingStatusClient struct {
	client.Client
	failedNames sets.Set[string]
}

func (c *failingStatusClient) Status() client.SubResourceWriter {
	return &failingStatusWriter{SubResourceWriter: c.Client.Status(), failedNames: c.failedNames}
}

type failingStatusWriter struct {
	client.SubResourceWriter
	failedNames sets.Set[string]
}

func (w *failingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if w.failedNames.Has(obj.GetName()) {
		return fmt.Errorf("induced status patch failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

// conditionRacingClient simulates another controller writing a status condition to the NodeClaim
// between a Get and a Status().Patch call, racing with the patch that's about to be applied
type conditionRacingClient struct {
	client.Client
	injected atomic.Bool
}

func newConditionRacingClient(c client.Client) *conditionRacingClient {
	return &conditionRacingClient{Client: c}
}

func (c *conditionRacingClient) Status() client.SubResourceWriter {
	return &conditionRacingStatusWriter{SubResourceWriter: c.Client.Status(), kubeClient: c.Client, injected: &c.injected}
}

type conditionRacingStatusWriter struct {
	client.SubResourceWriter
	kubeClient client.Client
	injected   *atomic.Bool
}

func (s *conditionRacingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if nc, ok := obj.(*v1.NodeClaim); ok && s.injected.CompareAndSwap(false, true) {
		fresh := &v1.NodeClaim{}
		if err := s.kubeClient.Get(ctx, client.ObjectKeyFromObject(nc), fresh); err != nil {
			return err
		}
		stored := fresh.DeepCopy()
		fresh.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)
		if err := s.kubeClient.Status().Patch(ctx, fresh, client.MergeFrom(stored)); err != nil {
			return err
		}
	}
	return s.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}
