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

package symbol

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestUsymResolverExecutableChangesAfterExec(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc is required to build the executable fixtures")
	}
	directory := t.TempDir()
	source := filepath.Join(directory, "target.c")
	program := `
#include <stdio.h>
#include <unistd.h>
__attribute__((noinline)) void MARKER(void) { __asm__ volatile(""); }
int main(int argc, char **argv) {
    printf("%d %p\n", getpid(), (void *)&MARKER);
    fflush(stdout);
    if (getchar() == EOF) return 2;
    if (argc > 1) {
        execl(argv[1], argv[1], (char *)NULL);
        return 3;
    }
    return 0;
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	var binaries []string
	for _, marker := range []string{"before_exec_marker", "after_exec_marker"} {
		binary := filepath.Join(directory, marker)
		build := exec.Command("cc", "-O0", "-g", "-no-pie", "-DMARKER="+marker,
			"-o", binary, source)
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", marker, err, output)
		}
		binaries = append(binaries, binary)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binaries[0], binaries[1])
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		input.Close()
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	scanner := bufio.NewScanner(output)
	readMarker := func() (uint32, uint64) {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("read child marker: %v", scanner.Err())
		}
		var pid uint32
		var address uint64
		if _, err := fmt.Sscanf(scanner.Text(), "%d 0x%x", &pid, &address); err != nil {
			t.Fatalf("parse child marker %q: %v", scanner.Text(), err)
		}
		return pid, address
	}
	beforePID, beforeAddress := readMarker()
	resolver := NewUsymResolver()
	before := resolver.UsymStackStrs(beforePID, []uint64{beforeAddress}, 1)
	if !slices.Equal(before, []string{"before_exec_marker"}) {
		t.Fatalf("before exec: %v", before)
	}
	if _, err := fmt.Fprintln(input); err != nil {
		t.Fatal(err)
	}
	afterPID, afterAddress := readMarker()
	if beforePID != afterPID || beforeAddress != afterAddress {
		t.Fatalf("fixture must reuse PID and address: before=(%d,%#x), after=(%d,%#x)",
			beforePID, beforeAddress, afterPID, afterAddress)
	}
	after := resolver.UsymStackStrs(afterPID, []uint64{afterAddress}, 1)
	if !slices.Equal(after, []string{"after_exec_marker"}) {
		t.Fatalf("after exec: %v, want [after_exec_marker]", after)
	}
	if _, err := fmt.Fprintln(input); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	afterExit := resolver.UsymStackStrs(afterPID, []uint64{afterAddress}, 1)
	if !slices.Equal(afterExit, after) {
		t.Fatalf("after exit: %v, want %v", afterExit, after)
	}
	t.Logf("pid=%d before=%v after=%v after exit=%v", afterPID, before, after, afterExit)
}
