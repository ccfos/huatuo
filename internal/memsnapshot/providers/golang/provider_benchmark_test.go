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

package golang

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func BenchmarkSnapshot(b *testing.B) {
	directory := b.TempDir()
	source := filepath.Join(directory, "main.go")
	code := `package main
import ("fmt"; "os"; "runtime")
func main() {
    runtime.MemProfileRate = 1
    payloads := make([][]byte, 8)
    for i := range payloads { payloads[i] = make([]byte, 128<<10) }
    for range 2 { runtime.GC() }
    fmt.Println("ready")
    var stop [1]byte
    os.Stdin.Read(stop[:])
    runtime.KeepAlive(payloads)
}
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		b.Fatal(err)
	}
	executable := filepath.Join(directory, "heap")
	if output, err := exec.Command("go", "build", "-o", executable, source).CombinedOutput(); err != nil {
		b.Fatalf("build fixture: %v: %s", err, output)
	}
	command := exec.CommandContext(b.Context(), executable)
	stdin, err := command.StdinPipe()
	if err != nil {
		b.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := command.StdoutPipe()
	if err != nil {
		b.Fatal(err)
	}
	if err := command.Start(); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = command.Wait() }()
	ready := bufio.NewScanner(stdout)
	if !ready.Scan() || ready.Text() != "ready" {
		b.Fatal("fixture did not become ready")
	}
	identity, err := memsnapshot.ReadProcessInstance(command.Process.Pid)
	if err != nil {
		b.Fatal(err)
	}
	provider := New()
	request := memsnapshot.Request{Process: identity, TopK: 10}
	b.ReportAllocs()
	for b.Loop() {
		result, err := provider.Snapshot(b.Context(), request)
		if err != nil || result == nil || result.Status != memsnapshot.StatusComplete || result.Reason != "" || len(result.Entries) == 0 {
			b.Fatalf("snapshot = %+v, %v", result, err)
		}
		found := false
		for _, entry := range result.Entries {
			if slices.Contains(entry.Stack, "main.main") {
				found = true
				break
			}
		}
		if !found {
			b.Fatal("allocation function main.main was not resolved")
		}
	}
}
