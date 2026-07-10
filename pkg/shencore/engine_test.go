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
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// The kl runtime is process-global, so bootstrap it and load the smoke source
// once and share the Engine across the suite.
var (
	testEngineOnce sync.Once
	testEngine     *Engine
	testEngineErr  error
)

func engine(t *testing.T) *Engine {
	t.Helper()
	testEngineOnce.Do(func() {
		testEngine, testEngineErr = New(Options{
			DefaultStepBudget: 2_000_000,
			Interpret:         []string{"testdata/smoke.shen"},
		})
	})
	if testEngineErr != nil {
		t.Fatalf("New: %v", testEngineErr)
	}
	return testEngine
}

func TestEngineLoadAndListFiles(t *testing.T) {
	e := engine(t)
	files := e.LoadedFiles()
	if len(files) != 1 {
		t.Fatalf("LoadedFiles = %v, want one entry", files)
	}
}

func TestEngineCallListSum(t *testing.T) {
	e := engine(t)
	got, err := e.Call("list-sum", sexpr.List{sexpr.Int(10), sexpr.Int(20), sexpr.Int(12)})
	if err != nil {
		t.Fatalf("Call list-sum: %v", err)
	}
	if got != sexpr.Int(42) {
		t.Fatalf("list-sum = %#v, want Int(42)", got)
	}
}

func TestEngineCallEchoRoundTrip(t *testing.T) {
	e := engine(t)
	arg := sexpr.List{
		sexpr.Symbol("disrupt-result"),
		sexpr.List{sexpr.Symbol("ledger"), sexpr.List{sexpr.Symbol("cmd-7"), sexpr.Symbol("tainted")}},
		sexpr.String("note"),
		sexpr.Int(-3),
	}
	got, err := e.Call("echo", arg)
	if err != nil {
		t.Fatalf("Call echo: %v", err)
	}
	if !reflect.DeepEqual(got, arg) {
		t.Fatalf("echo round-trip mismatch\n  want: %#v\n  got:  %#v", arg, got)
	}
}

func TestEngineShenErrorMapping(t *testing.T) {
	e := engine(t)
	_, err := e.Call("boom", sexpr.Int(0))
	if err == nil {
		t.Fatal("Call boom: expected error, got nil")
	}
	var se *ShenError
	if !errors.As(err, &se) {
		t.Fatalf("Call boom: error is %T (%v), want *ShenError", err, err)
	}
	if se.Message != "kaboom" {
		t.Fatalf("ShenError.Message = %q, want %q", se.Message, "kaboom")
	}
}

func TestEngineUnknownFunction(t *testing.T) {
	e := engine(t)
	if _, err := e.Call("no-such-fn", sexpr.Int(1)); err == nil {
		t.Fatal("Call to undefined function: expected error, got nil")
	}
}

func TestEngineBudgetExceeded(t *testing.T) {
	e := engine(t)
	_, err := e.CallWithBudget(context.Background(), "spin", sexpr.Int(0))
	if err == nil {
		t.Fatal("CallWithBudget spin: expected budget error, got nil")
	}
	var be *BudgetExceededError
	if !errors.As(err, &be) {
		t.Fatalf("CallWithBudget spin: error is %T (%v), want *BudgetExceededError", err, err)
	}
	if be.Budget != 2_000_000 {
		t.Fatalf("BudgetExceededError.Budget = %d, want 2000000", be.Budget)
	}
}

func TestEngineBudgetAllowsHonestWork(t *testing.T) {
	e := engine(t)
	got, err := e.CallWithBudget(context.Background(), "list-sum",
		sexpr.List{sexpr.Int(1), sexpr.Int(2), sexpr.Int(3), sexpr.Int(4)})
	if err != nil {
		t.Fatalf("CallWithBudget list-sum: %v", err)
	}
	if got != sexpr.Int(10) {
		t.Fatalf("list-sum = %#v, want Int(10)", got)
	}
}

func TestEngineBudgetRefusesCancelledContext(t *testing.T) {
	e := engine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.CallWithBudget(ctx, "list-sum", sexpr.List{sexpr.Int(1)})
	var be *BudgetExceededError
	if !errors.As(err, &be) {
		t.Fatalf("error is %T (%v), want *BudgetExceededError", err, err)
	}
	if !errors.Is(be.Cause, context.Canceled) {
		t.Fatalf("BudgetExceededError.Cause = %v, want context.Canceled", be.Cause)
	}
}

// TestEngineCallAfterFaultsRecovers proves the fresh-ControlFlow-per-call design
// isolates transient state: an error call and a budget trip do not corrupt the
// runtime for subsequent calls.
func TestEngineCallAfterFaultsRecovers(t *testing.T) {
	e := engine(t)
	_, _ = e.Call("boom", sexpr.Int(0))
	_, _ = e.CallWithBudget(context.Background(), "spin", sexpr.Int(0))
	got, err := e.Call("list-sum", sexpr.List{sexpr.Int(5), sexpr.Int(5)})
	if err != nil {
		t.Fatalf("Call after faults: %v", err)
	}
	if got != sexpr.Int(10) {
		t.Fatalf("list-sum after faults = %#v, want Int(10)", got)
	}
}
