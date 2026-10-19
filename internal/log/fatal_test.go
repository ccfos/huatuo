// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package log

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestFatalExitsAtEveryLogLevel(t *testing.T) {
	if mode := os.Getenv("HUATUO_TEST_FATAL_MODE"); mode != "" {
		SetLevel(os.Getenv("HUATUO_TEST_FATAL_LEVEL"))
		SetOutput(io.Discard)
		if mode == "Fatal" {
			Fatal("unrecoverable error")
		} else {
			Fatalf("unrecoverable error: %s", "test")
		}
		return
	}

	for _, level := range []string{"info", "fatal", "panic"} {
		for _, mode := range []string{"Fatal", "Fatalf"} {
			t.Run(level+"/"+mode, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestFatalExitsAtEveryLogLevel$")
				cmd.Env = append(os.Environ(), "HUATUO_TEST_FATAL_MODE="+mode,
					"HUATUO_TEST_FATAL_LEVEL="+level)
				err := cmd.Run()
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("%s at level %s: want exit status 1, got %v", mode, level, err)
				}
			})
		}
	}
}
