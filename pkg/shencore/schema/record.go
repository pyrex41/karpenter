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
	"fmt"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// SchemaVersion is bumped whenever the sexpr contract changes shape, so a corpus
// recorded under an old schema is distinguishable from the current one.
const SchemaVersion = 1

// Record wraps an input and an output form into a single self-describing record:
//
//	(record (kind <kind>) (version <n>) (input ...) (output ...))
//
// kind is "solve" or "disrupt". input and output are the (input ...)/(output ...)
// forms produced by the SOLVE or DISRUPT converters.
func Record(kind string, input, output sexpr.Value) sexpr.Value {
	return tagged("record",
		tagged("kind", sym(kind)),
		tagged("version", intv(SchemaVersion)),
		input,
		output,
	)
}

// InputHash returns a short, stable content hash of a record's input form, used
// to name the record file. Two records with identical inputs (a very common case
// across the giant suites) hash to the same name and deduplicate on disk.
func InputHash(input sexpr.Value) string {
	return hashValue(input)[:16]
}

// EncodeRecord returns the canonical bytes of a record with a trailing newline,
// so a corpus directory reads cleanly and diffs line-terminate.
func EncodeRecord(record sexpr.Value) ([]byte, error) {
	b, err := sexpr.Encode(record)
	if err != nil {
		return nil, fmt.Errorf("encoding record: %w", err)
	}
	return append(b, '\n'), nil
}

// DecodeRecord parses record bytes back into a value, tolerating the trailing
// newline that EncodeRecord adds.
func DecodeRecord(b []byte) (sexpr.Value, error) {
	// Trim a single trailing newline if present; the codec rejects trailing bytes.
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return sexpr.Decode(b)
}
