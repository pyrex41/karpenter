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

package parity

import (
	"bytes"
	"os"
	"testing"

	"sigs.k8s.io/karpenter/pkg/shencore/schema"
)

// loadOrSkip loads the corpus, skipping the test when it is absent or empty so a
// checkout without a harvested corpus does not fail. Run `make record-corpus`
// first to populate it.
func loadOrSkip(t *testing.T) []Record {
	t.Helper()
	dir := CorpusDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("corpus dir %q absent; run `make record-corpus` to populate it", dir)
	}
	records, err := LoadCorpus(dir)
	if err != nil {
		t.Fatalf("loading corpus from %q: %v", dir, err)
	}
	if len(records) == 0 {
		t.Skipf("corpus dir %q has no records; run `make record-corpus`", dir)
	}
	return records
}

// TestCorpusByteStability is the core P0 gate: every recorded input and output
// must re-encode to exactly the bytes on disk. This proves the codec is canonical
// over the whole corpus — the precondition for byte-equality parity between the Go
// and Shen engines. A regression here means a converter introduced non-canonical
// output (an unsorted map or set, a float, a raw placeholder).
func TestCorpusByteStability(t *testing.T) {
	records := loadOrSkip(t)
	for _, rec := range records {
		// Re-encode the whole decoded record and compare to the raw file.
		decoded, err := schema.DecodeRecord(rec.Raw)
		if err != nil {
			t.Errorf("%s: decode: %v", rec.Path, err)
			continue
		}
		reencoded, err := schema.EncodeRecord(decoded)
		if err != nil {
			t.Errorf("%s: re-encode: %v", rec.Path, err)
			continue
		}
		if !bytes.Equal(rec.Raw, reencoded) {
			t.Errorf("%s: not byte-stable under decode∘encode", rec.Path)
		}
	}
	t.Logf("byte-stability verified over %d records", len(records))
}

// TestCorpusEngines compares every registered engine's decision against the
// recorded golden output, byte-for-byte. With only the identity replay engine
// registered this is a structural self-check; when the Shen engine is added it
// becomes the real Go-vs-Shen parity gate with no change to this test.
func TestCorpusEngines(t *testing.T) {
	records := loadOrSkip(t)
	for _, engine := range Engines() {
		var mismatches int
		for _, rec := range records {
			got, err := engine.Decide(rec)
			if err != nil {
				t.Errorf("engine %s: %s: decide: %v", engine.Name(), rec.Path, err)
				continue
			}
			gotBytes, err := schema.EncodeRecord(got)
			if err != nil {
				t.Errorf("engine %s: %s: encode: %v", engine.Name(), rec.Path, err)
				continue
			}
			wantBytes, err := schema.EncodeRecord(rec.Output)
			if err != nil {
				t.Errorf("engine %s: %s: encode golden: %v", engine.Name(), rec.Path, err)
				continue
			}
			if !bytes.Equal(gotBytes, wantBytes) {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("engine %s: %s: decision diverges from golden output", engine.Name(), rec.Path)
				}
			}
		}
		t.Logf("engine %s: %d/%d records matched golden output", engine.Name(), len(records)-mismatches, len(records))
	}
}

// TestCorpusKinds is a lightweight inventory of what the corpus covers, surfaced
// in test output so a harvest's shape (how many solves vs disrupts) is visible.
func TestCorpusKinds(t *testing.T) {
	records := loadOrSkip(t)
	counts := map[string]int{}
	for _, rec := range records {
		counts[rec.Kind]++
	}
	for kind, n := range counts {
		t.Logf("kind %q: %d records", kind, n)
	}
}
