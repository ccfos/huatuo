//go:build integration && linux

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

// Exercise memburst with real processes, collector reads and event storage.

package autotracing

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
)

func burstAcceptanceProcess(t *testing.T) (*processMemInfo, *exec.Cmd, io.WriteCloser) {
	t.Helper()
	bin := os.Getenv("MEMBURST_ACCEPTANCE_FIXTURE")
	if bin == "" {
		t.Fatal("run test_basic_autotracing_memburst.sh")
	}
	cmd := exec.Command(bin, "1")
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
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, err = io.WriteString(input, "a"); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(output)
		if s.Scan() {
			ready <- s.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		if line != "ready" {
			t.Fatalf("fixture: %q", line)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("fixture readiness timeout")
	}
	identity, err := memsnapshot.ReadProcessInstanceID(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return &processMemInfo{PID: int32(cmd.Process.Pid), ProcessName: "go-snapshot", MemSize: 23 << 20, identity: identity}, cmd, input
}

func assertBurstAcceptanceSnapshot(t *testing.T, record burstProcessSnapshot) {
	t.Helper()
	if record.Language != memsnapshot.LanguageGo || record.Snapshot == nil || record.Snapshot.Status != memsnapshot.SnapshotStatusComplete {
		t.Fatalf("Go snapshot: %+v", record)
	}
	if record.ProcessMemory == nil || record.ProcessMemory.RSSBytes == nil || *record.ProcessMemory.RSSBytes == 0 {
		t.Fatalf("missing process memory: %+v", record.ProcessMemory)
	}
	// Provider acceptance owns exact allocation counts and byte relationships.
	if record.Snapshot.RuntimeVersion == "" || len(record.Snapshot.Entries) == 0 || len(record.Snapshot.Entries) > 10 {
		t.Fatalf("missing or unbounded snapshot payload: %+v", record.Snapshot)
	}
}

func TestMemburstAcceptanceLiveSnapshots(t *testing.T) {
	process, cmd, _ := burstAcceptanceProcess(t)
	ranked := []*processMemInfo{process, process, process, process}
	for _, top := range []int{1, 3, 8} {
		t.Run(fmt.Sprintf("top=%d", top), func(t *testing.T) {
			records := collectBurstSnapshots(t.Context(), ranked, top)
			if len(records) != min(top, len(ranked)) {
				t.Fatalf("snapshot count=%d", len(records))
			}
			for _, r := range records {
				if r.PID != process.PID {
					t.Fatalf("wrong selected pid: %d", r.PID)
				}
				assertBurstAcceptanceSnapshot(t, r)
			}
		})
	}
	if got := collectBurstSnapshots(t.Context(), nil, 3); len(got) != 0 {
		t.Fatal("empty ranking produced records")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	records := collectBurstSnapshots(ctx, ranked, 3)
	for _, r := range records {
		if r.Snapshot == nil || r.Snapshot.StatusReason != context.Canceled.Error() {
			t.Fatalf("cancellation: %+v", r)
		}
	}
	stale := *process
	stale.identity.StartTimeTicks++
	if _, err := captureBurstProcess(t.Context(), &stale); err == nil {
		t.Fatal("PID reuse identity accepted")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := captureBurstProcess(t.Context(), process); err == nil {
		t.Fatal("exited process accepted")
	}
	recovered, _, _ := burstAcceptanceProcess(t)
	assertBurstAcceptanceSnapshot(t, collectBurstSnapshots(t.Context(), []*processMemInfo{recovered}, 1)[0])
}

func TestMemburstAcceptanceConfiguration(t *testing.T) {
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"defaults", "", true},
		{"minimum", "DeltaMemoryBurst=1\nDeltaAnonThreshold=0\nInterval=1\nIntervalTracing=1\nSlidingWindowLength=1\nDumpProcessMaxNum=1\nSnapshotProcessMaxNum=1", true},
		{"snapshot-above-ranking", "DumpProcessMaxNum=1\nSnapshotProcessMaxNum=3", true},
		{"delta-zero", "DeltaMemoryBurst=0", false},
		{"delta-negative", "DeltaMemoryBurst=-1", false},
		{"anon-low", "DeltaAnonThreshold=-1", false},
		{"anon-high", "DeltaAnonThreshold=101", false},
		{"anon-maximum", "DeltaAnonThreshold=100", true},
		{"interval-zero", "Interval=0", false},
		{"interval-negative", "Interval=-1", false},
		{"cooldown-zero", "IntervalTracing=0", false},
		{"window-zero", "SlidingWindowLength=0", false},
		{"ranking-zero", "DumpProcessMaxNum=0", false},
		{"snapshot-zero", "SnapshotProcessMaxNum=0", false},
		{"snapshot-negative", "SnapshotProcessMaxNum=-1", false},
		{"snapshot-overflow", "SnapshotProcessMaxNum=9223372036854775807", false},
		{"wrong-type", "SnapshotProcessMaxNum=\"bad\"", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bamai.conf")
			text := "[AutoTracing.MemoryBurst]\n" + tc.body + "\n"
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				AutoTracing struct{ MemoryBurst MemBurstConfig }
			}
			err := internalconfig.Load(path, &cfg)
			if err == nil {
				err = validateMemBurst(&cfg.AutoTracing.MemoryBurst)
				if tc.name == "defaults" && cfg.AutoTracing.MemoryBurst.SnapshotProcessMaxNum != 3 {
					t.Fatal("default snapshot top count is not 3")
				}
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestMemburstAcceptanceKillDuringRemoteRead(t *testing.T) {
	process, cmd, _ := burstAcceptanceProcess(t)
	dir := t.TempDir()
	t.Setenv("MEMSNAP_READ_BARRIER_DIR", dir)
	t.Setenv("MEMSNAP_READ_BARRIER_PID", strconv.Itoa(int(process.PID)))
	finished := make(chan error, 1)
	go func() { _, err := captureBurstProcess(t.Context(), process); finished <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "reached")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capture did not reach second remote read")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600) })
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("capture accepted a process killed during remote read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not recover from target SIGKILL")
	}
	if _, err := os.Stat(filepath.Join(dir, "target-exited")); err != nil {
		t.Fatalf("missing real post-exit remote-read failure: %v", err)
	}
	recovered, _, _ := burstAcceptanceProcess(t)
	assertBurstAcceptanceSnapshot(t, collectBurstSnapshots(t.Context(), []*processMemInfo{recovered}, 1)[0])
}

func TestMemburstAcceptanceAllocationPressure(t *testing.T) {
	t.Setenv("MEMSNAPSHOT_CHURN", "1")
	process, _, _ := burstAcceptanceProcess(t)
	deadline := time.Now().Add(3 * time.Second)
	for round := 0; round < 12 || time.Now().Before(deadline); round++ {
		snapshots := collectBurstSnapshots(t.Context(), []*processMemInfo{process}, 1)
		if len(snapshots) != 1 || snapshots[0].PID != process.PID || snapshots[0].Language != memsnapshot.LanguageGo || snapshots[0].Snapshot == nil {
			t.Fatalf("invalid pressure result: %+v", snapshots)
		}
		snapshot := snapshots[0].Snapshot
		if snapshot.Status != memsnapshot.SnapshotStatusComplete && snapshot.Status != memsnapshot.SnapshotStatusPartial {
			t.Fatalf("pressure snapshot status: %+v", snapshot)
		}
		raw, err := json.Marshal(snapshot)
		if err != nil || len(raw) > memsnapshot.MaxSnapshotBytes || len(snapshot.Entries) > 10 {
			t.Fatalf("pressure snapshot exceeded output bounds: bytes=%d err=%v", len(raw), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func burstAcceptanceBarrier(t *testing.T, process *processMemInfo) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MEMSNAP_READ_BARRIER_DIR", dir)
	t.Setenv("MEMSNAP_READ_BARRIER_PID", strconv.Itoa(int(process.PID)))
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600) })
	return dir
}

func burstAcceptanceWaitRead(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "reached")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("capture did not reach remote read")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMemburstAcceptanceFailureIsolation(t *testing.T) {
	first, _, _ := burstAcceptanceProcess(t)
	last, _, _ := burstAcceptanceProcess(t)
	dead, cmd, _ := burstAcceptanceProcess(t)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	stale := *dead
	stale.identity.StartTimeTicks++
	for _, bad := range []*processMemInfo{dead, &stale} {
		ranked := []*processMemInfo{first, bad, last}
		records := collectBurstSnapshots(t.Context(), ranked, 3)
		if len(records) != 3 || records[1].Snapshot.Status != memsnapshot.SnapshotStatusFailed || records[1].Snapshot.StatusReason == "" {
			t.Fatalf("failure isolation: %+v", records)
		}
		for _, i := range []int{0, 2} {
			if records[i].PID != ranked[i].PID {
				t.Fatal("snapshot changed ranking identity")
			}
			assertBurstAcceptanceSnapshot(t, records[i])
		}
	}
}

func TestMemburstAcceptanceBatchDeadline(t *testing.T) {
	first, _, _ := burstAcceptanceProcess(t)
	blocked, _, _ := burstAcceptanceProcess(t)
	last, _, _ := burstAcceptanceProcess(t)
	dir := burstAcceptanceBarrier(t, blocked)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan []burstProcessSnapshot, 1)
	started := time.Now()
	go func() { done <- collectBurstSnapshots(ctx, []*processMemInfo{first, blocked, last}, 3) }()
	burstAcceptanceWaitRead(t, dir)
	<-ctx.Done()
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case records := <-done:
		if len(records) != 3 {
			t.Fatalf("lost batch records: %+v", records)
		}
		assertBurstAcceptanceSnapshot(t, records[0])
		for _, i := range []int{1, 2} {
			if records[i].Snapshot.Status != memsnapshot.SnapshotStatusFailed {
				t.Fatalf("deadline accepted result: %+v", records[i])
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("batch did not stop after deadline")
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("batch deadline exceeded shutdown budget")
	}
	assertBurstAcceptanceSnapshot(t, collectBurstSnapshots(t.Context(), []*processMemInfo{last}, 1)[0])
}

func burstAcceptanceStore(t *testing.T, dir string) {
	t.Helper()
	store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{LocalFile: &tracingstore.LocalFileConfig{Path: dir, RotationSizeMiB: 16, MaxRotatedFiles: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if err = tracing.EnableDocumentWriter(store, document.New("integration")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tracing.DisableDocumentWriter()
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

func TestMemburstAcceptanceCancelActiveWorker(t *testing.T) {
	process, _, _ := burstAcceptanceProcess(t)
	dir := burstAcceptanceBarrier(t, process)
	events := t.TempDir()
	burstAcceptanceStore(t, events)
	c := &memBurstTracing{snapshotJobs: make(chan *burstSnapshotJob, 1), snapshotProcessMaxNum: 1}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go c.runBurstSnapshots(ctx, done)
	c.snapshotJobs <- &burstSnapshotJob{processes: []*processMemInfo{process}, started: time.Now()}
	burstAcceptanceWaitRead(t, dir)
	cancel()
	close(c.snapshotJobs)
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("active worker did not stop")
	}
	raw, err := os.ReadFile(filepath.Join(events, "memburst"))
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Data MemoryTracingData `json:"tracer_data"`
	}
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Data.TopMemoryUsage) != 1 || event.Data.SnapshotReason != "snapshot canceled: context canceled" {
		t.Fatalf("canceled worker lost basic event: %s", raw)
	}
	assertBurstAcceptanceSnapshot(t, collectBurstSnapshots(t.Context(), []*processMemInfo{process}, 1)[0])
}

func TestMemburstAcceptanceStorageRecovery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "events")
	// A regular file obstructs directory creation even when tests run as root.
	if err := os.WriteFile(path, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	burstAcceptanceStore(t, path)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	process, _, _ := burstAcceptanceProcess(t)
	c := &memBurstTracing{}
	job := &burstSnapshotJob{processes: []*processMemInfo{process}, started: time.Now()}
	c.saveBurst(job, nil, "storage failure exercise")
	if !strings.Contains(logs.String(), "save memburst snapshot") || !strings.Contains(logs.String(), `level="warning"`) {
		t.Fatalf("missing expected save warning: %s", logs.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	c.saveBurst(job, collectBurstSnapshots(t.Context(), job.processes, 1), "")
	if logs.Len() != 0 {
		t.Fatalf("save did not recover: %s", logs.String())
	}
	raw, err := os.ReadFile(filepath.Join(path, "memburst"))
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Data MemoryTracingData `json:"tracer_data"`
	}
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Data.ProcessSnapshots) != 1 {
		t.Fatalf("missing recovered event: %s", raw)
	}
	assertBurstAcceptanceSnapshot(t, event.Data.ProcessSnapshots[0])
}

func burstAcceptanceRuntime(t *testing.T, cmd *exec.Cmd) *processMemInfo {
	t.Helper()
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
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, err = io.WriteString(input, "a"); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if scanner.Text() == "ready" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("runtime exited before readiness")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runtime readiness timeout")
	}
	pid := cmd.Process.Pid
	if name := os.Getenv("MEMBURST_ACTIVE_CONTAINER"); name != "" {
		raw, err := exec.Command("docker", "inspect", "--format", "{{.State.Pid}}", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
	}
	identity, err := memsnapshot.ReadProcessInstanceID(pid)
	if err != nil {
		t.Fatal(err)
	}
	return &processMemInfo{PID: int32(pid), ProcessName: filepath.Base(cmd.Path), identity: identity}
}

func TestMemburstAcceptanceMixedLanguages(t *testing.T) {
	dir := os.Getenv("MEMBURST_RUNTIME_FIXTURES")
	if dir == "" {
		t.Fatal("runtime fixtures are required")
	}
	goProcess, _, _ := burstAcceptanceProcess(t)
	ranked := []*processMemInfo{goProcess}
	expected := map[int32]memsnapshot.Language{goProcess.PID: memsnapshot.LanguageGo}
	for _, fixture := range []struct {
		language memsnapshot.Language
		args     []string
	}{
		{memsnapshot.LanguagePython, []string{"python3", filepath.Join(dir, "snapshot.py")}},
		{memsnapshot.LanguageJava, []string{"java", "-Xms32m", "-Xmx256m", "-cp", dir, "HeapFixture"}},
		{memsnapshot.LanguageNative, []string{filepath.Join(dir, "c-snapshot")}},
		{memsnapshot.LanguageCPP, []string{filepath.Join(dir, "cpp-snapshot")}},
	} {
		if _, err := exec.LookPath(fixture.args[0]); err != nil {
			t.Run(string(fixture.language), func(t *testing.T) { t.Skip("runtime/compiler unavailable") })
			continue
		}
		if fixture.language == memsnapshot.LanguageJava {
			if _, err := os.Stat(filepath.Join(dir, "HeapFixture.class")); err != nil {
				t.Run("java", func(t *testing.T) { t.Skip("javac unavailable") })
				continue
			}
		}
		p := burstAcceptanceRuntime(t, exec.Command(fixture.args[0], fixture.args[1:]...))
		ranked = append(ranked, p)
		expected[p.PID] = fixture.language
	}
	// Rank the real RSS values, then exercise both truncation and the full batch.
	for _, p := range ranked {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", p.PID))
		if err != nil {
			t.Fatal(err)
		}
		pages, err := strconv.ParseUint(strings.Fields(string(raw))[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		p.MemSize = pages * uint64(os.Getpagesize())
	}
	slices.SortFunc(ranked, func(a, b *processMemInfo) int { return cmp.Compare(b.MemSize, a.MemSize) })
	for _, top := range []int{3, 5} {
		records := collectBurstSnapshots(t.Context(), ranked, top)
		if len(records) != min(top, len(ranked)) {
			t.Fatalf("mixed selection count=%d", len(records))
		}
		for i, r := range records {
			if r.PID != ranked[i].PID || r.Language != expected[r.PID] || r.Snapshot == nil || r.ProcessMemory == nil {
				t.Fatalf("mixed selection identity: %+v", r)
			}
			s := r.Snapshot
			switch r.Language {
			case memsnapshot.LanguageGo:
				assertBurstAcceptanceSnapshot(t, r)
			case memsnapshot.LanguageNative, memsnapshot.LanguageCPP:
				if s.Status != memsnapshot.SnapshotStatusUnavailable || s.StatusReason == "" {
					t.Fatalf("native fallback: %+v", s)
				}
			case memsnapshot.LanguagePython:
				if s.Status == memsnapshot.SnapshotStatusUnavailable {
					if !strings.Contains(s.StatusReason, "CPython runtime is unsupported") {
						t.Fatalf("unexpected Python fallback: %+v", s)
					}
				} else if s.Status != memsnapshot.SnapshotStatusComplete || len(s.Entries) == 0 {
					t.Fatalf("Python capture: %+v", s)
				}
			case memsnapshot.LanguageJava:
				if (s.Status != memsnapshot.SnapshotStatusComplete && s.Status != memsnapshot.SnapshotStatusPartial) || len(s.Entries) == 0 {
					t.Fatalf("Java capture: %+v", s)
				}
			}
			raw, err := json.Marshal(s)
			if err != nil || len(raw) > memsnapshot.MaxSnapshotBytes || len(s.Entries) > 10 {
				t.Fatalf("mixed output bounds: %d %v", len(raw), err)
			}
		}
	}
}

func TestMemburstAcceptanceContainer(t *testing.T) {
	image := os.Getenv("MEMBURST_CONTAINER_IMAGE")
	if image == "" {
		t.Skip("container scenario runs through e2e/test_autotracing_memburst.sh")
	}
	name := fmt.Sprintf("huatuo-memburst-%d", os.Getpid())
	t.Setenv("MEMBURST_ACTIVE_CONTAINER", name)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	fixture := os.Getenv("MEMBURST_ACCEPTANCE_FIXTURE")
	process := burstAcceptanceRuntime(t, exec.Command("docker", "run", "--rm", "-i", "--name", name, "--network=none", "--mount", "type=bind,src="+fixture+",dst=/go-snapshot,readonly", "--entrypoint", "/go-snapshot", image, "1"))
	host, _, _ := burstAcceptanceProcess(t)
	records := collectBurstSnapshots(t.Context(), []*processMemInfo{process, host}, 2)
	if len(records) != 2 || records[0].PID != process.PID {
		t.Fatalf("container host PID: %+v", records)
	}
	for _, r := range records {
		assertBurstAcceptanceSnapshot(t, r)
	}
	if err := exec.Command("docker", "kill", name).Run(); err != nil {
		t.Fatal(err)
	}
	records = collectBurstSnapshots(t.Context(), []*processMemInfo{process, host}, 2)
	if records[0].Snapshot.Status != memsnapshot.SnapshotStatusFailed {
		t.Fatal("exited container accepted")
	}
	assertBurstAcceptanceSnapshot(t, records[1])
}

func TestMemburstAcceptanceThresholdActionContention(t *testing.T) {
	process, _, _ := burstAcceptanceProcess(t)
	dir := burstAcceptanceBarrier(t, process)
	events := t.TempDir()
	burstAcceptanceStore(t, events)
	cfg := &Config{}
	cfg.MemoryThresholdSnapshot.MaxMemoryObjectEntries = 10
	cfg.MemoryThresholdSnapshot.RunTracingToolTimeout = 15
	// Container discovery is covered by E2E; both real capture paths share this PID.
	batch := &actionBatch{config: cfg, ops: &actionBatchOps{
		selectProcess: func(context.Context, cgroupRef, uint64) (selectedProcess, error) {
			return selectedProcess{instance: process.identity, comm: process.ProcessName}, nil
		},
		validateContainer: func(pod.ContainerRef) error { return nil },
		validateProcess: func(_ context.Context, _ cgroupRef, identity memsnapshot.ProcessInstanceID) error {
			return memsnapshot.ValidateProcessInstanceID(identity)
		},
		snapshotProcessMemory: collector.Snapshot,
		save:                  tracing.Save,
	}}
	candidate := &memcgCandidate{observation: &memoryEventObservation{}, current: 90, max: 100, ratio: .9}
	finished := make(chan error, 1)
	go func() { finished <- batch.snapshotCandidate(t.Context(), candidate) }()
	burstAcceptanceWaitRead(t, dir)
	snapshots := collectBurstSnapshots(t.Context(), []*processMemInfo{process}, 1)
	if len(snapshots) != 1 || snapshots[0].Snapshot.StatusReason != collector.ErrCaptureBusy.Error() {
		t.Fatalf("threshold action did not exclude memburst: %+v", snapshots)
	}
	c := &memBurstTracing{}
	c.saveBurst(&burstSnapshotJob{processes: []*processMemInfo{process}, started: time.Now()}, snapshots, "")
	waitCtx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if err := batch.snapshotCandidate(waitCtx, candidate); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("threshold action wait timeout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("threshold action did not release slot")
	}
	raw, err := os.ReadFile(filepath.Join(events, memoryThresholdSnapshotTracer))
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Data memoryThresholdSnapshotData `json:"tracer_data"`
	}
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("threshold generated multiple or invalid events: %v", err)
	}
	if event.Data.VictimPID != int(process.PID) || event.Data.Snapshot.Status != memsnapshot.SnapshotStatusComplete {
		t.Fatalf("threshold action result: %s", raw)
	}
	assertBurstAcceptanceSnapshot(t, collectBurstSnapshots(t.Context(), []*processMemInfo{process}, 1)[0])
}

func TestMemburstAcceptanceConfiguredBatchBudget(t *testing.T) {
	process, _, _ := burstAcceptanceProcess(t)
	dir := burstAcceptanceBarrier(t, process)
	finished := make(chan []burstProcessSnapshot, 1)
	started := time.Now()
	go func() { finished <- collectBurstSnapshots(t.Context(), []*processMemInfo{process}, 1) }()
	burstAcceptanceWaitRead(t, dir)
	// Let the production two-second budget expire while a real read is paused.
	timer := time.NewTimer(time.Until(started.Add(burstProcessSnapshotBudget + 100*time.Millisecond)))
	defer timer.Stop()
	<-timer.C
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case records := <-finished:
		if len(records) != 1 || records[0].Snapshot.Status != memsnapshot.SnapshotStatusFailed || !strings.Contains(records[0].Snapshot.StatusReason, context.DeadlineExceeded.Error()) {
			t.Fatalf("configured batch budget: %+v", records)
		}
	case <-time.After(time.Second):
		t.Fatal("configured batch budget did not stop capture")
	}
	assertBurstAcceptanceSnapshot(t, collectBurstSnapshots(t.Context(), []*processMemInfo{process}, 1)[0])
}

func TestMemburstAcceptanceThresholdRetriesAfterBurst(t *testing.T) {
	process, _, _ := burstAcceptanceProcess(t)
	dir := burstAcceptanceBarrier(t, process)
	burstDone := make(chan []burstProcessSnapshot, 1)
	go func() { burstDone <- collectBurstSnapshots(t.Context(), []*processMemInfo{process}, 1) }()
	burstAcceptanceWaitRead(t, dir)
	batch := newRunnerActionBatchForTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	batch.ctx = ctx
	batch.config.MemoryThresholdSnapshot.MaxMemoryObjectEntries = 10
	batch.config.MemoryThresholdSnapshot.RunTracingToolTimeout = 1
	batch.ops.selectProcess = func(context.Context, cgroupRef, uint64) (selectedProcess, error) {
		return selectedProcess{instance: process.identity, comm: process.ProcessName}, nil
	}
	busy := make(chan struct{}, 1)
	batch.ops.snapshotProcessMemory = func(ctx context.Context, identity memsnapshot.ProcessInstanceID, options collector.Options) (*collector.Result, error) {
		result, err := collector.Snapshot(ctx, identity, options)
		if errors.Is(err, collector.ErrCaptureBusy) {
			select {
			case busy <- struct{}{}:
			default:
			}
		}
		return result, err
	}
	saved := 0
	batch.ops.save = func(req *tracing.WriteRequest) error {
		if req.TracerData.(*memoryThresholdSnapshotData).VictimPID != int(process.PID) {
			return errors.New("wrong process")
		}
		saved++
		return nil
	}
	done := make(chan time.Time, 1)
	go func() { done <- batch.Run() }()
	select {
	case <-busy:
	case <-ctx.Done():
		t.Fatal("threshold did not exhaust its first wait budget")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case records := <-burstDone:
		assertBurstAcceptanceSnapshot(t, records[0])
	case <-ctx.Done():
		t.Fatal("burst did not finish")
	}
	select {
	case finished := <-done:
		if finished.IsZero() || saved != 1 {
			t.Fatal("threshold event lost after contention")
		}
	case <-ctx.Done():
		t.Fatal("threshold retry did not finish")
	}
}
