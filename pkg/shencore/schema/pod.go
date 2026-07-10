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

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// Pod converts a single pod into its snapshot form. The pod is identified by UID
// only; no pointer back to the live object is retained. Node scheduling
// constraints (nodeSelector, required node affinity, the pod's own well-known
// labels) are folded through scheduling.NewPodRequirements — the exact requirement
// set the scheduler bin-packs against — so the recorded requirements are the
// scheduler's view, not a re-derivation.
func Pod(p *corev1.Pod) sexpr.Value {
	return tagged("pod",
		tagged("id", str(string(p.UID))),
		tagged("namespace", str(p.Namespace)),
		tagged("name", str(p.Name)),
		optStr("node-name", p.Spec.NodeName),
		tagged("phase", str(string(p.Status.Phase))),
		resourceList("requests", resources.RequestsForPods(p)),
		requirements("reqs", scheduling.NewPodRequirements(p)),
		podTolerations(p),
		podTopologySpread(p),
		podHostPorts(p),
		podVolumes(p),
		podAffinity(p),
	)
}

// Pods converts a slice of pods, sorted by UID for byte-stability. The scheduler
// receives pods in a batch whose order is not a decision input, so we impose a
// stable order here.
func Pods(tag string, pods []*corev1.Pod) sexpr.Value {
	sorted := append([]*corev1.Pod(nil), pods...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].UID < sorted[j].UID })
	entries := make([]sexpr.Value, 0, len(sorted)+1)
	entries = append(entries, sym(tag))
	for _, p := range sorted {
		entries = append(entries, Pod(p))
	}
	return sexpr.List(entries)
}

// podUIDs emits (<tag> "<uid>"...) with UIDs sorted. Used wherever a decision
// references a set of pods by identity only.
func podUIDs(tag string, pods []*corev1.Pod) sexpr.Value {
	uids := make([]string, len(pods))
	for i, p := range pods {
		uids[i] = string(p.UID)
	}
	sort.Strings(uids)
	entries := make([]sexpr.Value, 0, len(uids)+1)
	entries = append(entries, sym(tag))
	for _, u := range uids {
		entries = append(entries, str(u))
	}
	return sexpr.List(entries)
}

func podTolerations(p *corev1.Pod) sexpr.Value {
	tols := append([]corev1.Toleration(nil), p.Spec.Tolerations...)
	sort.Slice(tols, func(i, j int) bool {
		if tols[i].Key != tols[j].Key {
			return tols[i].Key < tols[j].Key
		}
		if tols[i].Operator != tols[j].Operator {
			return tols[i].Operator < tols[j].Operator
		}
		if tols[i].Value != tols[j].Value {
			return tols[i].Value < tols[j].Value
		}
		return tols[i].Effect < tols[j].Effect
	})
	entries := make([]sexpr.Value, 0, len(tols)+1)
	entries = append(entries, sym("tolerations"))
	for _, t := range tols {
		entries = append(entries, tagged("tol",
			str(t.Key),
			sym(string(t.Operator)),
			str(t.Value),
			str(string(t.Effect)),
			optInt64("seconds", t.TolerationSeconds),
		))
	}
	return sexpr.List(entries)
}

func podTopologySpread(p *corev1.Pod) sexpr.Value {
	tscs := append([]corev1.TopologySpreadConstraint(nil), p.Spec.TopologySpreadConstraints...)
	sort.Slice(tscs, func(i, j int) bool {
		if tscs[i].TopologyKey != tscs[j].TopologyKey {
			return tscs[i].TopologyKey < tscs[j].TopologyKey
		}
		return tscs[i].WhenUnsatisfiable < tscs[j].WhenUnsatisfiable
	})
	entries := make([]sexpr.Value, 0, len(tscs)+1)
	entries = append(entries, sym("topology-spread"))
	for _, t := range tscs {
		var minDomains *int
		if t.MinDomains != nil {
			md := int(*t.MinDomains)
			minDomains = &md
		}
		entries = append(entries, tagged("tsc",
			tagged("topology-key", str(t.TopologyKey)),
			tagged("max-skew", intv(int64(t.MaxSkew))),
			optInt("min-domains", minDomains),
			tagged("when-unsatisfiable", str(string(t.WhenUnsatisfiable))),
			tagged("node-affinity-policy", optNodeInclusionPolicy(t.NodeAffinityPolicy)),
			tagged("node-taints-policy", optNodeInclusionPolicy(t.NodeTaintsPolicy)),
			labelSelector(t.LabelSelector),
			sortedStringList(t.MatchLabelKeys),
		))
	}
	return sexpr.List(entries)
}

