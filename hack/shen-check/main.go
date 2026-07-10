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

// Command shen-check is the (tc +) build gate for the Shen decision sources: it
// bootstraps the Shen kernel, turns on the sequent-calculus type checker, and
// loads each .shen file passed on the command line. A type error in any file
// makes the load fail, which this command reports and turns into a nonzero
// exit — so `make shen-check` fails the build on an ill-typed decision core.
//
// Files are loaded in the order given, into one shared namespace, so pass
// dependencies (e.g. shen/types before shen/core) first.
package main

import (
	"fmt"
	"os"

	"sigs.k8s.io/karpenter/pkg/shencore"
)

func main() {
	files := os.Args[1:]
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: shen-check <file.shen>...")
		os.Exit(2)
	}

	engine, err := shencore.New(shencore.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "shen-check: kernel bootstrap failed: %v\n", err)
		os.Exit(2)
	}
	if err := engine.EnableTypecheck(); err != nil {
		fmt.Fprintf(os.Stderr, "shen-check: enabling (tc +) failed: %v\n", err)
		os.Exit(2)
	}

	failed := false
	for _, f := range files {
		if err := engine.LoadFile(f); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL  %s\n      %v\n", f, err)
			failed = true
			continue
		}
		fmt.Printf("ok    %s\n", f)
	}
	if failed {
		os.Exit(1)
	}
}
