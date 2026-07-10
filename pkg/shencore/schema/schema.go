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

// Package schema converts Karpenter's live scheduling and disruption types into
// the deterministic s-expression contract consumed by the shencore decision
// engine. It is the SOLVE/DISRUPT data contract described in the rewrite plan:
// snapshot-in / decision-out, IDs only (no back-pointers), integer quantities
// (milli-CPU, whole bytes), requirements carrying their complement encoding, and
// lexicographic ordering everywhere a map or set is serialized.
//
// Byte-stability is the load-bearing property: the same logical input must always
// produce identical bytes, so that a Go decision and a Shen decision can be
// compared by byte-equality. Every map is emitted with sorted keys, every set
// with sorted members, and every non-deterministic placeholder (the scheduler's
// atomic-counter hostnames, wall-clock-derived values, floating point prices) is
// normalized to a stable representation here rather than left to the caller.
//
// The functions in this package are pure and deep-read: they never retain a
// pointer into live cluster state. Callers hand in values already detached from
// the informer cache (e.g. state.Cluster.DeepCopyNodes()).
package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// --- Core builders -----------------------------------------------------------
//
// The schema uses two lexical classes deliberately: fixed vocabulary (tags like
// `pod`, `req`, `In`) is emitted as Symbol; arbitrary payload text (label values,
// resource names, error strings) is emitted as String so the codec's escaping
// handles any byte content. Keeping user data out of the Symbol space also avoids
// the vbar-quoting churn the encoder applies to lowercase symbols.

func sym(s string) sexpr.Value { return sexpr.Symbol(s) }
func str(s string) sexpr.Value { return sexpr.String(s) }
func intv(i int64) sexpr.Value { return sexpr.Int(i) }

// boolv encodes a Go bool as the Shen booleans true/false, matching objconv.
func boolv(b bool) sexpr.Value {
	if b {
		return sexpr.Symbol("true")
	}
	return sexpr.Symbol("false")
}

// list builds a proper list from the given values.
func list(vs ...sexpr.Value) sexpr.Value { return sexpr.List(vs) }

// tagged builds (tag v...) — the standard "keyword-headed form" used throughout
// the schema so every sub-tree is self-describing.
func tagged(tag string, vs ...sexpr.Value) sexpr.Value {
	return sexpr.List(append([]sexpr.Value{sexpr.Symbol(tag)}, vs...))
}

// optInt emits (tag n) when set, or (tag) when nil, so an absent optional integer
// is distinguishable from a zero value.
func optInt(tag string, p *int) sexpr.Value {
	if p == nil {
		return tagged(tag)
	}
	return tagged(tag, intv(int64(*p)))
}

// optStr emits (tag "s") when non-empty, or (tag) when empty.
func optStr(tag string, s string) sexpr.Value {
	if s == "" {
		return tagged(tag)
	}
	return tagged(tag, str(s))
}

// optInt64 emits (tag n) when set, or (tag) when nil.
func optInt64(tag string, p *int64) sexpr.Value {
	if p == nil {
		return tagged(tag)
	}
	return tagged(tag, intv(*p))
}

// optNodeInclusionPolicy renders an optional TopologySpreadConstraint node
// inclusion policy as a symbol, or the empty list when unset.
func optNodeInclusionPolicy(p *corev1.NodeInclusionPolicy) sexpr.Value {
	if p == nil {
		return list()
	}
	return sym(string(*p))
}

// sortForms sorts a slice of already-built values by their canonical encoding, so
// order-insensitive collections (affinity terms, offerings) serialize stably
// regardless of the order the live objects presented them in.
func sortForms(forms []sexpr.Value) {
	sort.Slice(forms, func(i, j int) bool {
		bi, _ := sexpr.Encode(forms[i])
		bj, _ := sexpr.Encode(forms[j])
		return string(bi) < string(bj)
	})
}

// --- Resource normalization --------------------------------------------------

// normalizeQuantity reduces a resource.Quantity to a single lossless integer.
// CPU is expressed in milli-units (so fractional cores survive); every other
// resource is expressed in its whole base unit (bytes for memory/storage, a
// count for pods/devices). This mirrors the plan's "milli-CPU, bytes" rule and
// keeps the value free of the float formatting that would break byte-stability.
func normalizeQuantity(name corev1.ResourceName, q resource.Quantity) int64 {
	if name == corev1.ResourceCPU {
		return q.MilliValue()
	}
	return q.Value()
}