func podHostPorts(p *corev1.Pod) sexpr.Value {
	ports := scheduling.GetHostPorts(p)
	type hp struct {
		ip    string
		port  int32
		proto string
	}
	rows := make([]hp, len(ports))
	for i, h := range ports {
		rows[i] = hp{ip: h.IP.String(), port: h.Port, proto: string(h.Protocol)}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].port != rows[j].port {
			return rows[i].port < rows[j].port
		}
		if rows[i].ip != rows[j].ip {
			return rows[i].ip < rows[j].ip
		}
		return rows[i].proto < rows[j].proto
	})
	entries := make([]sexpr.Value, 0, len(rows)+1)
	entries = append(entries, sym("host-ports"))
	for _, r := range rows {
		entries = append(entries, tagged("hp", str(r.ip), intv(int64(r.port)), str(r.proto)))
	}
	return sexpr.List(entries)
}

func podVolumes(p *corev1.Pod) sexpr.Value {
	names := make([]string, 0, len(p.Spec.Volumes))
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			names = append(names, v.PersistentVolumeClaim.ClaimName)
		} else if v.Ephemeral != nil {
			// Ephemeral volumes bind to a PVC named <pod>-<volume>.
			names = append(names, p.Name+"-"+v.Name)
		}
	}
	sort.Strings(names)
	entries := make([]sexpr.Value, 0, len(names)+1)
	entries = append(entries, sym("volumes"))
	for _, n := range names {
		entries = append(entries, tagged("pvc", str(n)))
	}
	return sexpr.List(entries)
}

// podAffinity emits the pod's inter-pod affinity and anti-affinity terms. Both
// required and preferred terms are captured (preferred carry a weight); the
// scheduler's preference relaxation consumes the preferred terms, so they are part
// of the decision surface.
func podAffinity(p *corev1.Pod) sexpr.Value {
	affinity := p.Spec.Affinity
	var podAff, podAnti *corev1.PodAffinity
	var podAntiAff *corev1.PodAntiAffinity
	if affinity != nil {
		podAff = affinity.PodAffinity
		podAntiAff = affinity.PodAntiAffinity
	}
	_ = podAnti
	return tagged("affinity",
		affinityTerms("pod-affinity", requiredOf(podAff), preferredOf(podAff)),
		affinityTermsAnti("pod-anti-affinity", podAntiAff),
	)
}

func requiredOf(a *corev1.PodAffinity) []corev1.PodAffinityTerm {
	if a == nil {
		return nil
	}
	return a.RequiredDuringSchedulingIgnoredDuringExecution
}

func preferredOf(a *corev1.PodAffinity) []corev1.WeightedPodAffinityTerm {
	if a == nil {
		return nil
	}
	return a.PreferredDuringSchedulingIgnoredDuringExecution
}

func affinityTermsAnti(tag string, a *corev1.PodAntiAffinity) sexpr.Value {
	if a == nil {
		return affinityTerms(tag, nil, nil)
	}
	return affinityTerms(tag, a.RequiredDuringSchedulingIgnoredDuringExecution, a.PreferredDuringSchedulingIgnoredDuringExecution)
}

func affinityTerms(tag string, required []corev1.PodAffinityTerm, preferred []corev1.WeightedPodAffinityTerm) sexpr.Value {
	reqForms := make([]sexpr.Value, len(required))
	for i, t := range required {
		reqForms[i] = affinityTerm(t)
	}
	sortForms(reqForms)
	prefForms := make([]sexpr.Value, len(preferred))
	for i, w := range preferred {
		prefForms[i] = tagged("weighted", intv(int64(w.Weight)), affinityTerm(w.PodAffinityTerm))
	}
	sortForms(prefForms)
	return tagged(tag,
		sexpr.List(append([]sexpr.Value{sym("required")}, reqForms...)),
		sexpr.List(append([]sexpr.Value{sym("preferred")}, prefForms...)),
	)
}

func affinityTerm(t corev1.PodAffinityTerm) sexpr.Value {
	ns := append([]string(nil), t.Namespaces...)
	sort.Strings(ns)
	return tagged("term",
		tagged("topology-key", str(t.TopologyKey)),
		labelSelector(t.LabelSelector),
		tagged("namespaces", stringList(ns)),
		tagged("namespace-selector", labelSelector(t.NamespaceSelector)),
	)
}
