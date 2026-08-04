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

//go:build linux

// Tests cover real evidence commands, metrics requests, and target discovery.
package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/iohealth"
	"github.com/ccfos/huatuo/pkg/types"

	"golang.org/x/sys/unix"
)

const ioHealthIntegrationResultTimeout = 15 * time.Second

func TestIOHealthMetricsRequestTimeout(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required for the integration request timeout check")
	}
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- struct{}{}
		// Bound the unfixed case too: it incorrectly succeeds after this
		// response instead of returning curl's three-second timeout.
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
		case <-timer.C:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	// Match run.sh: the parent exports an array, then starts a new Bash.
	command := exec.CommandContext(ctx, "bash", "-c", `
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
export CURL_TIMEOUT
exec bash -c 'source "$1"; huatuo_bamai_metrics' _ "$1"
`, "_", filepath.Join(root, "integration", "test_io_health_event.sh"))
	command.Env = append(os.Environ(),
		"ROOT_DIR="+root,
		"HUATUO_BAMAI_TEST_TMPDIR="+t.TempDir(),
		"HUATUO_BAMAI_METRICS_API="+server.URL,
	)
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 28 {
		t.Fatalf("metrics request = %v, want curl timeout (28); output: %s", err, output)
	}
	select {
	case <-requested:
	default:
		t.Fatal("metrics request never reached the test server")
	}
}

func TestIOHealthRealCommandEvidence(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}
	if os.Getenv("TEST_IO_CONFIRM_NON_SYSTEM") != "1" {
		t.Skip("Set TEST_IO_CONFIRM_NON_SYSTEM=1 for dedicated test devices")
	}

	tests := []struct {
		name        string
		env         string
		tool        string
		protocol    iohealth.EvidenceProtocol
		triggerType string
	}{
		{
			name:        "nvme",
			env:         "TEST_NVME_DEVICE",
			tool:        "nvme",
			protocol:    iohealth.EvidenceProtocolNVMe,
			triggerType: ioHealthTypeNVMeTimeout,
		},
		{
			name:        "scsi",
			env:         "TEST_SCSI_DEVICE",
			tool:        "smartctl",
			protocol:    iohealth.EvidenceProtocolSCSI,
			triggerType: ioHealthTypeSCSITimeout,
		},
	}

	selected := 0
	for _, test := range tests {
		rawDevice := strings.TrimSpace(os.Getenv(test.env))
		if rawDevice == "" {
			continue
		}
		selected++
		t.Run(test.name, func(t *testing.T) {
			device := requireIOHealthIntegrationDevice(
				t,
				rawDevice,
				test.env,
				test.protocol,
			)
			if _, err := exec.LookPath(test.tool); err != nil {
				t.Skipf("%s is unavailable: %v", test.tool, err)
			}
			runIOHealthRealCommandEvidence(
				t,
				device,
				test.protocol,
				test.triggerType,
			)
		})
	}
	if selected == 0 {
		t.Skip("Set TEST_NVME_DEVICE or TEST_SCSI_DEVICE")
	}
}

func TestIOHealthRealTargetResolution(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	tests := []struct {
		name      string
		env       string
		multiLeaf bool
	}{
		{name: "unique leaf", env: "TEST_IO_UNIQUE_DEVICE"},
		{name: "multiple leaves", env: "TEST_IO_MULTILEAF_DEVICE", multiLeaf: true},
	}
	selected := 0
	for _, test := range tests {
		device := strings.TrimSpace(os.Getenv(test.env))
		if device == "" {
			continue
		}
		selected++
		t.Run(test.name, func(t *testing.T) {
			if filepath.Base(device) != device {
				t.Fatalf("%s must be a block device name, got %q", test.env, device)
			}

			resolver := newIOHealthResolver("/sys")
			leaves, err := resolver.blockLeaves(device, make(map[string]bool))
			if err != nil {
				t.Fatalf("resolve %s leaves: %v", device, err)
			}
			target := resolver.resolveBlockName(device)
			if test.multiLeaf {
				if len(leaves) < 2 || target.target != "" ||
					target.reason != iohealth.CollectionReasonTargetUnsupported {
					t.Fatalf("device %s leaves=%v target=%+v", device, leaves, target)
				}
				return
			}
			if len(leaves) != 1 || target.target == "" || target.reason != "" {
				t.Fatalf("device %s leaves=%v target=%+v", device, leaves, target)
			}
		})
	}
	if selected == 0 {
		t.Skip("Set TEST_IO_UNIQUE_DEVICE or TEST_IO_MULTILEAF_DEVICE")
	}
}

