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

package java

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentLibrarySessionsDoNotShareFiles(t *testing.T) {
	targetDir := t.TempDir()
	firstTool := t.TempDir()
	secondTool := t.TempDir()
	for tool, contents := range map[string]string{
		firstTool:  "first-agent",
		secondTool: "second-agent",
	} {
		libDir := filepath.Join(tool, "lib")
		if err := os.Mkdir(libDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(libDir, "libasyncProfiler.so"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(libDir, "libasyncProfiler.so"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := copyAgentLib(firstTool, targetDir, "first"); err != nil {
		t.Fatal(err)
	}
	if err := copyAgentLib(secondTool, targetDir, "second"); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(targetDir, filepath.Base(agentTargetPath("first")))
	second := filepath.Join(targetDir, filepath.Base(agentTargetPath("second")))
	got, err := os.ReadFile(first)
	if err != nil || string(got) != "first-agent" {
		t.Fatalf("first session agent=%q, error=%v", got, err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(second)
	if err != nil || string(got) != "second-agent" {
		t.Fatalf("second session agent=%q, error=%v", got, err)
	}
}
