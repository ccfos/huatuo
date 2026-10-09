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

//go:build integration && linux

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	bamaiconfig "github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/memsnapshot/providers/python"
)

func TestSnapshotLiveGoProcess(t *testing.T) {
	for _, mode := range []string{"exe", "pie"} {
		for _, test := range []struct {
			name       string
			sampleRate int
			gcCycles   int
		}{
			{name: "disabled", sampleRate: 0, gcCycles: 2},
			{name: "published", sampleRate: 1, gcCycles: 2},
			{name: "unpublished", sampleRate: 1, gcCycles: 0},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				snapshotLiveGoProcess(t, mode, test.sampleRate, test.gcCycles)
			})
		}
	}
}

func snapshotLiveGoProcess(t *testing.T, mode string, sampleRate, gcCycles int) {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "heap.go")
	if err := os.WriteFile(source, []byte(`
package main

import (
    "fmt"
    "os"
    "runtime"
    "strconv"
    "time"
)

func main() {
    rate, err := strconv.Atoi(os.Args[1])
    if err != nil { panic(err) }
    gcCycles, err := strconv.Atoi(os.Args[2])
    if err != nil { panic(err) }
    // Rate 1 makes the enabled case deterministic; rate 0 disables profiling.
    runtime.MemProfileRate = rate
    payloads := make([][]byte, 8)
    for i := range payloads {
        payloads[i] = make([]byte, 128<<10)
        payloads[i][0] = byte(i)
    }
    for i := 0; i < gcCycles; i++ {
        runtime.GC()
    }
    fmt.Println("ready")
    time.Sleep(time.Minute)
    runtime.KeepAlive(payloads)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "heap")
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), time.Minute)
	defer cancelBuild()
	if output, err := exec.CommandContext(buildCtx, "go", "build",
		"-buildmode="+mode, "-o", executable, source).CombinedOutput(); err != nil {
		t.Fatalf("compile Go fixture: %v: %s", err, output)
	}

	fixtureCtx, stopFixture := context.WithTimeout(t.Context(), time.Minute)
	defer stopFixture()
	command := exec.CommandContext(fixtureCtx, executable, strconv.Itoa(sampleRate), strconv.Itoa(gcCycles))
	// Only explicit collections may publish the fixture's heap profile.
	command.Env = append(os.Environ(), "GOGC=off", "GOMEMLIMIT=off")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopFixture()
		_ = command.Wait()
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("Go fixture did not acknowledge readiness")
	}

	process, err := memsnapshot.ReadProcessInstanceID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	snapshotCtx, cancelSnapshot := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelSnapshot()
	result, err := collector.Snapshot(snapshotCtx, process, collector.Options{
		MaxMemoryObjectEntries: 10, SnapshotTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Process != process || result.Language != memsnapshot.LanguageGo ||
		result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
		t.Fatalf("live Go collector result = %+v", result)
	}
	snapshot := result.Snapshot
	if snapshot.RuntimeVersion == "" {
		t.Fatalf("live Go snapshot has no runtime version: %+v", snapshot)
	}
	if sampleRate == 0 {
		if snapshot.Status != memsnapshot.SnapshotStatusUnavailable ||
			snapshot.StatusReason != "Go heap profiling is disabled by MemProfileRate=0" ||
			len(snapshot.Entries) != 0 || snapshot.OutputTruncated {
			t.Fatalf("disabled Go profiling snapshot = %+v", snapshot)
		}
	} else if gcCycles == 0 {
		if snapshot.Status != memsnapshot.SnapshotStatusUnavailable ||
			snapshot.StatusReason != "Go heap profile has no published statistics" ||
			len(snapshot.Entries) != 0 || snapshot.OutputTruncated {
			t.Fatalf("unpublished Go heap profile snapshot = %+v", snapshot)
		}
	} else {
		if snapshot.Status != memsnapshot.SnapshotStatusComplete && snapshot.Status != memsnapshot.SnapshotStatusPartial {
			t.Fatalf("live Go snapshot status = %q, reason = %q",
				snapshot.Status, snapshot.StatusReason)
		}
		if len(snapshot.Entries) == 0 {
			t.Fatalf("live Go snapshot has no runtime data: %+v", snapshot)
		}
		for index := range snapshot.Entries {
			if kind := snapshot.Entries[index].Kind; kind != "inuse_space_objects" {
				t.Fatalf("live Go entry %d kind = %q, want inuse_space_objects", index, kind)
			}
		}
	}

	t.Run("snapshot timeout", func(t *testing.T) {
		result, err := collector.Snapshot(t.Context(), process, collector.Options{
			MaxMemoryObjectEntries: 10, SnapshotTimeout: time.Nanosecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Snapshot.Status != memsnapshot.SnapshotStatusFailed || len(result.Snapshot.Entries) != 0 ||
			!strings.Contains(result.Snapshot.StatusReason, context.DeadlineExceeded.Error()) {
			t.Fatalf("timed-out Go snapshot = %+v", result.Snapshot)
		}
		if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
			t.Fatalf("timed-out Go snapshot lost process memory: %+v", result)
		}
	})

	t.Run("exit before detection", func(t *testing.T) {
		// Selection can succeed before the process exits and removes its procfs files.
		stopFixture()
		_ = command.Wait()
		result, err := collector.Snapshot(t.Context(), process, collector.Options{})
		if result != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("exited process snapshot = %+v, %v; want no result and missing process error", result, err)
		}
		if !strings.Contains(err.Error(), "detect process runtime:") {
			t.Fatalf("missing detection error context: %v", err)
		}
	})
}

// TestSnapshotLiveHotSpotProcess is an optional environment validation rather
// than a required CI gate. It exercises a real HotSpot process when a supported
// JDK is already available and otherwise skips without installing one.
func TestSnapshotLiveHotSpotProcess(t *testing.T) {
	t.Run("arrays", func(t *testing.T) { snapshotLiveHotSpotProcess(t, false) })
	t.Run("finalizable", func(t *testing.T) { snapshotLiveHotSpotProcess(t, true) })
}

func snapshotLiveHotSpotProcess(t *testing.T, finalizable bool) {
	t.Helper()
	javaPath, javaErr := exec.LookPath("java")
	javacPath, javacErr := exec.LookPath("javac")
	if javaErr != nil || javacErr != nil {
		t.Skip("java and javac are required")
	}
	requireSupportedHotSpot(t, javaPath, javacPath)
	directory := t.TempDir()
	source := filepath.Join(directory, "HeapFixture.java")
	if err := os.WriteFile(source, []byte(`
import java.util.ArrayList;
import java.util.List;

public class HeapFixture {
    private static final List<Object> OBJECTS = new ArrayList<>();
    static class FinalizablePayload {
        long a, b, c, d, e, f, g, h, i, j, k, l, m, n, o, p;
        protected void finalize() { a = 1; }
    }

    public static void main(String[] args) throws Exception {
        for (int i = 0; i < 200000; i++) {
            OBJECTS.add(args.length > 0 ? new FinalizablePayload() : new byte[128]);
        }
        System.out.println("ready");
        Thread.sleep(60000);
    }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), javacPath,
		"-source", "8", "-target", "8", source).CombinedOutput(); err != nil {
		t.Fatalf("compile HotSpot fixture: %v: %s", err, output)
	}

	javaArgs := []string{"-XX:+UseG1GC", "-Xms32m", "-Xmx64m"}
	if exec.CommandContext(t.Context(), javaPath,
		"-XX:-UseCompactObjectHeaders", "-version").Run() == nil {
		javaArgs = append(javaArgs, "-XX:-UseCompactObjectHeaders")
	}
	if finalizable {
		if output, err := exec.CommandContext(t.Context(), javaPath,
			"-XX:-RegisterFinalizersAtInit", "-version").CombinedOutput(); err != nil {
			t.Skipf("JVM does not support slow finalizer allocation: %v: %s", err, output)
		}
		javaArgs = append(javaArgs, "-XX:-RegisterFinalizersAtInit")
	}
	javaArgs = append(javaArgs, "-cp", directory, "HeapFixture")
	if finalizable {
		javaArgs = append(javaArgs, "finalizable")
	}
	fixtureCtx, stopFixture := context.WithTimeout(t.Context(), time.Minute)
	defer stopFixture()
	command := exec.CommandContext(fixtureCtx, javaPath, javaArgs...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopFixture()
		_ = command.Wait()
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("HotSpot fixture did not acknowledge readiness")
	}

	process, err := memsnapshot.ReadProcessInstanceID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	snapshotCtx, cancelSnapshot := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelSnapshot()
	result, err := collector.Snapshot(snapshotCtx, process, collector.Options{
		MaxMemoryObjectEntries: 10, SnapshotTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Process != process || result.Language != memsnapshot.LanguageJava ||
		result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
		t.Fatalf("live Java collector result = %+v", result)
	}
	snapshot := result.Snapshot
	if snapshot.Status != memsnapshot.SnapshotStatusComplete && snapshot.Status != memsnapshot.SnapshotStatusPartial {
		t.Fatalf("live HotSpot snapshot status = %q, reason = %q",
			snapshot.Status, snapshot.StatusReason)
	}
	if snapshot.RuntimeVersion == "" || len(snapshot.Entries) == 0 {
		t.Fatalf("live HotSpot snapshot has no runtime data: %+v", snapshot)
	}
	for index := range snapshot.Entries {
		if kind := snapshot.Entries[index].Kind; kind != "object_class" {
			t.Fatalf("live HotSpot entry %d kind = %q, want object_class", index, kind)
		}
	}
	if finalizable {
		for _, entry := range snapshot.Entries {
			if strings.Contains(entry.Name, "FinalizablePayload") && entry.Bytes > 0 && entry.Objects > 0 {
				return
			}
		}
		t.Fatalf("retained finalizable payloads are missing: %+v", snapshot)
	}
}

func requireSupportedHotSpot(t *testing.T, javaPath, javacPath string) {
	t.Helper()
	runtimeOutput, err := exec.CommandContext(t.Context(), javaPath,
		"-XshowSettings:properties", "-version").CombinedOutput()
	if err != nil {
		t.Skipf("cannot inspect Java runtime: %v: %s", err, runtimeOutput)
	}
	runtimeMajor, vmName, err := parseJavaRuntime(runtimeOutput)
	if err != nil {
		t.Skipf("cannot parse Java runtime information: %v", err)
	}
	vmNameLower := strings.ToLower(vmName)
	if !strings.Contains(vmNameLower, "hotspot") &&
		!strings.Contains(vmNameLower, "openjdk") {
		t.Skipf("requires a HotSpot-compatible VM, found %q", vmName)
	}
	if runtimeMajor < 8 {
		t.Skipf("requires Java 8 or newer, found Java %d", runtimeMajor)
	}

	compilerOutput, err := exec.CommandContext(t.Context(), javacPath,
		"-version").CombinedOutput()
	if err != nil {
		t.Skipf("cannot inspect javac: %v: %s", err, compilerOutput)
	}
	compilerFields := strings.Fields(string(compilerOutput))
	if len(compilerFields) < 2 || compilerFields[0] != "javac" {
		t.Skipf("cannot parse javac version: %q", compilerOutput)
	}
	compilerMajor, err := parseJavaMajor(compilerFields[1])
	if err != nil {
		t.Skipf("cannot parse javac version: %q", compilerOutput)
	}
	if compilerMajor < 8 {
		t.Skipf("requires javac 8 or newer, found javac %d", compilerMajor)
	}
}

func parseJavaRuntime(output []byte) (int, string, error) {
	properties := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			properties[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	version := properties["java.specification.version"]
	vmName := properties["java.vm.name"]
	if version == "" || vmName == "" {
		return 0, "", errors.New("java specification version or VM name is missing")
	}
	major, err := parseJavaMajor(version)
	if err != nil {
		return 0, "", err
	}
	return major, vmName, nil
}

func parseJavaMajor(version string) (int, error) {
	parts := strings.Split(strings.Trim(version, `"`), ".")
	if len(parts) == 0 {
		return 0, errors.New("empty Java version")
	}
	index := 0
	if parts[0] == "1" {
		if len(parts) < 2 {
			return 0, errors.New("legacy Java version has no major component")
		}
		index = 1
	}
	major, err := strconv.Atoi(parts[index])
	if err != nil || major <= 0 {
		return 0, errors.New("invalid Java major version")
	}
	return major, nil
}

func TestSnapshotLiveCPythonProcess(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		skipMissingRuntime(t, "python3 is not installed")
	}
	requireSupportedCPython(t, pythonPath)
	fixtureCtx, stopFixture := context.WithTimeout(t.Context(), time.Minute)
	defer stopFixture()
	command := exec.CommandContext(fixtureCtx, pythonPath, "-c", `
import gc
import sys
import time
class MemsnapshotPayload:
    __slots__ = ("payload",)
objects = [MemsnapshotPayload() for _ in range(20000)]
for obj in objects:
    obj.payload = list(range(64))
gc.collect()
print("ready", sys.getsizeof(objects[0]), flush=True)
time.sleep(60)
`)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopFixture()
		_ = command.Wait()
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "ready ") {
		t.Fatal("CPython fixture did not acknowledge readiness")
	}

	objectBytes, err := strconv.ParseUint(strings.TrimPrefix(scanner.Text(), "ready "), 10, 64)
	if err != nil || objectBytes == 0 {
		t.Fatalf("invalid fixture object size: %q", scanner.Text())
	}
	process, err := memsnapshot.ReadProcessInstanceID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	snapshotCtx, cancelSnapshot := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelSnapshot()
	result, err := collector.Snapshot(snapshotCtx, process, collector.Options{
		MaxMemoryObjectEntries: 10, SnapshotTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Process != process || result.Language != memsnapshot.LanguagePython ||
		result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
		t.Fatalf("live Python collector result = %+v", result)
	}
	snapshot := result.Snapshot
	// The live test is optional because distro Python builds do not expose a
	// uniform discovery ABI. In particular, some builds export _PyRuntime but
	// expose neither Py_Version nor a versioned libpython mapping. The provider
	// classifies that environment as unsupported; skip it without hiding actual
	// capture failures, which use SnapshotStatusFailed.
	if snapshot.Status == memsnapshot.SnapshotStatusUnavailable &&
		strings.HasPrefix(snapshot.StatusReason, "CPython runtime is unsupported:") {
		skipMissingRuntime(t, "live CPython snapshot is unsupported in this environment: %s",
			snapshot.StatusReason)
	}
	if snapshot.Status != memsnapshot.SnapshotStatusComplete && snapshot.Status != memsnapshot.SnapshotStatusPartial {
		t.Fatalf("live CPython snapshot status = %q, reason = %q",
			snapshot.Status, snapshot.StatusReason)
	}
	if snapshot.RuntimeVersion == "" || len(snapshot.Entries) == 0 {
		t.Fatalf("live CPython snapshot has no runtime data: %+v", snapshot)
	}
	for index := range snapshot.Entries {
		if kind := snapshot.Entries[index].Kind; kind != "gc_tracked_object_type" {
			t.Fatalf("live CPython entry %d kind = %q, want gc_tracked_object_type", index, kind)
		}
	}

	foundPayload := false
	for _, entry := range snapshot.Entries {
		if strings.Contains(entry.Name, "MemsnapshotPayload") {
			foundPayload = true
			if entry.Objects != 20000 || entry.Bytes != 20000*objectBytes || entry.AverageBytes != float64(objectBytes) {
				t.Fatalf("incorrect Python retained count/shallow byte statistics: %+v", entry)
			}
		}
	}
	if !foundPayload {
		t.Fatalf("Python fixture type absent: %+v", snapshot)
	}

	// Exercise the provider boundary directly so collector trimming cannot hide
	// a provider that ignores MaxMemoryObjectEntries.
	bounded, err := python.New().Snapshot(snapshotCtx, memsnapshot.Request{
		Process: process, MaxMemoryObjectEntries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded.Entries) != 1 || !bounded.OutputTruncated {
		t.Fatalf("live CPython provider did not apply MaxMemoryObjectEntries: %+v", bounded)
	}
	liveRuntimePressureAndKill(t, process, command, stopFixture)
}

func requireSupportedCPython(t *testing.T, pythonPath string) {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), pythonPath, "-c", `
import ctypes
import sys

try:
    getattr(ctypes.pythonapi, "_PyRuntime")
    exports_runtime = 1
except AttributeError:
    exports_runtime = 0

print(sys.implementation.name, sys.version_info.major, sys.version_info.minor,
      sys.version_info.micro, exports_runtime)
`).CombinedOutput()
	if err != nil {
		skipMissingRuntime(t, "cannot inspect python3 runtime: %v: %s", err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 5 {
		skipMissingRuntime(t, "cannot parse python3 runtime information: %q", output)
	}
	major, majorErr := strconv.Atoi(fields[1])
	minor, minorErr := strconv.Atoi(fields[2])
	micro, microErr := strconv.Atoi(fields[3])
	if majorErr != nil || minorErr != nil || microErr != nil {
		skipMissingRuntime(t, "cannot parse python3 version: %q", output)
	}
	if fields[0] != "cpython" {
		skipMissingRuntime(t, "requires CPython, found %s %d.%d.%d",
			fields[0], major, minor, micro)
	}
	if major != 3 || minor < 8 || minor > 14 {
		skipMissingRuntime(t, "requires CPython 3.8-3.14, found %d.%d.%d",
			major, minor, micro)
	}
	if fields[4] != "1" {
		skipMissingRuntime(t, "CPython %d.%d.%d does not export _PyRuntime",
			major, minor, micro)
	}
}

func skipMissingRuntime(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("MEMSNAP_REQUIRE_LIVE") == "1" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func TestSnapshotLiveJavaProcess(t *testing.T) {
	snapshotLiveJavaProcess(t)
}

func snapshotLiveJavaProcess(t *testing.T) {
	t.Helper()
	javaPath, javaErr := exec.LookPath("java")
	javacPath, javacErr := exec.LookPath("javac")
	if javaErr != nil || javacErr != nil {
		skipMissingRuntime(t, "java and javac are required")
	}
	requireSupportedHotSpot(t, javaPath, javacPath)
	directory := t.TempDir()
	source := filepath.Join(directory, "HeapFixture.java")
	code := `public class HeapFixture {
 static Object[] retained = new Object[200000];
 static Payload[][] large = new Payload[6][];
 static class Payload {long a,b,c,d,e,f,g,h;}
 public static void main(String[] args) throws Exception {
  for (int i=0;i<retained.length;i++) retained[i]=new Payload();
  for (int i=0;i<large.length;i++) large[i]=new Payload[300000];
  System.gc();
  System.out.println("ready");
  Thread.sleep(60000);
 }
}`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureCtx, stopFixture := context.WithTimeout(t.Context(), time.Minute)
	defer stopFixture()
	if output, err := exec.CommandContext(fixtureCtx, javacPath, "-source", "8", "-target", "8", source).CombinedOutput(); err != nil {
		t.Fatalf("compile Java fixture: %v: %s", err, output)
	}
	args := []string{"-XX:+UseG1GC", "-XX:G1HeapRegionSize=1m", "-XX:+UseCompressedOops", "-XX:+UseCompressedClassPointers", "-Xms32m", "-Xmx64m"}
	if exec.CommandContext(fixtureCtx, javaPath, "-XX:-UseCompactObjectHeaders", "-version").Run() == nil {
		args = append(args, "-XX:-UseCompactObjectHeaders")
	}
	args = append(args, "-cp", directory, "HeapFixture")
	command := exec.CommandContext(fixtureCtx, javaPath, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopFixture(); _ = command.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("Java fixture did not acknowledge readiness")
	}
	identity, err := memsnapshot.ReadProcessInstanceID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	snapshotCtx, cancelSnapshot := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelSnapshot()
	result, err := collector.Snapshot(snapshotCtx, identity, collector.Options{
		MaxMemoryObjectEntries: 10, SnapshotTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Process != identity || result.Language != memsnapshot.LanguageJava ||
		result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
		t.Fatalf("live Java collector result = %+v", result)
	}
	snapshot := result.Snapshot
	if snapshot.Status != memsnapshot.SnapshotStatusComplete && snapshot.Status != memsnapshot.SnapshotStatusPartial {
		t.Fatalf("live Java snapshot status = %q, reason = %q", snapshot.Status, snapshot.StatusReason)
	}
	if snapshot.RuntimeVersion == "" || len(snapshot.Entries) == 0 {
		t.Fatalf("live Java snapshot has no runtime data: %+v", snapshot)
	}
	if snapshot.Status != memsnapshot.SnapshotStatusPartial || !strings.Contains(snapshot.StatusReason, "concurrently") {
		t.Fatalf("external Java scan must report partial concurrent data: %+v", snapshot)
	}
	// External G1 scanning is concurrent: object byte size is exact, census is partial.
	found := false
	for _, entry := range snapshot.Entries {
		if entry.Name == "HeapFixture$Payload" {
			if entry.Objects == 0 || entry.Objects > 200000 || entry.Bytes != entry.Objects*80 || entry.AverageBytes != 80 {
				t.Fatalf("incorrect Java retained count/object byte size: %+v", entry)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("retained Java payloads are missing: %+v", snapshot)
	}

	t.Run("exact humongous arrays", func(t *testing.T) {
		// This class has no ordinary one-dimensional arrays. Every instance is
		// humongous and counted directly, independently of ordinary sampling.
		found := false
		for _, entry := range snapshot.Entries {
			if entry.Name == "HeapFixture$Payload[]" {
				t.Logf("array aggregate: %+v", entry)
				if entry.Objects == 6 && entry.Bytes == 6*(300000*4+16) && entry.AverageBytes == 300000*4+16 {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("exact retained humongous arrays missing: %+v", snapshot)
		}
	})
	liveRuntimePressure(t, identity)
	t.Run("snapshot timeout", func(t *testing.T) {
		result, err := collector.Snapshot(t.Context(), identity, collector.Options{
			MaxMemoryObjectEntries: 10, SnapshotTimeout: time.Nanosecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Snapshot.Status != memsnapshot.SnapshotStatusFailed || len(result.Snapshot.Entries) != 0 ||
			!strings.Contains(result.Snapshot.StatusReason, context.DeadlineExceeded.Error()) {
			t.Fatalf("timed-out snapshot = %+v", result.Snapshot)
		}
		if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
			t.Fatalf("timed-out snapshot lost process memory: %+v", result)
		}
	})
	liveRuntimeKill(t, identity, command, stopFixture, true)
	t.Run("exit before detection", func(t *testing.T) {
		stopFixture()
		_ = command.Wait()
		result, err := collector.Snapshot(t.Context(), identity, collector.Options{})
		if result != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("exited process snapshot = %+v, %v; want no result and missing process error", result, err)
		}
		if !strings.Contains(err.Error(), "detect process runtime:") {
			t.Fatalf("missing detection error context: %v", err)
		}
	})
}

// Native processes retain process memory without a runtime snapshot provider.
func TestSnapshotLiveCCppProcess(t *testing.T) {
	for _, tc := range []struct{ compiler, suffix, language string }{
		{"gcc", "c", "c"}, {"g++", "cc", "cpp"},
	} {
		for _, stripped := range []bool{false, true} {
			name := tc.language
			if stripped {
				name += "/stripped"
			}
			t.Run(name, func(t *testing.T) {
				snapshotLiveCCppProcess(t, tc.compiler, tc.suffix, tc.language, stripped)
			})
		}
	}
}

func snapshotLiveCCppProcess(t *testing.T, compilerName, suffix, language string, stripped bool) {
	t.Helper()
	compiler, err := exec.LookPath(compilerName)
	if err != nil {
		skipMissingRuntime(t, "%s is required", compilerName)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "fixture."+suffix)
	code := `#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
int main(void) {
 volatile unsigned char *p = (volatile unsigned char *)malloc(8*1024*1024);
 if (!p) return 1;
 for (int i=0;i<8*1024*1024;i+=4096) p[i]=1;
 puts("ready"); fflush(stdout); sleep(60);
 free((void *)p); return 0;
}
`
	if language == "cpp" {
		code = "#include <iostream>\n" + code
		code = strings.Replace(code, `puts("ready"); fflush(stdout);`, `std::cout << "ready" << std::endl;`, 1)
	}
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fixture")
	args := []string{"-g", "-O0", "-o", bin, source}
	if stripped {
		args = append(args, "-s")
	}
	if raw, err := exec.CommandContext(t.Context(), compiler, args...).CombinedOutput(); err != nil {
		t.Fatalf("compile native fixture: %v: %s", err, raw)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, bin)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = command.Wait() })
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() || reader.Text() != "ready" {
		t.Fatal("native fixture is not ready")
	}
	identity, err := memsnapshot.ReadProcessInstanceID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	result, err := collector.Snapshot(t.Context(), identity, collector.Options{MaxMemoryObjectEntries: 10, SnapshotTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	want := language
	if language == "c" {
		want = "native"
	}
	if string(result.Language) != want {
		t.Fatalf("language=%s want=%s", result.Language, want)
	}
	if result.Snapshot.Status != memsnapshot.SnapshotStatusUnavailable || len(result.Snapshot.Entries) != 0 {
		t.Fatalf("unsupported runtime snapshot=%+v", result.Snapshot)
	}
	if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil || *result.ProcessMemory.RSSBytes < 8<<20 {
		t.Fatalf("missing touched allocation in process memory: %+v", result.ProcessMemory)
	}
	if err := memsnapshot.ValidateProcessInstanceID(memsnapshot.ProcessInstanceID{TGID: identity.TGID, StartTimeTicks: identity.StartTimeTicks + 1}); err == nil {
		t.Fatal("PID reuse accepted")
	}

	liveRuntimePressure(t, identity)
	t.Run("snapshot timeout", func(t *testing.T) {
		result, err := collector.Snapshot(t.Context(), identity, collector.Options{
			MaxMemoryObjectEntries: 10, SnapshotTimeout: time.Nanosecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Snapshot.Status != memsnapshot.SnapshotStatusFailed || len(result.Snapshot.Entries) != 0 ||
			!strings.Contains(result.Snapshot.StatusReason, context.DeadlineExceeded.Error()) {
			t.Fatalf("timed-out snapshot = %+v", result.Snapshot)
		}
		if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
			t.Fatalf("timed-out snapshot lost process memory: %+v", result)
		}
	})
	liveRuntimeKill(t, identity, command, cancel, false)
	t.Run("exit before detection", func(t *testing.T) {
		cancel()
		_ = command.Wait()
		result, err := collector.Snapshot(t.Context(), identity, collector.Options{})
		if result != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("exited process snapshot = %+v, %v; want no result and missing process error", result, err)
		}
		if !strings.Contains(err.Error(), "detect process runtime:") {
			t.Fatalf("missing detection error context: %v", err)
		}
	})
}

type acceptanceTarget struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	identity memsnapshot.ProcessInstanceID
	stopped  bool
}

func startAcceptanceTarget(t *testing.T) *acceptanceTarget {
	t.Helper()
	return startAcceptanceVariant(t, "")
}

func startAcceptanceVariant(t *testing.T, variant string) *acceptanceTarget {
	t.Helper()
	binary := os.Getenv("MEMSNAP_ACCEPTANCE_FIXTURE")
	if binary == "" {
		t.Fatal("run integration/test_basic_memsnapshot_provider_live.sh")
	}
	cmd := exec.CommandContext(t.Context(), binary, "1")
	cmd.Env = append(os.Environ(), "MEMSNAPSHOT_VARIANT="+variant)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	target := &acceptanceTarget{cmd: cmd, input: input}
	t.Cleanup(func() { target.stop(); _ = input.Close() })
	if _, err = input.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(output); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("fixture did not acknowledge ready")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("fixture readiness timed out")
	}
	target.identity, err = memsnapshot.ReadProcessInstanceID(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func (target *acceptanceTarget) stop() {
	if !target.stopped {
		_ = target.cmd.Process.Kill()
		_ = target.cmd.Wait()
		target.stopped = true
	}
}

func acceptanceCapture(t *testing.T, target *acceptanceTarget, options collector.Options) *collector.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result, err := collector.Snapshot(ctx, target.identity, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Process != target.identity || result.Language != memsnapshot.LanguageGo || result.Snapshot == nil || result.Snapshot.Status != memsnapshot.SnapshotStatusComplete {
		t.Fatalf("unexpected capture: %+v", result)
	}
	if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil || *result.ProcessMemory.RSSBytes < 22<<20 {
		t.Fatalf("missing byte-valued process memory: %+v", result.ProcessMemory)
	}
	return result
}

func TestMemsnapshotAcceptanceDataAndOptions(t *testing.T) {
	target := startAcceptanceTarget(t)
	for _, k := range []int{0, 1, memsnapshot.MaxMemoryObjectEntries} {
		t.Run(fmt.Sprintf("topk=%d", k), func(t *testing.T) {
			result := acceptanceCapture(t, target, collector.Options{MaxMemoryObjectEntries: k})
			snapshot := result.Snapshot
			limit := k
			if limit == 0 {
				limit = 10
			}
			if len(snapshot.Entries) == 0 || len(snapshot.Entries) > limit {
				t.Fatalf("entry count: %+v", snapshot)
			}
			if !strings.HasPrefix(snapshot.RuntimeVersion, "go1.") {
				t.Fatalf("runtime version: %q", snapshot.RuntimeVersion)
			}
			for i, entry := range snapshot.Entries {
				if entry.Kind == "" || entry.Name == "" || entry.Objects == 0 || entry.AverageBytes != float64(entry.Bytes)/float64(entry.Objects) {
					t.Fatalf("invalid entry: %+v", entry)
				}
				if i > 0 && snapshot.Entries[i-1].Bytes < entry.Bytes {
					t.Fatal("entries not sorted by bytes")
				}
				if strings.Contains(strings.Join(entry.Stack, "\n"), "main.allocateReleased") {
					t.Fatal("released allocation retained")
				}
			}
			primary := snapshot.Entries[0]
			if primary.Name != "main.allocateBlock" || primary.Bytes != 16777216 || primary.Objects != 6 || (len(primary.Stack) < 3 || !strings.HasPrefix(primary.Stack[0], "main.allocateBlock, ") || !strings.HasPrefix(primary.Stack[1], "main.allocatePrimary, ") || !strings.HasPrefix(primary.Stack[2], "main.main, ")) {
				t.Fatalf("incorrect primary byte/object/stack statistics: %+v", primary)
			}
			if k == 1 && !snapshot.OutputTruncated {
				t.Fatal("MaxMemoryObjectEntries truncation not reported")
			}
			if k != 1 {
				secondary := snapshot.Entries[1]
				if secondary.Bytes != 6291456 || secondary.Objects != 3 {
					t.Fatalf("incorrect secondary: %+v", secondary)
				}
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) > memsnapshot.MaxSnapshotBytes {
				t.Fatal("encoded snapshot exceeds bound")
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"status", "runtime_version", "duration_ms", "entries"} {
				if _, ok := fields[key]; !ok {
					t.Fatalf("missing JSON field %s", key)
				}
			}
			if dir := os.Getenv("MEMSNAP_ACCEPTANCE_ARTIFACTS"); dir != "" {
				if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("topk-%d.json", k)), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, options := range []collector.Options{{MaxMemoryObjectEntries: -1}, {MaxMemoryObjectEntries: memsnapshot.MaxMemoryObjectEntries + 1}, {SnapshotTimeout: -time.Second}} {
		result, err := collector.Snapshot(t.Context(), target.identity, options)
		if err == nil || result != nil {
			t.Fatalf("invalid options accepted: %+v", options)
		}
	}
	acceptanceCapture(t, target, collector.Options{})
}

func TestMemsnapshotAcceptanceFailureRecovery(t *testing.T) {
	target := startAcceptanceTarget(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := collector.Snapshot(ctx, target.identity, collector.Options{}); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation result=%+v error=%v", result, err)
	}
	result, err := collector.Snapshot(t.Context(), target.identity, collector.Options{SnapshotTimeout: time.Nanosecond})
	if err != nil || result == nil || result.Snapshot.Status != memsnapshot.SnapshotStatusFailed || !strings.Contains(result.Snapshot.StatusReason, "deadline exceeded") || len(result.Snapshot.Entries) != 0 {
		t.Fatalf("timeout result=%+v error=%v", result, err)
	}
	acceptanceCapture(t, target, collector.Options{})
	target.stop()
	if result, err = collector.Snapshot(t.Context(), target.identity, collector.Options{}); result != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SIGKILL result=%+v error=%v", result, err)
	}
	acceptanceCapture(t, startAcceptanceTarget(t), collector.Options{})
}

func TestMemsnapshotAcceptanceConcurrent(t *testing.T) {
	targets := []*acceptanceTarget{startAcceptanceTarget(t), startAcceptanceVariant(t, "small"), startAcceptanceTarget(t)}
	start := make(chan struct{})
	outcomes := make(chan error, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(target *acceptanceTarget) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			result, err := collector.Snapshot(ctx, target.identity, collector.Options{WaitForCapture: true, SnapshotTimeout: 10 * time.Second})
			if err == nil && (result.Process != target.identity || result.Snapshot.Status != memsnapshot.SnapshotStatusComplete || len(result.Snapshot.Entries) == 0) {
				err = fmt.Errorf("cross-target or incomplete result: %+v", result)
			}
			if err == nil {
				want := uint64(16777216)
				if target == targets[1] {
					want = 6 << 20
				}
				found := false
				for _, entry := range result.Snapshot.Entries {
					if len(entry.Stack) > 1 && strings.HasPrefix(entry.Stack[1], "main.allocatePrimary, ") {
						found = true
						if entry.Bytes != want || entry.Objects != 6 {
							err = fmt.Errorf("cross-target allocation: got %+v want bytes %d", entry, want)
						}
					}
				}
				if !found {
					err = fmt.Errorf("primary allocation absent for PID %d", target.identity.TGID)
				}
			}
			outcomes <- err
		}(target)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil {
			t.Fatal(err)
		}
	}
	acceptanceCapture(t, targets[0], collector.Options{})
}

func acceptanceResources(t *testing.T) (int, uint64) {
	t.Helper()
	runtime.GC()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	var rss uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "VmRSS:" {
			rss, err = strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return len(fds), rss * 1024
}

func TestMemsnapshotAcceptancePressure(t *testing.T) {
	target := startAcceptanceTarget(t)
	acceptanceCapture(t, target, collector.Options{})
	fdBefore, rssBefore := acceptanceResources(t)
	threadsBefore := acceptanceThreads(t)
	rounds := 12
	if value := os.Getenv("MEMSNAP_ACCEPTANCE_ROUNDS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 3 || parsed > 100 {
			t.Fatal("MEMSNAP_ACCEPTANCE_ROUNDS must be in [3,100]")
		}
		rounds = parsed
	}
	for i := 0; i < rounds; i++ {
		acceptanceCapture(t, target, collector.Options{})
		fd, rss := acceptanceResources(t)
		t.Logf("round=%d fds=%d rss_bytes=%d threads=%d", i, fd, rss, acceptanceThreads(t))
	}
	fdAfter, rssAfter := acceptanceResources(t)
	if threads := acceptanceThreads(t); threads > threadsBefore+16 {
		t.Fatalf("thread growth: %d -> %d", threadsBefore, threads)
	}
	if fdAfter > fdBefore+2 {
		t.Fatalf("FD growth: %d -> %d", fdBefore, fdAfter)
	}
	// This is a bounded regression guard, not proof that every RSS increase is a leak.
	if rssAfter > rssBefore+(128<<20) {
		t.Fatalf("RSS grew beyond 128 MiB: %d -> %d", rssBefore, rssAfter)
	}
	acceptanceCapture(t, target, collector.Options{})
}

func TestMemsnapshotAcceptanceConfigFile(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"minimum", "ThresholdPercent = 1\nIntervalTracing = 1\nRunTracingToolTimeout = 1\nMaxMemoryObjectEntries = 1", ""},
		{"maximum", "ThresholdPercent = 100\nMaxMemoryObjectEntries = 100", ""},
		{"threshold-zero", "ThresholdPercent = 0", "threshold percent"},
		{"interval-zero", "IntervalTracing = 0", "tracing interval seconds"},
		{"timeout-zero", "RunTracingToolTimeout = 0", "tracing tool timeout seconds"},
		{"entries-zero", "MaxMemoryObjectEntries = 0", "maximum memory object entries"},
		{"threshold-negative", "ThresholdPercent = -1", "threshold percent"},
		{"threshold-over", "ThresholdPercent = 101", "threshold percent"},
		{"interval-negative", "IntervalTracing = -1", "tracing interval seconds"},
		{"timeout-negative", "RunTracingToolTimeout = -1", "tracing tool timeout seconds"},
		{"timeout-overflow", "RunTracingToolTimeout = 9223372036854775807", "overflows"},
		{"entries-negative", "MaxMemoryObjectEntries = -1", "maximum memory object entries"},
		{"entries-over", "MaxMemoryObjectEntries = 101", "maximum memory object entries"},
		{"wrong-type", "ThresholdPercent = \"invalid\"", "loading config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bamai.conf")
			body := "[HTTPServer.Auth]\nBearerToken = \"acceptance-token\"\n[AutoTracing.MemoryThresholdSnapshot]\n" + tc.body + "\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := bamaiconfig.Load(path)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestMemsnapshotAcceptanceChurn(t *testing.T) {
	t.Setenv("MEMSNAPSHOT_CHURN", "1")
	target := startAcceptanceTarget(t)
	until := time.Now().Add(3 * time.Second)
	for i := 0; i < 12 || time.Now().Before(until); i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		result, err := collector.Snapshot(ctx, target.identity, collector.Options{SnapshotTimeout: 5 * time.Second})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if result.Process != target.identity || result.Language != memsnapshot.LanguageGo || result.Snapshot == nil || (result.Snapshot.Status != memsnapshot.SnapshotStatusComplete && result.Snapshot.Status != memsnapshot.SnapshotStatusPartial) {
			t.Fatalf("invalid churn result: %+v", result)
		}
		if len(result.Snapshot.Entries) > 10 {
			t.Fatal("churn output exceeds MaxMemoryObjectEntries")
		}
		raw, err := json.Marshal(result.Snapshot)
		if err != nil || len(raw) > memsnapshot.MaxSnapshotBytes {
			t.Fatalf("invalid churn JSON size=%d error=%v", len(raw), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	target.stop()
	t.Setenv("MEMSNAPSHOT_CHURN", "")
	acceptanceCapture(t, startAcceptanceTarget(t), collector.Options{})
}

func acceptanceThreads(t *testing.T) int {
	t.Helper()
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Fatal(err)
	}
	return len(tasks)
}

// Every provider owns different remote readers; exercise their cleanup independently.
func liveRuntimePressure(t *testing.T, identity memsnapshot.ProcessInstanceID) {
	t.Helper()
	capture := func() {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		result, err := collector.Snapshot(ctx, identity, collector.Options{MaxMemoryObjectEntries: 10, SnapshotTimeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if result == nil || result.Process != identity || result.Snapshot == nil {
			t.Fatalf("invalid repeated result: %+v", result)
		}
		snap := result.Snapshot
		if snap.Status == memsnapshot.SnapshotStatusUnavailable && result.Language != memsnapshot.Language("native") && result.Language != memsnapshot.Language("cpp") {
			t.Fatalf("provider became unavailable: %+v", snap)
		}
		if snap.Status != memsnapshot.SnapshotStatusComplete && snap.Status != memsnapshot.SnapshotStatusPartial && snap.Status != memsnapshot.SnapshotStatusUnavailable {
			t.Fatalf("repeated capture failed: %+v", snap)
		}
		if len(snap.Entries) > 10 {
			t.Fatal("MaxMemoryObjectEntries exceeded")
		}
		for i, e := range snap.Entries {
			if e.Name == "" || e.Kind == "" || e.Objects == 0 || e.AverageBytes != float64(e.Bytes)/float64(e.Objects) {
				t.Fatalf("invalid byte-valued entry: %+v", e)
			}
			if i > 0 && snap.Entries[i-1].Bytes < e.Bytes {
				t.Fatal("entries not sorted by bytes")
			}
		}
		raw, err := json.Marshal(snap)
		if err != nil || len(raw) > memsnapshot.MaxSnapshotBytes {
			t.Fatalf("invalid JSON size=%d: %v", len(raw), err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"status", "duration_ms"} {
			if _, ok := fields[key]; !ok {
				t.Fatalf("missing JSON field %s", key)
			}
		}
	}
	capture()
	t.Run("timeout recovery", func(t *testing.T) {
		result, err := collector.Snapshot(t.Context(), identity, collector.Options{SnapshotTimeout: time.Nanosecond})
		if err != nil || result == nil || result.Snapshot == nil || result.Snapshot.Status != memsnapshot.SnapshotStatusFailed || !strings.Contains(result.Snapshot.StatusReason, context.DeadlineExceeded.Error()) {
			t.Fatalf("invalid timeout result=%+v error=%v", result, err)
		}
		capture()
	})
	t.Run("queued concurrency", func(t *testing.T) {
		done := make(chan error, 3)
		for i := 0; i < 3; i++ {
			go func() {
				ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
				defer cancel()
				result, err := collector.Snapshot(ctx, identity, collector.Options{MaxMemoryObjectEntries: 10, SnapshotTimeout: 10 * time.Second, WaitForCapture: true})
				if err == nil && (result == nil || result.Process != identity || result.Snapshot == nil || result.Snapshot.Status == memsnapshot.SnapshotStatusFailed) {
					err = fmt.Errorf("invalid concurrent capture: %+v", result)
				}
				done <- err
			}()
		}
		for i := 0; i < 3; i++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	})
	fds, rss := acceptanceResources(t)
	threads := acceptanceThreads(t)
	for i := 0; i < 12; i++ {
		capture()
	}
	afterFD, afterRSS := acceptanceResources(t)
	if afterFD > fds+2 || afterRSS > rss+(128<<20) || acceptanceThreads(t) > threads+16 {
		t.Fatalf("resource growth: fds %d -> %d, rss %d -> %d", fds, afterFD, rss, afterRSS)
	}
	capture()
}

func liveRuntimePressureAndKill(t *testing.T, identity memsnapshot.ProcessInstanceID, cmd *exec.Cmd, stop context.CancelFunc) {
	t.Helper()
	liveRuntimePressure(t, identity)
	liveRuntimeKill(t, identity, cmd, stop, true)
}

func liveRuntimeKill(t *testing.T, identity memsnapshot.ProcessInstanceID, cmd *exec.Cmd, stop context.CancelFunc, hasProvider bool) {
	t.Helper()
	if hasProvider && os.Getenv("MEMSNAP_READ_BARRIER_ENABLED") == "1" {
		t.Run("kill after first remote read", func(t *testing.T) { liveRuntimeReadInterruption(t, identity, cmd, stop) })
		return
	}
	type outcome struct {
		result *collector.Result
		err    error
	}
	done := make(chan outcome, 1)
	started := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		close(started)
		r, e := collector.Snapshot(ctx, identity, collector.Options{SnapshotTimeout: 5 * time.Second})
		done <- outcome{r, e}
	}()
	<-started
	// The race deliberately permits capture to finish before SIGKILL reaches the target.
	_ = cmd.Process.Kill()
	stop()
	_ = cmd.Wait()
	select {
	case got := <-done:
		if got.err == nil {
			if got.result == nil || got.result.Process != identity || got.result.Snapshot == nil {
				t.Fatalf("misattributed kill result: %+v", got.result)
			}
			if got.result.Snapshot.Status == memsnapshot.SnapshotStatusFailed && got.result.Snapshot.StatusReason == "" {
				t.Fatal("failed snapshot has no reason")
			}
		} else if !errors.Is(got.err, os.ErrNotExist) && !strings.Contains(got.err.Error(), "process") {
			t.Fatalf("unexpected kill error: %v", got.err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("capture hung after target SIGKILL")
	}
	// A new process checks that the killed provider released the shared capture slot.
	acceptanceCapture(t, startAcceptanceTarget(t), collector.Options{})
}

func TestMemsnapshotAcceptanceReadInterruption(t *testing.T) {
	if os.Getenv("MEMSNAP_READ_BARRIER_ENABLED") != "1" {
		t.Skip("run the unified shell to enable the remote-read overlay")
	}
	target := startAcceptanceTarget(t)
	liveRuntimeReadInterruption(t, target.identity, target.cmd, func() {})
	target.stopped = true
}

func liveRuntimeReadInterruption(t *testing.T, identity memsnapshot.ProcessInstanceID, cmd *exec.Cmd, stop context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MEMSNAP_READ_BARRIER_DIR", dir)
	t.Setenv("MEMSNAP_READ_BARRIER_PID", strconv.Itoa(identity.TGID))
	release := func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600) }
	defer release()
	type outcome struct {
		result *collector.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		r, e := collector.Snapshot(ctx, identity, collector.Options{SnapshotTimeout: 12 * time.Second})
		done <- outcome{r, e}
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case got := <-done:
			t.Fatalf("capture finished before read barrier: result=%+v error=%v", got.result, got.err)
		case <-deadline.C:
			t.Fatal("provider did not reach second remote read")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(dir, "reached")); err == nil {
				waiting = false
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "first-read")); err != nil {
		t.Fatal("barrier reached without a successful remote read")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	stop()
	release()
	select {
	case got := <-done:
		if _, err := os.Stat(filepath.Join(dir, "target-exited")); err != nil {
			t.Fatalf("no real ESRCH from post-kill remote read: %v", err)
		}
		if got.err == nil {
			if got.result == nil || got.result.Process != identity || got.result.Snapshot == nil || got.result.Snapshot.Status == memsnapshot.SnapshotStatusComplete || got.result.Snapshot.StatusReason == "" {
				t.Fatalf("invalid interrupted snapshot: %+v", got.result)
			}
		} else if !errors.Is(got.err, os.ErrNotExist) && !strings.Contains(got.err.Error(), "process") {
			t.Fatalf("unexpected interruption error: %v", got.err)
		}
	case <-time.After(16 * time.Second):
		t.Fatal("interrupted capture did not return")
	}
	t.Setenv("MEMSNAP_READ_BARRIER_DIR", "")
	acceptanceCapture(t, startAcceptanceTarget(t), collector.Options{})
}

// Exercise executable names and library evidence with a real interpreter,
// including legacy CPython versions whose snapshot provider is unavailable.
func TestMemsnapshotAcceptanceCPythonCompatibility(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		skipMissingRuntime(t, "python3 is not installed")
	}
	pythonPath, err = filepath.EvalSymlinks(pythonPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pythonPath)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := exec.Command(pythonPath, "-c", "import sys; print(sys.prefix)").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"platform-python3.6", "python3.6m", "renamed-interpreter"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, raw, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			t.Cleanup(cancel)
			cmd := exec.CommandContext(ctx, path, "-c", "import time; print('ready', flush=True); time.sleep(30)")
			cmd.Env = append(os.Environ(), "PYTHONHOME="+strings.TrimSpace(string(prefix)))
			cmd.Stderr = os.Stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cancel(); _ = cmd.Wait() })
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "ready" {
				t.Fatal("interpreter did not become ready")
			}
			want := memsnapshot.LanguagePython
			if name == "renamed-interpreter" {
				maps, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", cmd.Process.Pid))
				if err != nil {
					t.Fatal(err)
				}
				// A statically linked interpreter loses its only Python evidence
				// when renamed; this case must retain the native fallback.
				if !strings.Contains(string(maps), "libpython3") {
					want = memsnapshot.LanguageNative
				}
			}
			language, err := memsnapshot.DetectLanguage(cmd.Process.Pid)
			if err != nil || language != want {
				t.Fatalf("interpreter %s: language=%s error=%v", name, language, err)
			}
		})
	}
}
