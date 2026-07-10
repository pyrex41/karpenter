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

package recorder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"sigs.k8s.io/karpenter/pkg/shencore/schema"
	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

func countSexpr(t *testing.T, dir, prefix string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".sexpr") {
			n++
		}
	}
	return n
}

func input(tag string) sexpr.Value {
	return sexpr.List{sexpr.Symbol("input"), sexpr.List{sexpr.Symbol("k"), sexpr.String(tag)}}
}

// TestWriteRecordDedup verifies records are content-addressed: identical inputs
// collapse to one file, distinct inputs produce distinct files.
func TestWriteRecordDedup(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, clocktesting.NewFakeClock(time.Unix(0, 0)))
	out := sexpr.List{sexpr.Symbol("output")}

	if err := r.WriteRecord("solve", input("a"), out); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := r.WriteRecord("solve", input("a"), out); err != nil {
		t.Fatalf("write 2 (dup): %v", err)
	}
	if got := countSexpr(t, dir, "solve-"); got != 1 {
		t.Fatalf("expected 1 deduplicated record, got %d", got)
	}
	if err := r.WriteRecord("solve", input("b"), out); err != nil {
		t.Fatalf("write 3: %v", err)
	}
	if got := countSexpr(t, dir, "solve-"); got != 2 {
		t.Fatalf("expected 2 distinct records, got %d", got)
	}
}

// TestNilRecorderNoOps verifies a disabled recorder is safe and writes nothing.
func TestNilRecorderNoOps(t *testing.T) {
	var r *Recorder
	if r.Enabled() {
		t.Fatal("nil recorder should report disabled")
	}
	if err := r.WriteRecord("solve", input("x"), sexpr.List{}); err != nil {
		t.Fatalf("nil WriteRecord should no-op, got %v", err)
	}
}

// TestFromEnv verifies the env gate.
func TestFromEnv(t *testing.T) {
	t.Setenv(EnvRecordDir, "")
	if FromEnv(clocktesting.NewFakeClock(time.Unix(0, 0))) != nil {
		t.Fatal("empty env should yield nil recorder")
	}
	dir := t.TempDir()
	t.Setenv(EnvRecordDir, dir)
	if FromEnv(clocktesting.NewFakeClock(time.Unix(0, 0))) == nil {
		t.Fatal("set env should yield a recorder")
	}
}

// TestWrittenRecordDecodes verifies a written file parses back into a well-formed
// record with the expected kind.
func TestWrittenRecordDecodes(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, clocktesting.NewFakeClock(time.Unix(0, 0)))
	if err := r.WriteRecord("disrupt", input("z"), sexpr.List{sexpr.Symbol("output")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	var path string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "disrupt-") {
			path = filepath.Join(dir, e.Name())
		}
	}
	if path == "" {
		t.Fatal("no disrupt record written")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := schema.DecodeRecord(raw); err != nil {
		t.Fatalf("written record does not decode: %v", err)
	}
}
