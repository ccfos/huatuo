// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Fixture and runner tests exercise the integration scripts through real
// commands, with device operations replaced at their dependency boundary.
package iocost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pelletier/go-toml"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func ioCostRepository(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func TestIOCostOfflineQualificationUsesLiveCallback(t *testing.T) {
	repository := ioCostRepository(t)
	for _, test := range []struct {
		name    string
		release string
		symbols string
		want    string
	}{
		{"backported callback", "4.18.0-backport", "1000 t ioc_pd_offline\n", "selected\n"},
		{"unversioned callback", "vendor-kernel", "1000 t ioc_pd_offline\n", "selected\n"},
		{"callback removed", "5.10.0", "1000 t ioc_pd_free\n", "skipped\n"},
		{"different callback", "6.18.0", "1000 t other_ioc_pd_offline\n", "skipped\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			kallsyms := filepath.Join(t.TempDir(), "kallsyms")
			require.NoError(t, os.WriteFile(kallsyms, []byte(test.symbols), 0o600))
			command := exec.CommandContext(t.Context(), "bash", "-c", `
source "$1/integration/iocost/lib.sh"
source "$1/integration/iocost/cases.sh"
IOCOST_KERNEL_RELEASE=$2
IOCOST_TEST_KALLSYMS=$3
grep() { command grep "${@:1:$#-1}" "${IOCOST_TEST_KALLSYMS}"; }
iocost_log() { :; }
iocost_set_cost_rates() { printf 'selected\n'; exit 0; }
iocost_functional_510_offline
printf 'skipped\n'
`, "iocost-offline-capability", repository, test.release, kallsyms)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.Equal(t, test.want, string(output))
		})
	}
}

