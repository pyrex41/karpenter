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

// Package shencore embeds the shen-go Shen VM in-process so karpenter can call
// pure Shen decision functions with s-expression snapshots. It is Phase 0
// plumbing: the wrapper, the Go<->Shen data bridge, a cooperative step budget,
// and an interpreter dev-mode loader. No karpenter decision logic lives here yet.
//
// # Runtime model
//
// The shen-go kl package keeps its Shen state — every symbol's function and
// value binding — in a single process-global symbol trie, and its VM is not
// safe for concurrent evaluation. An Engine therefore wraps that one global
// runtime: bootstrapping is done once per process, and every Call/LoadFile is
// serialized under a mutex. Constructing two Engines in one process gives two
// handles onto the same global Shen namespace, not two isolated interpreters.
//
// # Loading model (Phase 0: interpreter dev-mode)
//
// The kernel is bootstrapped by interpreting the shen-go kernel .kl sources
// in-process, then Shen decision sources (.shen) are loaded through the kernel
// loader. This is the functional path until the ratatoskr AOT artifact exists
// (a later task); see the compiled-path TODO in New.
package shencore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tiancaiamao/shen-go/kl"

	"sigs.k8s.io/karpenter/pkg/shencore/sexpr"
)

// kernelOrder is the load order of the shen-go kernel KLambda sources. It
// mirrors cmd/shen's regist() list (which replays the AOT-compiled equivalents):
// each file binds functions later files and shen.initialise depend on, so the
// order is load-bearing.
var kernelOrder = []string{
	"toplevel.kl", "core.kl", "sys.kl", "sequent.kl", "yacc.kl",
	"reader.kl", "prolog.kl", "track.kl", "load.kl", "writer.kl",
	"macros.kl", "declarations.kl", "t-star.kl", "types.kl", "dict.kl",
	"extension-launcher.kl", "init.kl",
}

// Options configures a new Engine.
type Options struct {
	// KernelDir is the directory holding the shen-go kernel .kl sources
	// (kernel/klambda). If empty, it is resolved from the SHENCORE_KERNEL_DIR
	// environment variable, then by locating the shen-go module via `go list`.
	// The release/AOT path will not need this.
	KernelDir string

	// Interpret is a list of .shen source files to load at startup, in order,
	// through the interpreter. This is the dev-mode seam behind the planned
	// --decision-engine-source=interpret:<path> flag.
	Interpret []string

	// DefaultStepBudget is the step ceiling applied by CallWithBudget when a
	// caller does not override it. Zero means unlimited (Call is always
	// unlimited regardless of this value).
	DefaultStepBudget int64
}

// Engine is a handle onto the process-global Shen runtime.
type Engine struct {
	mu          sync.Mutex
	stepBudget  int64
	loadedFiles []string
}

// process-global bootstrap state. The kl runtime is a process singleton, so the
// kernel is bootstrapped exactly once regardless of how many Engines are built.
var (
	bootOnce sync.Once
	bootErr  error
)