func runIOHealthRealCommandEvidence(
	t *testing.T,
	device string,
	protocol iohealth.EvidenceProtocol,
	triggerType string,
) {
	t.Helper()

	collector := newIOHealthCollector("/sys", "/proc/mdstat")
	saved := make(chan recordedIOHealthEvent, 1)
	collector.saveEvent = func(
		_ context.Context,
		at time.Time,
		event types.IOHealthEvent,
	) error {
		saved <- recordedIOHealthEvent{at: at, event: event}
		return nil
	}
	collector.setEventSubmitter(func(at time.Time, event types.IOHealthEvent) {
		_ = collector.saveEvent(t.Context(), at, event)
	})
	t.Cleanup(func() { collector.setEventSubmitter(nil) })
	worker := iohealth.NewEvidenceWorker(iohealth.EvidenceWorkerOptions{
		OnResult: collector.handleEvidenceResult,
	})
	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer func() {
		cancel()
		worker.Wait()
	}()

	triggeredAt := time.Now()
	if !worker.Submit(iohealth.EvidenceRequest{
		Trigger: types.IOHealthEvent{
			Type:   triggerType,
			Device: device,
		},
		Target:      device,
		Protocol:    protocol,
		TriggeredAt: triggeredAt,
	}) {
		t.Fatal("production evidence worker rejected the explicit device")
	}

	select {
	case got := <-saved:
		if got.at != triggeredAt ||
			got.event.Device != device ||
			got.event.Type != triggerType {
			t.Fatalf("saved event = %+v", got)
		}
		if got.event.CollectionStatus != "ok" &&
			got.event.CollectionStatus != "partial" {
			t.Fatalf("collection status = %q", got.event.CollectionStatus)
		}
		switch protocol {
		case iohealth.EvidenceProtocolNVMe:
			if got.event.NVMe == nil || got.event.SCSI != nil {
				t.Fatalf("NVMe evidence = %+v", got.event)
			}
		case iohealth.EvidenceProtocolSCSI:
			if got.event.SCSI == nil || got.event.NVMe != nil {
				t.Fatalf("SCSI evidence = %+v", got.event)
			}
		}
	case <-time.After(ioHealthIntegrationResultTimeout):
		t.Fatal("timed out waiting for health command evidence")
	}
}

func requireIOHealthIntegrationDevice(
	t *testing.T,
	rawDevice, envName string,
	protocol iohealth.EvidenceProtocol,
) string {
	t.Helper()

	device := rawDevice
	if filepath.IsAbs(device) {
		if filepath.Clean(device) != device || filepath.Dir(device) != "/dev" {
			t.Fatalf("%s must be /dev/<name> or <name>, got %q", envName, rawDevice)
		}
		device = filepath.Base(device)
	}
	if _, valid := healthCommandTargetForTest(device); !valid {
		t.Fatalf("%s has an invalid device name %q", envName, rawDevice)
	}
	path := filepath.Join("/dev", device)
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Skipf("stat %s: %v", path, err)
	}

	switch protocol {
	case iohealth.EvidenceProtocolNVMe:
		if !ioHealthNVMeControllerPattern.MatchString(device) {
			t.Fatalf("%s must name an NVMe controller", envName)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
			t.Skipf("%s is not an NVMe controller character device", path)
		}
		if _, err := os.Stat(filepath.Join("/sys/class/nvme", device)); err != nil {
			t.Skipf("resolve %s in sysfs: %v", device, err)
		}
	case iohealth.EvidenceProtocolSCSI:
		if !strings.HasPrefix(device, "sd") {
			t.Fatalf("%s must name a SCSI disk", envName)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFBLK {
			t.Skipf("%s is not a block device", path)
		}
	}
	return device
}

func healthCommandTargetForTest(target string) (string, bool) {
	if target == "" || target == "." || target == ".." ||
		filepath.Base(target) != target || strings.ContainsAny(target, `/\`) {
		return "", false
	}
	for _, value := range target {
		if value >= 'a' && value <= 'z' ||
			value >= 'A' && value <= 'Z' ||
			value >= '0' && value <= '9' ||
			value == '-' || value == '_' || value == '.' {
			continue
		}
		return "", false
	}
	return target, true
}
