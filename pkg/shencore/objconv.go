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
	"fmt"

	"github.com/tiancaiamao/shen-go/kl"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// objconv translates between the sexpr schema layer (the wire/contract type) and
// the shen-go VM's in-memory kl.Obj values. The mapping is total over the sexpr
// alphabet (symbol/string/int/list) and over the kl.Obj shapes a pure decision
// function can return. The two boolean singletons the VM uses internally are
// mapped to the Shen symbols true/false in both directions so a decision result
// stays inside the sexpr alphabet.
//
// Both directions are iterative over list spines (cons chains) so a 10k-element
// list neither recurses per element nor overflows the Go stack; only genuine
// nesting recurses, bounded by the data's nesting depth.

// toObj converts a sexpr.Value into a freshly allocated kl.Obj tree.
func toObj(v sexpr.Value) (kl.Obj, error) {
	switch x := v.(type) {
	case sexpr.Int:
		return kl.MakeInteger(int(x)), nil
	case sexpr.String:
		return kl.MakeString(string(x)), nil
	case sexpr.Symbol:
		switch x {
		case "true":
			return kl.True, nil
		case "false":
			return kl.False, nil
		}
		return kl.MakeSymbol(string(x)), nil
	case sexpr.List:
		// Build the cons chain back-to-front so the whole list costs O(n) with no
		// recursion over the spine.
		out := kl.Nil
		for i := len(x) - 1; i >= 0; i-- {
			elem, err := toObj(x[i])
			if err != nil {
				return nil, err
			}
			out = kl.Cons(elem, out)
		}
		return out, nil
	}
	return nil, fmt.Errorf("shencore: cannot convert %T to kl.Obj", v)
}

// fromObj converts a kl.Obj produced by a decision function back into a
// sexpr.Value. It rejects shapes outside the contract alphabet (procedures,
// streams, vectors, improper lists) rather than silently coercing them.
func fromObj(o kl.Obj) (sexpr.Value, error) {
	switch {
	case o == kl.Nil:
		return sexpr.List{}, nil
	case o == kl.True:
		return sexpr.Symbol("true"), nil
	case o == kl.False:
		return sexpr.Symbol("false"), nil
	}

	switch kind := objKind(o); kind {
	case kindNumber:
		return sexpr.Int(kl.GetInteger(o)), nil
	case kindString:
		return sexpr.String(kl.GetString(o)), nil
	case kindSymbol:
		return sexpr.Symbol(kl.GetSymbol(o)), nil
	case kindPair:
		out := sexpr.List{}
		for {
			isPair, cdr := kl.IsPair(o)
			if !isPair {
				break
			}
			elem, err := fromObj(kl.Car(o))
			if err != nil {
				return nil, err
			}
			out = append(out, elem)
			o = cdr
		}
		if o != kl.Nil {
			return nil, fmt.Errorf("shencore: improper list (non-nil tail %s) outside sexpr contract", kl.ObjString(o))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("shencore: kl.Obj %q is outside the sexpr contract alphabet", kl.ObjString(o))
	}
}

// objKind classifies a kl.Obj using only the shen-go kl public API. The kl
// package exposes IsNumber/IsSymbol/IsString and IsPair predicates but no single
// type tag, so we probe them in turn.
type kind int

const (
	kindOther kind = iota
	kindNumber
	kindString
	kindSymbol
	kindPair
)

func objKind(o kl.Obj) kind {
	switch {
	case kl.IsNumber(o):
		return kindNumber
	case kl.IsString(o):
		return kindString
	case kl.IsSymbol(o):
		return kindSymbol
	}
	if isPair, _ := kl.IsPair(o); isPair {
		return kindPair
	}
	return kindOther
}
