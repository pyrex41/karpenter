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

// Package parity replays the golden sexpr corpus recorded by pkg/shencore/recorder
// and checks decision engines against it. Today it enforces byte-stability of the
// corpus (decode∘encode is the identity) and provides the plug point for a second
// engine — the Shen decision core — to be compared against the recorded Go
// decisions byte-for-byte. Until that engine lands, the only registered engine is
// the identity "replay" engine, so the comparison is a structural self-check.
package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/karpenter/pkg/shencore/schema"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// Record is one decoded corpus entry.
type Record struct {
	Path   string
	Kind   string // "solve" or "disrupt"
	Input  sexpr.Value
	Output sexpr.Value
	Raw    []byte // original file bytes, for byte-stability comparison
}

// Engine is a decision engine that, given a record's input, produces the decision
// output. The Go path is captured in the corpus (the "golden" output); an
// alternative engine (e.g. Shen) implements this interface and is compared against
// that golden output. The identity replayEngine returns the recorded output and
// exists so the harness has a trivially-correct baseline and a worked example of
// the plug point.
type Engine interface {
	Name() string
	// Decide returns the decision output for the given record. It may return the
	// recorded output (replay) or compute a fresh one (a real engine).
	Decide(rec Record) (sexpr.Value, error)
}

// replayEngine returns each record's own recorded output. It makes the
// engine-comparison test meaningful before a real second engine exists: parity
// against replay is definitional, so a failure indicates a harness bug rather than
// an engine disagreement.
type replayEngine struct{}

func (replayEngine) Name() string                           { return "replay" }
func (replayEngine) Decide(rec Record) (sexpr.Value, error) { return rec.Output, nil }

// Engines returns the engines the parity suite compares against the golden corpus.
// A future Shen engine is appended here.
func Engines() []Engine {
	return []Engine{replayEngine{}}
}

// DefaultCorpusDir is the full harvested corpus (gitignored, regenerated with
// `make record-corpus`). SampleCorpusDir is the small committed subset CI replays.
const (
	DefaultCorpusDir = "corpus"
	SampleCorpusDir  = "corpus-sample"
)

// CorpusDir resolves the corpus directory: the PARITY_CORPUS_DIR override if set;
// else the full corpus when it has been harvested; else the committed sample. This
// makes the parity suite run against the full corpus locally after a harvest and
// against the sample in a fresh checkout.
func CorpusDir() string {
	if d := os.Getenv("PARITY_CORPUS_DIR"); d != "" {
		return d
	}
	if hasRecords(DefaultCorpusDir) {
		return DefaultCorpusDir
	}
	return SampleCorpusDir
}

// hasRecords reports whether dir exists and contains at least one non-catalog
// record file.
func hasRecords(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sexpr") && !strings.HasPrefix(e.Name(), "catalog-") {
			return true
		}
	}
	return false
}

// LoadCorpus reads and decodes every record file in dir. Catalog blobs
// (catalog-*.sexpr) are skipped — they are referenced by hash from records, not
// replayed directly. Files are returned sorted by path for stable iteration.
func LoadCorpus(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sexpr") || strings.HasPrefix(e.Name(), "catalog-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		rec, err := parseRecord(path, raw)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, nil
}

// parseRecord decodes a record file into its kind/input/output components.
func parseRecord(path string, raw []byte) (Record, error) {
	v, err := schema.DecodeRecord(raw)
	if err != nil {
		return Record{}, err
	}
	list, ok := v.(sexpr.List)
	if !ok || len(list) == 0 {
		return Record{}, fmt.Errorf("record is not a non-empty list")
	}
	if head, ok := list[0].(sexpr.Symbol); !ok || head != "record" {
		return Record{}, fmt.Errorf("record does not start with the `record` tag")
	}
	rec := Record{Path: path, Raw: raw}
	for _, field := range list[1:] {
		fl, ok := field.(sexpr.List)
		if !ok || len(fl) == 0 {
			continue
		}
		tag, _ := fl[0].(sexpr.Symbol)
		switch tag {
		case "kind":
			if len(fl) == 2 {
				if s, ok := fl[1].(sexpr.Symbol); ok {
					rec.Kind = string(s)
				}
			}
		case "input":
			rec.Input = field
		case "output":
			rec.Output = field
		}
	}
	if rec.Input == nil || rec.Output == nil {
		return Record{}, fmt.Errorf("record missing input or output")
	}
	return rec, nil
}
