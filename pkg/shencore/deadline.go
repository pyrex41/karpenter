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

	"github.com/tiancaiamao/shen-go/kl"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// stepLimitTripMessage is the exact condition text the shen-go VM raises when a
// ControlFlow step budget is exhausted (kl/eval.go tripStepLimit). We match on
// it to distinguish a budget expiry from an ordinary Shen error.
const stepLimitTripMessage = "eval step limit exceeded"

// BudgetExceededError reports that a Shen call exhausted its cooperative step
// budget (or its context was already cancelled) before producing a result. It
// is distinct from ShenError so callers can treat a deadline differently from a
// decision-level failure.
type BudgetExceededError struct {
	// Budget is the step ceiling that tripped, or 0 when the call was refused
	// because its context was already done.
	Budget int64
	// Cause is the context error when the context was already done, else nil.
	Cause error
}

func (e *BudgetExceededError) Error() string {
	if e.Cause != nil {
		return "shencore: budget refused: " + e.Cause.Error()
	}
	return "shencore: step budget exceeded"
}

func (e *BudgetExceededError) Unwrap() error { return e.Cause }

// CallWithBudget invokes fn under a cooperative step budget derived from the
// Engine's DefaultStepBudget. If the budget trips, it returns a
// *BudgetExceededError instead of a *ShenError. A context that is already done
// before the call short-circuits to a *BudgetExceededError.
//
// The budget is a step count, not wall-clock time: the shen-go VM is
// single-threaded and can only be interrupted cooperatively at step boundaries,
// so ctx bounds admission (checked here) while the step ceiling bounds the work.
// A future task may translate an observed steps/second rate into a ctx-deadline
// derived ceiling; the seam is intentionally left here.
func (e *Engine) CallWithBudget(ctx context.Context, fn string, args ...sexpr.Value) (sexpr.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, &BudgetExceededError{Cause: err}
	}

	argObjs, err := marshalArgs(fn, args)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	budget := e.stepBudget
	out, callErr := e.callLocked(budget, fn, argObjs...)
	e.mu.Unlock()

	if callErr != nil {
		if se, ok := callErr.(*ShenError); ok && se.Message == stepLimitTripMessage {
			return nil, &BudgetExceededError{Budget: budget}
		}
		return nil, callErr
	}
	return fromObj(out)
}

// setStepLimit sets the step ceiling on a ControlFlow the Engine owns, using
// shen-go's exported budget API.
func setStepLimit(cf *kl.ControlFlow, limit int64) {
	cf.SetStepLimit(limit)
}
