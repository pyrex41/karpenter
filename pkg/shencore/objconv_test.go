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
	"reflect"
	"testing"

	"github.com/tiancaiamao/shen-go/kl"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

func roundTrip(t *testing.T, v sexpr.Value) {
	t.Helper()
	obj, err := toObj(v)
	if err != nil {
		t.Fatalf("toObj(%#v): %v", v, err)
	}
	got, err := fromObj(obj)
	if err != nil {
		t.Fatalf("fromObj: %v", err)
	}
	if !reflect.DeepEqual(v, got) {
		t.Fatalf("round-trip mismatch\n  want: %#v\n  got:  %#v", v, got)
	}
}

func TestObjConvScalars(t *testing.T) {
	roundTrip(t, sexpr.Int(0))
	roundTrip(t, sexpr.Int(-2500000))
	roundTrip(t, sexpr.Int(1<<40)) // beyond the fixnum range, forces boxed number
	roundTrip(t, sexpr.String(""))
	roundTrip(t, sexpr.String("zone-a"))
	roundTrip(t, sexpr.Symbol("solve-result"))
	roundTrip(t, sexpr.List{})
}

func TestObjConvNestedStructure(t *testing.T) {
	roundTrip(t, sexpr.List{
		sexpr.Symbol("solve"),
		sexpr.List{sexpr.Symbol("clock"), sexpr.Int(1000)},
		sexpr.List{
			sexpr.List{sexpr.Symbol("pod"), sexpr.String("uid-1"), sexpr.Int(250)},
			sexpr.List{sexpr.Symbol("pod"), sexpr.String("uid-2"), sexpr.Int(500)},
		},
		sexpr.List{},
	})
}

func TestObjConvBooleanSymbols(t *testing.T) {
	// The Shen boolean singletons map to the symbols true/false in both
	// directions so a decision result stays within the sexpr alphabet.
	if got, _ := fromObj(kl.True); got != sexpr.Symbol("true") {
		t.Errorf("fromObj(True) = %#v, want Symbol(true)", got)
	}
	if got, _ := fromObj(kl.False); got != sexpr.Symbol("false") {
		t.Errorf("fromObj(False) = %#v, want Symbol(false)", got)
	}
	if obj, _ := toObj(sexpr.Symbol("true")); obj != kl.True {
		t.Error("toObj(Symbol(true)) did not yield the True singleton")
	}
	if obj, _ := toObj(sexpr.Symbol("false")); obj != kl.False {
		t.Error("toObj(Symbol(false)) did not yield the False singleton")
	}
}

func TestObjConvDeepNesting(t *testing.T) {
	// A 10k-deep left nesting: ((((...))) ...). Proves neither direction blows
	// the Go stack at realistic snapshot nesting depths.
	const depth = 10000
	v := sexpr.Value(sexpr.Int(1))
	for i := 0; i < depth; i++ {
		v = sexpr.List{v}
	}
	roundTrip(t, v)
}

func TestObjConvLargeList(t *testing.T) {
	// A flat 10k-element list. Proves the spine is walked iteratively.
	const n = 10000
	lst := make(sexpr.List, n)
	for i := range lst {
		lst[i] = sexpr.Int(int64(i))
	}
	roundTrip(t, lst)
}