// resourceList emits (resources (<name> <int>)...) with entries sorted by name.
// A nil or empty list still emits (resources) so the field is always present.
func resourceList(tag string, rl corev1.ResourceList) sexpr.Value {
	names := make([]string, 0, len(rl))
	for name := range rl {
		names = append(names, string(name))
	}
	sort.Strings(names)
	entries := make([]sexpr.Value, 0, len(names)+1)
	entries = append(entries, sym(tag))
	for _, name := range names {
		q := rl[corev1.ResourceName(name)]
		entries = append(entries, list(str(name), intv(normalizeQuantity(corev1.ResourceName(name), q))))
	}
	return sexpr.List(entries)
}

// --- Labels & selectors ------------------------------------------------------

// labels emits (labels (<key> <value>)...) sorted by key.
func labels(m map[string]string) sexpr.Value {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]sexpr.Value, 0, len(keys)+1)
	entries = append(entries, sym("labels"))
	for _, k := range keys {
		entries = append(entries, list(str(k), str(m[k])))
	}
	return sexpr.List(entries)
}

// taints emits (<tag> (taint <key> <value> <effect>)...) sorted by (key,value,effect).
func taints(tag string, ts []corev1.Taint) sexpr.Value {
	sorted := append([]corev1.Taint(nil), ts...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Key != sorted[j].Key {
			return sorted[i].Key < sorted[j].Key
		}
		if sorted[i].Value != sorted[j].Value {
			return sorted[i].Value < sorted[j].Value
		}
		return sorted[i].Effect < sorted[j].Effect
	})
	entries := make([]sexpr.Value, 0, len(sorted)+1)
	entries = append(entries, sym(tag))
	for _, t := range sorted {
		entries = append(entries, tagged("taint", str(t.Key), str(t.Value), str(string(t.Effect))))
	}
	return sexpr.List(entries)
}

// labelSelector emits a deterministic (selector (match-labels ...) (match-expressions ...)).
// A nil selector emits (selector) so presence is unambiguous.
func labelSelector(sel *metav1.LabelSelector) sexpr.Value {
	if sel == nil {
		return tagged("selector")
	}
	// match-labels sorted by key
	mlKeys := make([]string, 0, len(sel.MatchLabels))
	for k := range sel.MatchLabels {
		mlKeys = append(mlKeys, k)
	}
	sort.Strings(mlKeys)
	ml := make([]sexpr.Value, 0, len(mlKeys)+1)
	ml = append(ml, sym("match-labels"))
	for _, k := range mlKeys {
		ml = append(ml, list(str(k), str(sel.MatchLabels[k])))
	}
	// match-expressions sorted by (key, op)
	exprs := append([]metav1.LabelSelectorRequirement(nil), sel.MatchExpressions...)
	sort.Slice(exprs, func(i, j int) bool {
		if exprs[i].Key != exprs[j].Key {
			return exprs[i].Key < exprs[j].Key
		}
		return exprs[i].Operator < exprs[j].Operator
	})
	me := make([]sexpr.Value, 0, len(exprs)+1)
	me = append(me, sym("match-expressions"))
	for _, e := range exprs {
		vals := append([]string(nil), e.Values...)
		sort.Strings(vals)
		me = append(me, tagged("expr", str(e.Key), sym(string(e.Operator)), stringList(vals)))
	}
	return tagged("selector", sexpr.List(ml), sexpr.List(me))
}

// stringList emits a plain list of strings in the given (already-sorted) order.
func stringList(ss []string) sexpr.Value {
	vs := make([]sexpr.Value, len(ss))
	for i, s := range ss {
		vs[i] = str(s)
	}
	return sexpr.List(vs)
}

// sortedStringList copies, sorts, and emits a list of strings.
func sortedStringList(ss []string) sexpr.Value {
	c := append([]string(nil), ss...)
	sort.Strings(c)
	return stringList(c)
}

// --- Hashing (content addressing) -------------------------------------------

// hashValue returns the hex-encoded sha256 of the canonical encoding of v. It is
// used to content-address the instance-type catalog and to name record files by
// their input. Encoding is deterministic, so the hash is a stable identity for
// the logical value. Panics only if v contains a byte outside the codec alphabet,
// which the schema never produces.
func hashValue(v sexpr.Value) string {
	b, err := sexpr.Encode(v)
	if err != nil {
		// A schema-produced value is always in-alphabet; a failure here is a
		// programming error in a converter, not runtime data.
		panic("shencore/schema: encoding value for hashing: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
