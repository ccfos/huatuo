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

package executil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStopWaitsForGroupMemberGrace(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the process-group regression")
	}
	marker := filepath.Join(t.TempDir(), "cleaned")
	script := `import os, signal, sys, time
r,w = os.pipe()
pid = os.fork()
if pid == 0:
    os.close(r)
    fd = os.open('/dev/null', os.O_RDWR)
    for n in (0,1,2): os.dup2(fd,n)
    def cleanup(sig, frame):
        time.sleep(0.05)
        open(os.environ["MARKER"], "w").write("clean")
        sys.exit(0)
    signal.signal(signal.SIGTERM, cleanup)
    os.write(w,b'R'); os.close(w)
    while True: signal.pause()
os.close(w)
os.read(r,1); os.close(r)
signal.signal(signal.SIGTERM, lambda a,b: sys.exit(0))
print(pid, flush=True)
while True: signal.pause()
`
	p, err := New(Spec{
		Path: python, Args: []string{"-u", "-c", script},
		Env: append(os.Environ(), "MARKER="+marker),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Stop(ctx)
		_ = p.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := p.Stdout()
		if strings.TrimSpace(string(data)) != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	data, _ := p.Stdout()
	if strings.TrimSpace(string(data)) == "" {
		t.Fatal("helper did not become ready")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	err = p.Stop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "clean" {
		t.Fatalf("child could not finish SIGTERM cleanup: marker=%q, err=%v", got, err)
	}
}