// New bootstraps the Shen kernel (once per process), loads any Interpret
// sources, and returns an Engine.
func New(opts Options) (*Engine, error) {
	kernelDir, err := resolveKernelDir(opts.KernelDir)
	if err != nil {
		return nil, err
	}

	bootOnce.Do(func() { bootErr = bootstrapKernel(kernelDir) })
	if bootErr != nil {
		return nil, fmt.Errorf("shencore: kernel bootstrap failed: %w", bootErr)
	}

	// TODO(ratatoskr-aot): when the AOT artifact exists, a release Engine will
	// link the shaken compiled kernel + core instead of interpreting sources
	// here. That path is a separate task; this constructor is the interpreter
	// dev-mode entry point.

	e := &Engine{stepBudget: opts.DefaultStepBudget}
	for _, path := range opts.Interpret {
		if err := e.LoadFile(path); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// bootstrapKernel interprets the kernel .kl sources in order and runs
// shen.initialise, leaving every kernel binding in the process-global trie. It
// runs under the caller's sync.Once, so it executes at most once per process.
func bootstrapKernel(kernelDir string) error {
	var cf kl.ControlFlow
	for _, name := range kernelOrder {
		path := filepath.Join(kernelDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading kernel source %s: %w", name, err)
		}
		forms, err := kl.ShenReadSExprs(data)
		if err != nil {
			return fmt.Errorf("parsing kernel source %s: %w", name, err)
		}
		for forms != kl.Nil {
			if res := kl.Eval(&cf, kl.Car(forms)); kl.IsError(res) {
				return fmt.Errorf("evaluating kernel source %s: %s", name, shenMessage(res))
			}
			forms = kl.Cdr(forms)
		}
	}
	// Swap in the native FNV-1a hash before shen.initialise builds its
	// dictionaries (every dict must be created and queried with the same hash),
	// mirroring cmd/shen.
	kl.BindSymbolFunc(kl.MakeSymbol("hash"), kl.MakePrimitive("hash", 2, kl.PrimHash))
	if res := kl.Eval(&cf, kl.Cons(kl.MakeSymbol("shen.initialise"), kl.Nil)); kl.IsError(res) {
		return fmt.Errorf("shen.initialise: %s", shenMessage(res))
	}
	return nil
}

// LoadFile loads a Shen source file into the runtime through the kernel loader,
// defining its functions in the process-global namespace.
func (e *Engine) LoadFile(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("shencore: resolving %s: %w", path, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	_, err = e.callLocked(0, "load", kl.MakeString(abs))
	if err != nil {
		return fmt.Errorf("shencore: loading %s: %w", path, err)
	}
	e.loadedFiles = append(e.loadedFiles, abs)
	return nil
}

// Call invokes the Shen function named fn with the given sexpr arguments and
// returns its result. It runs with no step budget; use CallWithBudget for a
// cooperative deadline. A raised Shen condition is returned as a *ShenError.
func (e *Engine) Call(fn string, args ...sexpr.Value) (sexpr.Value, error) {
	argObjs, err := marshalArgs(fn, args)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	out, err := e.callLocked(0, fn, argObjs...)
	if err != nil {
		return nil, err
	}
	return fromObj(out)
}

// marshalArgs converts arguments to kl.Obj. It performs no evaluation, so it is
// done outside the VM lock. Function-symbol resolution is deferred to
// callLocked, where an unbound symbol's raise is recovered into a ShenError.
func marshalArgs(fn string, args []sexpr.Value) ([]kl.Obj, error) {
	argObjs := make([]kl.Obj, len(args))
	for i, a := range args {
		o, err := toObj(a)
		if err != nil {
			return nil, fmt.Errorf("shencore: arg %d to %s: %w", i, fn, err)
		}
		argObjs[i] = o
	}
	return argObjs, nil
}

// callLocked resolves fn and invokes it on a FRESH ControlFlow (so per-call
// transient state and the step counter never leak between calls), turning a
// raised Shen condition or a returned error Obj into a typed Go error. The
// caller must hold e.mu. Resolving fn is inside the recover because an unbound
// function symbol raises a Shen condition (kl.PrimFunc panics).
//
// A step budget > 0 caps the evaluation; when it trips, the Shen runtime raises
// the "eval step limit exceeded" condition, which callers of CallWithBudget map
// to a *BudgetExceededError.
func (e *Engine) callLocked(stepBudget int64, fn string, args ...kl.Obj) (res kl.Obj, err error) {
	var cf kl.ControlFlow
	if stepBudget > 0 {
		setStepLimit(&cf, stepBudget)
	}
	defer func() {
		if r := recover(); r != nil {
			// A Shen condition escapes kl.Call as panic(Obj); anything else is a
			// genuine Go fault and must not be swallowed.
			if o, ok := r.(kl.Obj); ok && kl.IsError(o) {
				res, err = nil, &ShenError{Message: shenMessage(o)}
				return
			}
			panic(r)
		}
	}()
	fnObj := kl.PrimFunc(kl.MakeSymbol(fn))
	out := kl.Call(&cf, fnObj, args...)
	if kl.IsError(out) {
		// Some kernel paths return an error Obj rather than raising it.
		return nil, &ShenError{Message: shenMessage(out)}
	}
	return out, nil
}

// EnableTypecheck turns on Shen's sequent-calculus type checker for subsequent
// LoadFile calls. Shen's loader samples the typecheck flag once at the start of
// each file, so this must be called BEFORE the LoadFile whose sources should be
// checked; a type error then surfaces as that LoadFile's error. This is the
// mechanism behind the `shen-check` build gate.
func (e *Engine) EnableTypecheck() error { return e.setTypecheck("+") }

// DisableTypecheck turns the type checker back off for subsequent LoadFile calls.
func (e *Engine) DisableTypecheck() error { return e.setTypecheck("-") }

func (e *Engine) setTypecheck(sign string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.callLocked(0, "tc", kl.MakeSymbol(sign))
	return err
}

// LoadedFiles returns the .shen files loaded through this Engine, in load order.
func (e *Engine) LoadedFiles() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.loadedFiles...)
}

// ShenError is a Shen condition (from (error ...), a partial-function fault, a
// type error, etc.) surfaced as a Go error. The boundary never panics across
// into Go.
type ShenError struct {
	Message string
}

func (e *ShenError) Error() string { return "shen: " + e.Message }

// shenMessage renders an error Obj's message without the "error: " decoration
// the kernel sometimes prepends, so ShenError.Message is the raw condition text.
func shenMessage(o kl.Obj) string {
	msg := kl.GetString(kl.PrimErrorToString(o))
	return strings.TrimPrefix(msg, "error: ")
}

// resolveKernelDir determines the kernel/klambda directory from the explicit
// option, the environment, or the shen-go module location.
func resolveKernelDir(explicit string) (string, error) {
	if explicit != "" {
		return validateKernelDir(explicit)
	}
	if env := os.Getenv("SHENCORE_KERNEL_DIR"); env != "" {
		return validateKernelDir(env)
	}
	dir, err := locateShenGoKernel()
	if err != nil {
		return "", fmt.Errorf("shencore: no kernel dir (set Options.KernelDir or SHENCORE_KERNEL_DIR): %w", err)
	}
	return validateKernelDir(dir)
}

func validateKernelDir(dir string) (string, error) {
	probe := filepath.Join(dir, kernelOrder[0])
	if _, err := os.Stat(probe); err != nil {
		return "", fmt.Errorf("shencore: kernel dir %q missing %s: %w", dir, kernelOrder[0], err)
	}
	return dir, nil
}

// locateShenGoKernel finds the shen-go module directory via `go list` and
// appends kernel/klambda. This runs the Go toolchain, so it is only for the
// interpreter dev-mode path; the release/AOT path does not read kernel sources.
func locateShenGoKernel() (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/tiancaiamao/shen-go")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("`go list` for shen-go module: %w", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "kernel", "klambda"), nil
}