func TestIOCostSCSIRebindExpectedCountUsesObservedIOC(t *testing.T) {
	tests := []struct {
		name   string
		iocs   map[string]string
		device string
		want   string
	}{
		{"retained IOC", map[string]string{"0x1": "8:16"}, "8:16", "3"},
		{"retained IOC new label", map[string]string{"0x1": "8:32"}, "8:32", "3"},
		{"new IOC same label", map[string]string{"0x2": "8:16"}, "8:16", "2"},
		{"new IOC new label", map[string]string{"0x2": "8:32"}, "8:32", "2"},
		{"missing IOC", map[string]string{}, "8:16", ""},
		{"ambiguous IOC", map[string]string{"0x1": "8:16", "0x2": "8:16"}, "8:16", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			before, after := filepath.Join(directory, "before.json"), filepath.Join(directory, "after.json")
			for path, iocs := range map[string]map[string]string{
				before: {"0x1": "8:16"}, after: test.iocs,
			} {
				data, err := json.Marshal(map[string]any{
					"error": "",
					"drain": map[string]any{
						"pending_rows": 0, "active_wake_frames": 0, "drained": true,
					},
					"observation": map[string]any{"iocs": iocs},
				})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", `
source "$1/integration/iocost/cases.sh"
iocost_lifecycle_scsi_expected_count "$2" "$3" 8:16 "$4"
`, "iocost-scsi-count", ioCostRepository(t), before, after, test.device)
			output, err := command.Output()
			if test.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, strings.TrimSpace(string(output)))
		})
	}
}

func TestIOCostSCSIDebugUnloadStopsHungCommandAtDeadline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the process-identity cleanup contract requires Linux procfs")
	}
	repository := ioCostRepository(t)
	workDir := t.TempDir()
	survivorIdentityPath := filepath.Join(workDir, "survivor.identity")
	t.Cleanup(func() {
		data, err := os.ReadFile(survivorIdentityPath)
		if err != nil {
			return
		}
		identity := strings.Fields(string(data))
		if len(identity) != 2 {
			return
		}
		pid, err := strconv.Atoi(identity[0])
		if err != nil {
			return
		}
		startTime, err := ioCostTestProcessStartTime(pid)
		if err != nil || startTime != identity[1] {
			return
		}
		process, err := os.FindProcess(pid)
		if err == nil {
			_ = process.Kill()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", "-c", `
set -euo pipefail
source "$1/integration/iocost/lib.sh"
survivor_identity_path=$2
iocost_signal_critical_command() {
	sleep 4 &
	survivor_pid=$!
	survivor_starttime=$(iocost_proc_starttime "$survivor_pid")
	printf '%s %s\n' "$survivor_pid" "$survivor_starttime" >"$survivor_identity_path"
	wait
}
iocost_signal_critical_enter
set +e
iocost_run_scsi_debug_unload "$((SECONDS + 1))"
status=$?
set -e
iocost_signal_critical_leave
((status == 124))
((${#IOCOST_TRACKED_PIDS[@]} == 0))
`, "iocost-scsi-unload-deadline", repository, survivorIdentityPath)
	command.Dir = workDir
	started := time.Now()
	output, err := command.CombinedOutput()
	elapsed := time.Since(started)
	require.NoError(t, err, "%s", output)
	require.NoError(t, ctx.Err(), "hung cleanup exceeded the test deadline")
	require.FileExists(t, survivorIdentityPath)
	require.Less(t, elapsed, 3*time.Second,
		"a surviving descendant retained the qualification output pipe")
}

func TestIOCostStoppedProcessCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process cleanup requires Linux procfs and process groups")
	}
	for _, test := range []struct {
		name           string
		children       int
		stop           string
		ignoreTERM     bool
		minimumSeconds int
		maximumSeconds int
		setupFailure   string
		wantExit       int
	}{
		{"single stopped child handles TERM", 1, "single", false, 0, 5, "", 0},
		{"stopped batch handles TERM", 32, "all", false, 0, 5, "", 0},
		{"TERM-resistant batch shares deadline", 32, "all", true, 10, 20, "", 0},
		{"setup wait fails after spawn", 1, "single", false, 0, 5, "wait", 1},
		{"setup canceled before identity capture", 1, "single", false, 0, 5, "cancel", 143},
	} {
		var setupIdentity []string
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			identitiesPath := filepath.Join(directory, "identities")
			termPath := filepath.Join(directory, "term")
			t.Cleanup(func() {
				data, err := os.ReadFile(identitiesPath)
				if err != nil {
					return
				}
				for _, line := range strings.Split(string(data), "\n") {
					identity := strings.Fields(line)
					if len(identity) != 2 {
						continue
					}
					if test.setupFailure != "" {
						setupIdentity = identity
					}
					pid, err := strconv.Atoi(identity[0])
					if err != nil {
						continue
					}
					startTime, err := ioCostTestProcessStartTime(pid)
					if err != nil || startTime != identity[1] {
						continue
					}
					if pgid, err := unix.Getpgid(pid); err == nil && pgid == pid {
						_ = unix.Kill(-pgid, unix.SIGKILL)
					} else {
						_ = unix.Kill(pid, unix.SIGKILL)
					}
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(),
				time.Duration(test.maximumSeconds+5)*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", `
set -euo pipefail
source "$1/integration/iocost/lib.sh"
identities_path=$2
term_path=$3
trap 'iocost_handle_signal 143' TERM
if [[ $7 == wait ]]; then
	iocost_wait_for_state() { return 1; }
fi
for ((index = 0; index < $4; index++)); do
	iocost_signal_critical_enter
	setsid bash -c '
		if [[ $2 == true ]]; then
			trap "" TERM
		else
			trap '\''printf "%s\n" "$$" >> "$1"; exit 0'\'' TERM
		fi
		kill -STOP "$$"
		while :; do sleep 1; done
	' iocost-cleanup-child "${term_path}" "$6" > /dev/null 2>&1 &
	pid=$!
	if [[ $7 == cancel ]]; then
		kill -TERM "$$"
	fi
	starttime=$(iocost_proc_starttime "${pid}")
	printf '%s %s\n' "${pid}" "${starttime}" >> "${identities_path}"
	iocost_signal_critical_leave
	iocost_wait_for_state "${pid}" T 5
	iocost_record_pid "${pid}" "stopped cleanup child"
	[[ ${IOCOST_PID_PGID[${pid}]} == "${pid}" ]]
done
started=$SECONDS
if [[ $5 == single ]]; then
	iocost_stop_pid "${pid}"
else
	iocost_stop_all_pids
fi
((${#IOCOST_TRACKED_PIDS[@]} == 0))
printf '%s\n' "$((SECONDS - started))"
`, "iocost-stopped-cleanup", ioCostRepository(t), identitiesPath,
				termPath, strconv.Itoa(test.children), test.stop, strconv.FormatBool(test.ignoreTERM), test.setupFailure)
			// TERM lets the existing critical section publish the child identity
			// before cancellation exits Bash and Go performs fallback cleanup.
			command.Cancel = func() error { return command.Process.Signal(unix.SIGTERM) }
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if test.setupFailure != "" {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError, "%s", stderr.String())
				require.Equal(t, test.wantExit, exitError.ExitCode())
				require.NoError(t, ctx.Err())
				return
			}
			require.NoError(t, err, "%s", stderr.String())
			require.NoError(t, ctx.Err())
			seconds, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
			require.NoError(t, err)
			require.GreaterOrEqual(t, seconds, test.minimumSeconds)
			require.Less(t, seconds, test.maximumSeconds,
				"cleanup must not multiply the grace period by the number of children")
			data, err := os.ReadFile(identitiesPath)
			require.NoError(t, err)
			require.Len(t, strings.Split(strings.TrimSpace(string(data)), "\n"), test.children)
			if !test.ignoreTERM {
				data, err = os.ReadFile(termPath)
				require.NoError(t, err, "stopped children must handle TERM before forced cleanup")
				require.Len(t, strings.Fields(string(data)), test.children)
			}
		})
		if test.setupFailure != "" {
			require.Len(t, setupIdentity, 2, "setup failure must retain the latest spawned identity")
			pid, err := strconv.Atoi(setupIdentity[0])
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if err != nil {
					return os.IsNotExist(err)
				}
				end := strings.LastIndex(string(data), ") ")
				if end < 0 {
					return false
				}
				fields := strings.Fields(string(data)[end+2:])
				return len(fields) >= 20 && (fields[19] != setupIdentity[1] ||
					fields[0] == "Z" || fields[0] == "X")
			}, time.Second, 10*time.Millisecond, "setup failure leaked the spawned child")
		}
	}
}

func ioCostTestProcessStartTime(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	remainder := string(data)
	end := strings.LastIndex(remainder, ") ")
	if end < 0 {
		return "", fmt.Errorf("invalid stat for pid %d", pid)
	}
	fields := strings.Fields(remainder[end+2:])
	if len(fields) < 20 {
		return "", fmt.Errorf("short stat for pid %d", pid)
	}
	return fields[19], nil
}

func TestIOCostLoopRemovalUsesRegisteredDeviceIdentity(t *testing.T) {
	repository := ioCostRepository(t)
	data, err := os.ReadFile(filepath.Join(
		repository, "integration", "iocost", "lib.sh"))
	require.NoError(t, err)
	remove := ioCostLifecycleFunction(t, string(data), "iocost_remove_loop_minor_exact()")
	_, script, ok := strings.Cut(remove, "<< 'PY'\n")
	require.True(t, ok)
	script, _, ok = strings.Cut(script, "\nPY\n")
	require.True(t, ok)

	// Substitute device discovery and ioctl, preserving the real cleanup code.
	// loop.max_part=15 assigns loop256 minor 4096, while REMOVE still takes 256.
	program := `
import io, json, os, stat, sys
from types import SimpleNamespace
from unittest.mock import MagicMock, patch
node_id, sysfs_id, expected = sys.argv[1:]
major, minor = map(int, node_id.split(":"))
control = MagicMock()
control.__enter__.return_value = control
control.fileno.return_value = 9
removed = []
def opened(path, *args, **kwargs):
    if path == "/sys/block/loop256/dev":
        return io.StringIO(sysfs_id + "\n")
    assert path == "/dev/loop-control", path
    return control
def ioctl(fd, command, index):
    assert (fd, command, index) == (9, 0x4C81, 256)
    removed.append(index)
    return 0
sys.argv = ["loop", "256", expected, "1"]
error = ""
try:
    with patch("builtins.open", opened), patch("os.stat", return_value=SimpleNamespace(
            st_mode=stat.S_IFBLK, st_rdev=os.makedev(major, minor))), patch("fcntl.ioctl", ioctl):
        exec(` + strconv.Quote(script) + `)
except RuntimeError as exception:
    error = str(exception)
print(json.dumps({"error": error, "removed": removed}))
`
	for _, test := range []struct {
		name, node, sysfs, expected string
		wantError                   string
	}{
		{name: "no partitions", node: "7:256", sysfs: "7:256", expected: "7:256"},
		{name: "partition minors", node: "7:4096", sysfs: "7:4096", expected: "7:4096"},
		{name: "node mismatch", node: "7:4097", sysfs: "7:4096", expected: "7:4096", wantError: "identity mismatch"},
		{name: "registration mismatch", node: "7:4096", sysfs: "7:4096", expected: "7:256", wantError: "identity changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), "python3", "-",
				test.node, test.sysfs, test.expected)
			command.Stdin = strings.NewReader(program)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			var result struct {
				Error   string `json:"error"`
				Removed []int  `json:"removed"`
			}
			require.NoError(t, json.Unmarshal(output, &result))
			if test.wantError != "" {
				require.Contains(t, result.Error, test.wantError)
				require.Empty(t, result.Removed)
			} else {
				require.Empty(t, result.Error)
				require.Equal(t, []int{256}, result.Removed)
			}
		})
	}
}

func ioCostLifecycleFunction(t *testing.T, source, marker string) string {
	t.Helper()
	start := strings.Index(source, marker)
	require.GreaterOrEqual(t, start, 0, "missing %s", marker)
	end := strings.Index(source[start:], "\n}\n")
	require.Greater(t, end, 0, "unterminated %s", marker)
	return source[start : start+end]
}

func TestIOCostElapsedBoundsAllowBoundedProcessOverhead(t *testing.T) {
	repository := ioCostRepository(t)
	metrics := filepath.Join(repository,
		"integration", "iocost", "lib.sh")

	bounds := func(elapsedMilliseconds int) (float64, float64) {
		end := strconv.FormatInt(int64(elapsedMilliseconds)*1_000_000, 10)
		command := exec.Command("bash", "-c",
			`source "$1"; iocost_elapsed_bounds 0 "$2"`,
			"iocost-elapsed-bounds", metrics, end)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s", output)
		fields := strings.Fields(string(output))
		require.Len(t, fields, 2)
		lower, err := strconv.ParseFloat(fields[0], 64)
		require.NoError(t, err)
		upper, err := strconv.ParseFloat(fields[1], 64)
		require.NoError(t, err)
		return lower, upper
	}

	lower, upper := bounds(150)
	require.LessOrEqual(t, lower, 50.0,
		"a short wait must tolerate bounded process startup and exit time")
	require.GreaterOrEqual(t, upper, 50.0)

	lower, _ = bounds(1000)
	require.Greater(t, lower, 100.0,
		"a long wait must still reject a unit or scale error")
}

func TestIOCostPressureQualificationRejectsLeakedRawMapRows(t *testing.T) {
	repository := ioCostRepository(t)
	runtimeScript := filepath.Join(repository, "integration", "iocost", "lib.sh")
	for _, test := range []struct {
		name      string
		leakedRow string
		shuffled  bool
	}{
		{name: "only live rows"},
		{name: "shuffled business rows", shuffled: true},
		{name: "ioc_rows", leakedRow: "ioc_rows"},
		{name: "owner_rows", leakedRow: "owner_rows"},
		{name: "aggregate_rows", leakedRow: "aggregate_rows"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := map[string]uint64{"reason": 0, "errno": 0}
			rows := map[string]uint32{"ioc_rows": 1, "owner_rows": 1, "aggregate_rows": 1}
			if test.leakedRow != "" {
				rows[test.leakedRow]++
			}
			interval := func(count, average float64) []map[string]any {
				var points []map[string]any
				for _, scope := range []string{"host", "other"} {
					labels := []map[string]string{
						{"name": "region", "value": "iocost-test"},
						{"name": "host", "value": "fixture"},
						{"name": "device", "value": "7:9"},
						{"name": "operation", "value": "write"},
						{"name": "scope", "value": scope},
					}
					points = append(points,
						map[string]any{"name": "waitq_io_count", "value": count, "labels": labels},
						map[string]any{"name": "average_wait_milliseconds", "value": average, "labels": labels},
					)
				}
				return points
			}
			first, second := interval(1, 5), interval(0, 0)
			if test.shuffled {
				slices.Reverse(first)
				second = append(second[1:], second[0])
			}
			result := map[string]any{
				"error": "", "status": status,
				"drain":       map[string]any{"pending_rows": 0, "active_wake_frames": 0, "drained": true},
				"observation": rows,
				"lane_observation": map[string]any{
					"count_lanes": []int{0}, "wait_lanes": []int{0},
					"raw_series": []map[string]string{{
						"device": "7:9", "operation": "write", "io_count": "1", "wait_10us": "500",
					}},
				},
				"business_intervals": map[string]any{"first": first, "second": second},
			}
			data, err := json.Marshal(result)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "result.json")
			require.NoError(t, os.WriteFile(path, data, 0o600))
			command := exec.CommandContext(t.Context(), "bash", "-c", `
set -euo pipefail
iocost_die() { printf '%s\n' "$*" >&2; exit 1; }
source "$1"
iocost_assert_qualification_result "$2" '7:9,1,4,6'
iocost_assert_runtime_common "$2" 0 healthy 1 1 1
`, "iocost-qualification-map-rows", runtimeScript, path)
			output, err := command.CombinedOutput()
			if test.leakedRow == "" {
				require.NoError(t, err, "%s", output)
				return
			}
			require.Error(t, err, "filtered business/raw series hid an extra %s row: %s", test.leakedRow, output)
			require.Contains(t, string(output), test.leakedRow)
		})
	}
}

func TestIOCostGenericIntegrationBlacklists(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Join(filepath.Dir(file), "..", "..")
	configScript := filepath.Join(repository, "integration", "config.sh")
	for _, writer := range []string{
		"write_default_config",
		"write_include_filter_config",
		"write_exclude_filter_config",
		"write_sched_tick_config",
		"write_sched_tick_irqoff_config",
	} {
		t.Run(writer, func(t *testing.T) {
			directory := t.TempDir()
			command := exec.CommandContext(t.Context(), "bash", "-c", `
set -euo pipefail
HUATUO_BAMAI_TEST_TMPDIR=$2
source "$1"
"$3"
`, "iocost-generic-integration-config", configScript, directory, writer)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			data, err := os.ReadFile(filepath.Join(directory, "bamai.conf"))
			require.NoError(t, err)
			var config struct {
				BlackList []string
			}
			require.NoError(t, toml.Unmarshal(data, &config))
			count := 0
			for _, name := range config.BlackList {
				if name == "iocost" {
					count++
				}
			}
			require.Equal(t, 1, count,
				"ordinary integration config must disable IOCOST exactly once")
			if writer == "write_sched_tick_config" || writer == "write_sched_tick_irqoff_config" {
				require.Contains(t, config.BlackList, "blk_throtl")
				require.Contains(t, config.BlackList, "io_health")
				require.NotContains(t, config.BlackList, "sched_tick")
				require.NotContains(t, config.BlackList, "tracing_status")
			}
		})
	}
}

func TestIOCostIntegrationEntryRequiresExplicitQualification(t *testing.T) {
	repository := ioCostRepository(t)
	entry, err := os.ReadFile(filepath.Join(repository, "integration", "test_iocost.sh"))
	require.NoError(t, err)
	library, err := os.ReadFile(filepath.Join(repository, "integration", "lib.sh"))
	require.NoError(t, err)
	for _, test := range []struct {
		name       string
		required   string
		mode       string
		args       []string
		runnerExit int
		wantExit   int
		wantMode   string
	}{
		{name: "not requested", wantExit: 77},
		{name: "disabled", required: "0", wantExit: 77},
		{name: "not exactly one", required: "01", wantExit: 77},
		{name: "default functional", required: "1", wantMode: "functional"},
		{name: "environment pressure", required: "1", mode: "pressure", wantMode: "pressure"},
		{name: "selected pressure", required: "1", args: []string{"pressure"}, wantMode: "pressure"},
		{name: "argument overrides environment", required: "1", mode: "pressure", args: []string{"compat"}, wantMode: "compat"},
		{name: "runner failure", required: "1", runnerExit: 23, wantExit: 23, wantMode: "functional"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(directory, "iocost"), 0o700))
			entryPath := filepath.Join(directory, "test_iocost.sh")
			require.NoError(t, os.WriteFile(entryPath, entry, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "lib.sh"), library, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "iocost", "run.sh"), []byte(`
printf '%s\n' "${TEST_IOCOST_REQUIRED}" "$@" > "${IOCOST_ENTRY_RECORD}"
exit "${IOCOST_ENTRY_EXIT}"
`), 0o600))
			recordPath := filepath.Join(directory, "record")
			command := exec.CommandContext(t.Context(), "bash", append([]string{entryPath}, test.args...)...)
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "TEST_IOCOST_REQUIRED=") && !strings.HasPrefix(value, "TEST_IOCOST_MODE=") {
					command.Env = append(command.Env, value)
				}
			}
			command.Env = append(command.Env,
				"IOCOST_ENTRY_RECORD="+recordPath,
				"IOCOST_ENTRY_EXIT="+strconv.Itoa(test.runnerExit),
				"TEST_IOCOST_MODE="+test.mode)
			if test.required != "" {
				command.Env = append(command.Env, "TEST_IOCOST_REQUIRED="+test.required)
			}
			output, err := command.CombinedOutput()
			status := 0
			if err != nil {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError)
				status = exitError.ExitCode()
			}
			require.Equal(t, test.wantExit, status, "%s", output)
			data, err := os.ReadFile(recordPath)
			if test.wantExit == 77 {
				require.ErrorIs(t, err, os.ErrNotExist,
					"skipped requests must not enter qualification setup")
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"1", test.wantMode}, strings.Fields(string(data)))
			}
		})
	}
}

func TestIOCostIntegrationRunnerRequiresExecution(t *testing.T) {
	repository := ioCostRepository(t)
	for _, test := range []struct {
		name        string
		entry       string
		mode        string
		required    string
		missingTool string
		nonRoot     bool
		allTests    bool
		wantExit    int
		wantMode    string
	}{
		{name: "non-root optional", nonRoot: true},
		{name: "required flag for unselected case", entry: "test_other.sh", nonRoot: true, required: "1"},
		{name: "non-root required", nonRoot: true, required: "1", wantExit: 1},
		{name: "non-root required full suite", nonRoot: true, required: "1", allTests: true, wantExit: 1},
		{name: "missing unshare optional", missingTool: "unshare"},
		{name: "missing unshare required", missingTool: "unshare", required: "1", wantExit: 1},
		{name: "missing mount optional", missingTool: "mount"},
		{name: "missing mount required", missingTool: "mount", required: "1", wantExit: 1},
		{name: "default functional", required: "1", wantMode: "functional"},
		{name: "functional", mode: "functional", required: "1", wantMode: "functional"},
		{name: "lifecycle", mode: "lifecycle", required: "1", wantMode: "lifecycle"},
		{name: "faults", mode: "faults", required: "1", wantMode: "faults"},
		{name: "pressure", mode: "pressure", required: "1", wantMode: "pressure"},
		{name: "compat", mode: "compat", required: "1", wantMode: "compat"},
		{name: "all", mode: "all", required: "1", wantMode: "all"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.missingTool != "" || test.wantMode != "" {
				if os.Geteuid() != 0 {
					t.Skip("this runner path requires root")
				}
			}
			if test.wantMode != "" {
				if output, err := exec.CommandContext(t.Context(), "unshare", "--uts", "--mount", "true").CombinedOutput(); err != nil {
					t.Skipf("namespace dispatch unavailable: %v: %s", err, output)
				}
			}
			directory := t.TempDir()
			integration := filepath.Join(directory, "integration")
			require.NoError(t, os.MkdirAll(filepath.Join(integration, "iocost"), 0o755))
			require.NoError(t, os.Mkdir(filepath.Join(directory, "_output"), 0o755))
			for _, file := range []string{"run.sh", "lib.sh", "test_iocost.sh"} {
				data, err := os.ReadFile(filepath.Join(repository, "integration", file))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(integration, file), data, 0o600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(integration, "env.sh"), []byte(`
export ROOT_DIR=$PWD
HUATUO_BAMAI_TEST_TMPDIR=$(mktemp -d "$ROOT_DIR/workspace.XXXXXX")
export HUATUO_BAMAI_TEST_TMPDIR
HUATUO_BAMAI_ARGS_E2E=()
`), 0o600))
			record := filepath.Join(directory, "record")
			require.NoError(t, os.WriteFile(filepath.Join(integration, "iocost", "run.sh"), []byte(`
printf '%s\n' "$TEST_IOCOST_REQUIRED" "$@" > "$IOCOST_ENTRY_RECORD"
`), 0o600))
			if test.entry != "" {
				require.NoError(t, os.WriteFile(filepath.Join(integration, test.entry), []byte("exit 0\n"), 0o600))
			}
			args := []string{filepath.Join(integration, "run.sh")}
			if !test.allTests {
				entry := test.entry
				if entry == "" {
					entry = "test_iocost.sh"
				}
				args = append(args, entry)
			}
			command := exec.CommandContext(t.Context(), "bash", args...)
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "TEST_IOCOST_REQUIRED=") && !strings.HasPrefix(value, "TEST_IOCOST_MODE=") {
					command.Env = append(command.Env, value)
				}
			}
			command.Env = append(command.Env, "TEST_IOCOST_REQUIRED="+test.required,
				"TEST_IOCOST_MODE="+test.mode, "IOCOST_ENTRY_RECORD="+record)
			if test.nonRoot && os.Geteuid() == 0 {
				// Give the child access only to this test's runner and library.
				require.NoError(t, os.Chmod(filepath.Dir(directory), 0o755))
				require.NoError(t, os.Chmod(directory, 0o755))
				require.NoError(t, os.Chown(filepath.Join(integration, "run.sh"), 65534, 65534))
				require.NoError(t, os.Chown(filepath.Join(integration, "lib.sh"), 65534, 65534))
				command.SysProcAttr = &syscall.SysProcAttr{
					Credential: &syscall.Credential{Uid: 65534, Gid: 65534},
				}
			}
			if test.missingTool != "" {
				bin := filepath.Join(directory, "bin")
				require.NoError(t, os.Mkdir(bin, 0o755))
				for _, tool := range []string{"bash", "dirname", "date", "mount", "unshare"} {
					if tool == test.missingTool {
						continue
					}
					path, err := exec.LookPath(tool)
					require.NoError(t, err)
					require.NoError(t, os.Symlink(path, filepath.Join(bin, tool)))
				}
				command.Env = append(command.Env, "PATH="+bin)
			}
			output, err := command.CombinedOutput()
			status := 0
			if err != nil {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError)
				status = exitError.ExitCode()
			}
			require.Equal(t, test.wantExit, status, "%s", output)
			data, err := os.ReadFile(record)
			if test.wantMode == "" {
				require.ErrorIs(t, err, os.ErrNotExist, "qualification must not run before prerequisites pass")
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"1", test.wantMode}, strings.Fields(string(data)))
				require.Contains(t, string(output), "total=1 passed=1 skipped=0 failed=0")
			}
		})
	}
}
