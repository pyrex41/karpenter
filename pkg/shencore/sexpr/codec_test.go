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

package sexpr

import (
	"reflect"
	"testing"
)

var corpus = map[string]Value{
	"empty-list":  List{},
	"int":         Int(42),
	"neg-int":     Int(-17),
	"string":      String("127.0.0.1:17901"),
	"symbol":      Symbol("solve-result"),
	"digit-name":  Symbol("123abc"),
	"flat-list":   List{Symbol("req"), String("zone"), Int(3)},
	"nested-list": List{Symbol("solve"), List{Symbol("clock"), Int(1000)}, List{}},
	"decision": List{
		Symbol("disrupt-result"),
		List{Symbol("ledger"), List{Symbol("cmd-7"), Symbol("tainted")}},
		List{Symbol("actions"), List{Symbol("delete"), String("node-abc")}},
	},
	"escaped-string": String(`a "quoted" \ slash`),
}

func TestRoundTrip(t *testing.T) {
	for name, v := range corpus {
		t.Run(name, func(t *testing.T) {
			enc, err := Encode(v)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := Decode(enc)
			if err != nil {
				t.Fatalf("Decode(%q): %v", enc, err)
			}
			if !reflect.DeepEqual(v, got) {
				t.Fatalf("round-trip mismatch for %q\n  want: %#v\n  got:  %#v", enc, v, got)
			}
		})
	}
}

func TestEncodeCanonicalForms(t *testing.T) {
	cases := []struct {
		v    Value
		want string
	}{
		{List{}, "NIL"},
		{Int(0), "0"},
		{Int(-1), "-1"},
		{Symbol("solve"), "SHEN::|solve|"},
		{Symbol("123"), "SHEN::|123|"},
		{String("hi"), `"hi"`},
		{List{Int(1), Int(2)}, "(1 2)"},
	}
	for _, c := range cases {
		got, err := Encode(c.v)
		if err != nil {
			t.Fatalf("Encode(%#v): %v", c.v, err)
		}
		if string(got) != c.want {
			t.Errorf("Encode(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestDecodeRejectsTrailingBytes(t *testing.T) {
	if _, err := Decode([]byte("(1 2) 3")); err == nil {
		t.Fatal("expected trailing-bytes error, got nil")
	}
}

func TestDecodeRejectsUnknownAtom(t *testing.T) {
	if _, err := Decode([]byte("bogus")); err == nil {
		t.Fatal("expected unknown-atom error, got nil")
	}
}
