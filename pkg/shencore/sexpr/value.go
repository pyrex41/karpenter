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

// Package sexpr encodes and decodes the subset of Common Lisp s-expression
// syntax used as the Go<->Shen data contract for the shencore decision engine.
//
// It is adapted from the parity-checked codec in shen_split (go/sexpr), which
// re-implements the shen-raft server's canonical wire format byte-for-byte. The
// schema layer here is intentionally identical so a decision snapshot can be
// carried in-process (as kl.Obj, via objconv) or over the sidecar wire with no
// contract change.
//
// Alphabet:
//
//	Int    int64 decimal
//	String UTF-8 (control bytes < 0x20 and 0x7F rejected on encode)
//	Symbol always SHEN:: package; vbar form |NAME| when the name has
//	       any char that would not round-trip under the default :upcase
//	       readtable (which covers every canonical lowercase-kebab
//	       payload symbol)
//	List   proper; empty list encodes as NIL, not ()
//
// Floats, characters, vectors, hash tables, bignums, and improper lists are
// intentionally out of alphabet — the decision contract never emits them.
package sexpr

// Value is implemented by every node type. isValue keeps the set closed.
type Value interface {
	isValue()
}

// Int is a signed 64-bit integer.
type Int int64

// String is a UTF-8 string. Control bytes < 0x20 and 0x7F are rejected on
// encode because CL's :readably t escapes them with #A(...) array-literal
// syntax, which this encoder deliberately does not reproduce.
type String string

// Symbol is the bare name (no package prefix) of a SHEN-package symbol.
// Encoder always emits the SHEN:: prefix; decoder strips it.
type Symbol string

// List is a proper list. Empty list encodes as NIL.
type List []Value

func (Int) isValue()    {}
func (String) isValue() {}
func (Symbol) isValue() {}
func (List) isValue()   {}
