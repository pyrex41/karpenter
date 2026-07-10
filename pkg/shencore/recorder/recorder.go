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

// Package recorder captures each scheduling and disruption decision as a golden
// (input, output) s-expression pair, building the corpus that all Go-vs-Shen
// parity testing hangs on. It is dormant unless the SHENCORE_RECORD_DIR
// environment variable names a directory: a nil *Recorder short-circuits every
// method, so recording adds a single nil check to the hot path when disabled.
//
// Files are content-addressed and written once. The instance-type catalog — the
// bulky, rarely-changing part of a snapshot — is written to catalog-<hash>.sexpr
// and referenced by hash from each record, so many records share one catalog
// blob. Records themselves are named by the hash of their input, which
// deduplicates the identical trivial scenarios the big suites produce in bulk.
package recorder

import (
	"os"
	"path/filepath"
	"sync"

	"k8s.io/utils/clock"

	pscheduling "sigs.k8s.io/karpenter/pkg/controllers/provisioning/scheduling"
	"sigs.k8s.io/karpenter/pkg/shencore/schema"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// EnvRecordDir is the environment variable that both enables recording and names
// the output directory.
const EnvRecordDir = "SHENCORE_RECORD_DIR"

// Recorder writes golden records to a directory. The zero value is not usable;
// obtain one from FromEnv or New. All methods are safe to call on a nil receiver
// (they no-op), so callers hold a possibly-nil *Recorder and never branch.
type Recorder struct {
	dir   string
	clock clock.Clock

	mu       sync.Mutex
	catalogs map[string]struct{} // in-process memo of catalog hashes already written
}

// FromEnv returns a Recorder rooted at SHENCORE_RECORD_DIR, or nil if the
// variable is unset or empty. A nil Recorder disables recording.
func FromEnv(clk clock.Clock) *Recorder {
	dir := os.Getenv(EnvRecordDir)
	if dir == "" {
		return nil
	}
	return New(dir, clk)
}

// New returns a Recorder writing to dir.
func New(dir string, clk clock.Clock) *Recorder {
	return &Recorder{dir: dir, clock: clk, catalogs: map[string]struct{}{}}
}

// Enabled reports whether recording is active. Useful to skip assembling inputs
// when there is nothing to record.
func (r *Recorder) Enabled() bool { return r != nil }

// RecordSolve serializes a provisioning Solve — its input snapshot and the
// scheduler's Results — into one record file, writing the referenced catalog
// blob first if it has not been seen.
func (r *Recorder) RecordSolve(in schema.SolveInput, results pscheduling.Results) error {
	if r == nil {
		return nil
	}
	input, catalog, catalogHash := schema.SolveInputValue(in, r.clock)
	if err := r.writeCatalog(catalogHash, catalog); err != nil {
		return err
	}
	output := schema.SolveResultValue(results)
	return r.writeRecord("solve", input, output)
}

// WriteRecord serializes an already-built (input ...) / (output ...) pair. The
// disruption controller uses this directly because its Candidate/Command types
// live in a package the recorder must not import (an import cycle otherwise); it
// builds the forms with the schema converters and hands them here.
func (r *Recorder) WriteRecord(kind string, input, output sexpr.Value) error {
	if r == nil {
		return nil
	}
	return r.writeRecord(kind, input, output)
}

func (r *Recorder) writeRecord(kind string, input, output sexpr.Value) error {
	record := schema.Record(kind, input, output)
	b, err := schema.EncodeRecord(record)
	if err != nil {
		return err
	}
	name := kind + "-" + schema.InputHash(input) + ".sexpr"
	return r.writeOnce(filepath.Join(r.dir, name), b)
}

func (r *Recorder) writeCatalog(hash string, catalog sexpr.Value) error {
	r.mu.Lock()
	_, seen := r.catalogs[hash]
	if !seen {
		r.catalogs[hash] = struct{}{}
	}
	r.mu.Unlock()
	if seen {
		return nil
	}
	b, err := schema.EncodeRecord(catalog)
	if err != nil {
		return err
	}
	return r.writeOnce(filepath.Join(r.dir, "catalog-"+hash+".sexpr"), b)
}

// writeOnce writes content to path if it does not already exist. Concurrent test
// processes share one corpus directory, so the write is content-addressed and
// idempotent: the first writer wins and the rest observe the file already there.
// The write is staged to a unique temp file and renamed so a reader never sees a
// half-written record.
func (r *Recorder) writeOnce(path string, content []byte) error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil // already present (content-addressed → identical bytes)
	}
	tmp, err := os.CreateTemp(r.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename is atomic within a directory; if another process won the race the
	// destination already exists and we can drop ours.
	if err := os.Rename(tmpName, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}
