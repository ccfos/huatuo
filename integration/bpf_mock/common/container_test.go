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

//go:build integration

package bpfmock

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runContainerHelper(t *testing.T, workspace, script string, env ...string) ([]byte, error) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\nsource \"$ROOT_DIR/integration/bpf_mock/common/lib.sh\"\n"+script)
	cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace,
		"KUBELET_CERT=/test/client.crt", "KUBELET_KEY=/test/client.key", "KUBELET_PODS_API=https://127.0.0.1:10250/pods",
		"HUATUO_BAMAI_ADDR=http://127.0.0.1:19704", "BUSINESS_POD_IMAGE=busybox:1.36.1", "BPF_MOCK_NODE=")
	cmd.Env = append(cmd.Env, env...)
	return cmd.CombinedOutput()
}

func mockPodJSON(id, phase string, ready bool, restarts int) string {
	return fmt.Sprintf(`{"status":{"phase":%q,"containerStatuses":[
 {"name":"sidecar","containerID":"containerd://other","ready":false,"restartCount":1},
 {"name":"trigger","containerID":%q,"ready":%t,"restartCount":%d}]}}`, phase, "containerd://"+id, ready, restarts)
}

func TestReadMockContainer(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, controller, css, apiID, podID, failure string
		wantError                                    bool
	}{
		{name: "memory", controller: "memory", css: `{"memory":"0x0000000000001234"}`},
		{name: "cpu", controller: "cpu", css: `{"cpu":"0x0000000000001234"}`},
		{name: "missing controller", controller: "memory", css: `{"cpu":"0x0000000000001234"}`, wantError: true},
		{name: "zero address", controller: "memory", css: `{"memory":"0x0000000000000000"}`, wantError: true},
		{name: "malformed address", controller: "memory", css: `{"memory":"1234"}`, wantError: true},
		{name: "wrong ID", controller: "memory", css: `{"memory":"0x0000000000001234"}`, apiID: strings.Repeat("b", 64), wantError: true},
		{name: "invalid runtime ID", controller: "memory", css: `{}`, podID: "invalid", wantError: true},
		{name: "API error", controller: "memory", css: `{}`, failure: "curl", wantError: true},
		{name: "kubelet error", controller: "memory", css: `{}`, failure: "kubectl", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			apiID, podID := id, id
			if tc.apiID != "" {
				apiID = tc.apiID
			}
			if tc.podID != "" {
				podID = tc.podID
			}
			metadata := fmt.Sprintf(`{"data":{"id":%q,"cgroup_css":%s}}`, apiID, tc.css)
			output, err := runContainerHelper(t, workspace, `
kubectl() { [[ $FAIL_STEP != kubectl ]] || return 1; printf '%s\n' "$POD_JSON"; }
curl() { [[ $FAIL_STEP != curl ]] || return 1; [[ ${*: -1} == "$HUATUO_BAMAI_ADDR/v1/containers/$EXPECTED_ID" ]] || return 2; printf '%s\n' "$API_JSON"; }
read_bpf_mock_container ns pod "$CONTROLLER" "$HUATUO_BAMAI_TEST_TMPDIR/container.json"
`, "POD_JSON="+mockPodJSON(podID, "Running", true, 0), "API_JSON="+metadata, "CONTROLLER="+tc.controller, "FAIL_STEP="+tc.failure, "EXPECTED_ID="+id)
			if (err != nil) != tc.wantError {
				t.Fatalf("discovery: %v: %s", err, output)
			}
			if !tc.wantError {
				actual, err := os.ReadFile(filepath.Join(workspace, "container.json"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(actual)) != metadata {
					t.Fatalf("metadata changed: %s", actual)
				}
			}
		})
	}
}

func TestMockContainerLiveness(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, phase, actualID string
		ready                 bool
		restarts              int
		wantError             bool
	}{
		{"healthy trigger after sidecar", "Running", id, true, 0, false},
		{"pending", "Pending", id, true, 0, true},
		{"unready", "Running", id, false, 0, true},
		{"restarted", "Running", id, true, 1, true},
		{"replaced", "Running", strings.Repeat("b", 64), true, 0, true},
		{"missing trigger", "Running", id, true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := mockPodJSON(tc.actualID, tc.phase, tc.ready, tc.restarts)
			if tc.name == "missing trigger" {
				pod = `{"status":{"phase":"Running","containerStatuses":[]}}`
			}
			output, err := runContainerHelper(t, t.TempDir(), `
kubectl() { printf '%s\n' "$POD_JSON"; }
verify_bpf_mock_container_alive ns pod "$EXPECTED_ID"
`, "POD_JSON="+pod, "EXPECTED_ID="+id)
			if (err != nil) != tc.wantError {
				t.Fatalf("health: %v: %s", err, output)
			}
		})
	}
}

