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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnap"
	goprovider "github.com/ccfos/huatuo/internal/memsnap/providers/golang"
	javaprovider "github.com/ccfos/huatuo/internal/memsnap/providers/java"
	pythonprovider "github.com/ccfos/huatuo/internal/memsnap/providers/python"
)

func TestCaptureLiveGoProcess(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "heap.go")
	if err := os.WriteFile(source, []byte(`
package main

import (
    "fmt"
    "runtime"
    "time"
)

func main() {
    // Sample every allocation so this small fixture reliably has heap entries.
    runtime.MemProfileRate = 1
    payloads := make([][]byte, 8)
    for i := range payloads {
        payloads[i] = make([]byte, 128<<10)
        payloads[i][0] = byte(i)
    }
    runtime.GC()
    runtime.GC()
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
		"-o", executable, source).CombinedOutput(); err != nil {
		t.Fatalf("compile Go fixture: %v: %s", err, output)
	}

	fixtureCtx, stopFixture := context.WithTimeout(t.Context(), time.Minute)
	defer stopFixture()
	command := exec.CommandContext(fixtureCtx, executable)
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

	identity, err := memsnap.ReadIdentity(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	captureCtx, cancelCapture := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelCapture()
	snapshot, err := goprovider.NewProvider().Capture(captureCtx, memsnap.Request{
		Identity: identity, SamplingSeed: 1, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != memsnap.StatusComplete && snapshot.Status != memsnap.StatusPartial {
		t.Fatalf("live Go snapshot status = %q, reason = %q",
			snapshot.Status, snapshot.Reason)
	}
	if snapshot.RuntimeVersion == "" || len(snapshot.Entries) == 0 {
		t.Fatalf("live Go snapshot has no runtime data: %+v", snapshot)
	}
}

// TestCaptureLiveHotSpotProcess is an optional environment validation rather
// than a required CI gate. It exercises a real HotSpot process when a supported
// JDK is already available and otherwise skips without installing one.
func TestCaptureLiveHotSpotProcess(t *testing.T) {
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

    public static void main(String[] args) throws Exception {
        for (int i = 0; i < 200000; i++) {
            OBJECTS.add(new byte[128]);
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
	javaArgs = append(javaArgs, "-cp", directory, "HeapFixture")
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

	identity, err := memsnap.ReadIdentity(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	captureCtx, cancelCapture := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelCapture()
	snapshot, err := javaprovider.NewProvider().Capture(captureCtx, memsnap.Request{
		Identity: identity, SamplingSeed: 1, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != memsnap.StatusComplete && snapshot.Status != memsnap.StatusPartial {
		t.Fatalf("live HotSpot snapshot status = %q, reason = %q",
			snapshot.Status, snapshot.Reason)
	}
	if snapshot.RuntimeVersion == "" || len(snapshot.Entries) == 0 {
		t.Fatalf("live HotSpot snapshot has no runtime data: %+v", snapshot)
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

func TestCaptureLiveCPythonProcess(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		skipMissingRuntime(t, "python3 is not installed")
	}
	requireSupportedCPython(t, pythonPath)
	fixtureCtx, stopFixture := context.WithTimeout(t.Context(), time.Minute)
	defer stopFixture()
	command := exec.CommandContext(fixtureCtx, pythonPath, "-c", `
import gc
import time
objects = [{"payload": list(range(64))} for _ in range(20000)]
gc.collect()
print("ready", flush=True)
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
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("CPython fixture did not acknowledge readiness")
	}

	identity, err := memsnap.ReadIdentity(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	captureCtx, cancelCapture := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelCapture()
	snapshot, err := pythonprovider.NewProvider().Capture(captureCtx, memsnap.Request{
		Identity: identity, SamplingSeed: 1, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The live test is optional because distro Python builds do not expose a
	// uniform discovery ABI. In particular, some builds export _PyRuntime but
	// expose neither Py_Version nor a versioned libpython mapping. The provider
	// classifies that environment as unsupported; skip it without hiding actual
	// capture failures, which use StatusFailed.
	if snapshot.Status == memsnap.StatusUnavailable &&
		strings.HasPrefix(snapshot.Reason, "CPython runtime is unsupported:") {
		skipMissingRuntime(t, "live CPython snapshot is unsupported in this environment: %s",
			snapshot.Reason)
	}
	if snapshot.Status != memsnap.StatusComplete && snapshot.Status != memsnap.StatusPartial {
		t.Fatalf("live CPython snapshot status = %q, reason = %q",
			snapshot.Status, snapshot.Reason)
	}
	if snapshot.RuntimeVersion == "" || len(snapshot.Entries) == 0 {
		t.Fatalf("live CPython snapshot has no runtime data: %+v", snapshot)
	}
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