func TestCreateMockPod(t *testing.T) {
	for _, tc := range []struct {
		name, node, failure string
		wantCalls           int
		wantError           bool
	}{
		{"discover node", "", "", 3, false},
		{"explicit node", "selected-node", "", 3, false},
		{"node lookup fails", "", "curl", 0, true},
		{"label fails", "selected-node", "label", 1, true},
		{"apply fails", "selected-node", "apply", 2, true},
		{"readiness fails", "selected-node", "wait", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			output, err := runContainerHelper(t, workspace, `
curl() { [[ $FAIL_STEP != curl && -z $BPF_MOCK_NODE ]] || return 1; printf '%s\n' '{"items":[{"spec":{"nodeName":"local-node"}}]}'; }
kubectl() {
 shift
 printf '%s\n' "$1" >> "$HUATUO_BAMAI_TEST_TMPDIR/calls"
 [[ $1 != "$FAIL_STEP" ]]
}
create_bpf_mock_pod ns pod "$HUATUO_BAMAI_TEST_TMPDIR/trigger path" /dev/mock || exit $?
jq -e --arg node "${BPF_MOCK_NODE:-local-node}" --arg dir "$HUATUO_BAMAI_TEST_TMPDIR/trigger path" '
 .metadata.namespace == "ns" and .metadata.name == "pod" and .spec.nodeName == $node
 and .spec.terminationGracePeriodSeconds == 1
 and .spec.containers[0].name == "trigger" and .spec.containers[0].imagePullPolicy == "Never"
 and any(.spec.volumes[]; .name == "trigger" and .hostPath.path == $dir)
' "$HUATUO_BAMAI_TEST_TMPDIR/pod.json"
`, "BPF_MOCK_NODE="+tc.node, "FAIL_STEP="+tc.failure)
			if (err != nil) != tc.wantError {
				t.Fatalf("create: %v: %s", err, output)
			}
			calls, readErr := os.ReadFile(filepath.Join(workspace, "calls"))
			if readErr != nil && tc.wantCalls != 0 {
				t.Fatal(readErr)
			}
			if len(strings.Fields(string(calls))) != tc.wantCalls {
				t.Fatalf("later pod operations ran after a failure: %s", calls)
			}
		})
	}
}

func TestNativeContainerCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, failure     string
		initial, expected int
	}{
		{"success", "", 0, 0}, {"original failure", "", 42, 42}, {"delete failure", "delete", 0, 1}, {"diagnostic failure", "describe", 42, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			output, err := runContainerHelper(t, workspace, `
source "$ROOT_DIR/integration/bpf_mock/common/native_memory.sh"
bpf_mock_namespace=owned-namespace
bpf_mock_pod=trigger
loaded_module=owned-module
kubectl() { shift; echo "$1" >> "$HUATUO_BAMAI_TEST_TMPDIR/calls"; [[ $1 != "$FAIL_STEP" ]]; }
huatuo_bamai_stop() { echo daemon >> "$HUATUO_BAMAI_TEST_TMPDIR/calls"; }
rmmod() { [[ $1 == owned-module ]]; echo unload >> "$HUATUO_BAMAI_TEST_TMPDIR/calls"; }
trap 'cleanup_native_memory_mock || exit $?' EXIT
exit "$INITIAL_STATUS"
`, "FAIL_STEP="+tc.failure, fmt.Sprintf("INITIAL_STATUS=%d", tc.initial))
			actualExit := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				actualExit = exit.ExitCode()
			}
			if actualExit != tc.expected {
				t.Fatalf("cleanup: exit=%d error=%v output=%s", actualExit, err, output)
			}
			calls, err := os.ReadFile(filepath.Join(workspace, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			expected := "daemon\ndelete\nunload\n"
			if tc.initial != 0 {
				expected = "describe\n" + expected
			}
			if string(calls) != expected {
				t.Fatalf("cleanup order: %s; expected %s", calls, expected)
			}
		})
	}
}

func TestMockContainerConfig(t *testing.T) {
	workspace := t.TempDir()
	output, err := runContainerHelper(t, workspace, `
printf '%s\n' 'BlackList = []' > "$HUATUO_BAMAI_TEST_TMPDIR/bamai.conf"
write_bpf_mock_container_config 23456
`)
	if err != nil {
		t.Fatalf("config: %v: %s", err, output)
	}
	config, err := os.ReadFile(filepath.Join(workspace, "bamai.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"BlackList = []", "ListenAddress = \"127.0.0.1:23456\"", "KubeletReadOnlyPort = 0", "KubeletAuthorizedPort = 10250", "KubeletClientCertPath = \"/test/client.crt,/test/client.key\""} {
		if !strings.Contains(string(config), expected) {
			t.Fatalf("missing %q: %s", expected, config)
		}
	}
}
