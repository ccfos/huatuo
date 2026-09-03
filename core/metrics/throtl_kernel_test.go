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

//go:build !didi

// Kernel qualifications own disposable disks and cgroups to isolate host devices.
package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	cebpf "github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Real-kernel fixture bounds and ownership.

const (
	throtlKernelRequiredEnvironment   = "TEST_BLK_THROTL_REQUIRED"
	throtlKernelCgroupRootEnvironment = "TEST_BLK_THROTL_CGROUP_ROOT"
	throtlKernelIOPS                  = 2
	throtlKernelOperations            = 4
	throtlKernelCommandTimeout        = 15 * time.Second
	throtlKernelStateTimeout          = 10 * time.Second
)

type throtlKernelDisk struct {
	uuid   string
	path   string
	device string
	major  uint32
	minor  uint32
}

type throtlKernelFixture struct {
	ctx     context.Context
	backing string
	loop    string
	disk    *throtlKernelDisk
}

type throtlKernelCgroup struct {
	ctx       context.Context
	path      string
	moveFile  string
	device    string
	unified   bool
	removed   bool
	writeFile string
	readFile  string
}

type throtlKernelRows struct {
	aggregate int
	owner     int
}

// Real IO, cgroup retirement, and device session-restart qualification.

// Integration owns the block and cgroup objects. Required mode also fails
// when the kernel lacks blk-throttle, instead of reporting a skipped test.
func TestThrotlKernelThrottleLifecycle(t *testing.T) {
	requireThrotlKernelTest(t)
	fixture := newThrotlKernelFixture(t)
	tracing, session := startThrotlKernelSession(t, fixture.ctx)
	disk := fixture.createDisk(t)
	cgroup := newThrotlKernelCgroup(t, fixture.ctx, disk.device)
	requireThrotlKernelDeviceRestart(t, tracing, session)
	tracing, session = startThrotlKernelSession(t, fixture.ctx)

	// Establish the initial interval, then prove ordinary direct IO does not
	// produce a throttle metric.
	metrics, err := tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelMetricAbsent(t, metrics, disk.path, "write")

	runThrotlKernelWorkload(t, cgroup, disk.path, "write", 2)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelMetricAbsent(t, metrics, disk.path, "write")

	// Fixed-rate serial IO gives an exact delayed count and makes total wait
	// comparable with workload wall time. The empty updates prove interval
	// deltas are emitted once.
	cgroup.setIOPS(t, "write", throtlKernelIOPS)
	elapsed := runThrotlKernelWorkload(
		t, cgroup, disk.path, "write", throtlKernelOperations,
	)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(
		t, metrics, disk.path, "write", throtlKernelOperations, elapsed,
	)
	requireThrotlKernelHealthy(t, session)

	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(t, metrics, disk.path, "write", 0, 0)

	// Confirm the first group's counters before removing its directory.
	rows := readThrotlKernelRows(t, session, disk)
	require.Equal(t, 1, rows.aggregate)
	require.Equal(t, 1, rows.owner)
	cgroup.remove(t)
	requireThrotlKernelHealthy(t, session)

	// rmdir does not wait for RCU and pd_free. The old rows may still exist;
	// the replacement must publish its own interval without inheriting them.
	cgroup = newThrotlKernelCgroup(t, fixture.ctx, disk.device)
	cgroup.setIOPS(t, "read", throtlKernelIOPS)
	elapsed = runThrotlKernelWorkload(
		t, cgroup, disk.path, "read", throtlKernelOperations,
	)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(
		t, metrics, disk.path, "read", throtlKernelOperations, elapsed,
	)
	requireThrotlKernelHealthy(t, session)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(t, metrics, disk.path, "read", 0, 0)

	// Removing a disk with a live owner discards the whole session's interval.
	fixture.removeDisk(t)
	requireThrotlKernelDeviceRestart(t, tracing, session)
	cgroup.remove(t)

	// A replacement disk collects fresh data in a new session.
	disk = fixture.createDisk(t)
	cgroup = newThrotlKernelCgroup(t, fixture.ctx, disk.device)
	cgroup.setIOPS(t, "write", throtlKernelIOPS)
	tracing, session = startThrotlKernelSession(t, fixture.ctx)
	elapsed = runThrotlKernelWorkload(t, cgroup, disk.path, "write", 2)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(
		t, metrics, disk.path, "write", 2, elapsed,
	)
	rows = readThrotlKernelRows(t, session, disk)
	require.Equal(t, 1, rows.aggregate)
	require.Equal(t, 1, rows.owner)
	requireThrotlKernelHealthy(t, session)

	cgroup.remove(t)
	fixture.removeDisk(t)
	requireThrotlKernelDeviceRestart(t, tracing, session)
}

// Pending-map capacity failure qualification.

// Fill the test session's pending map so a real throttle admission receives
// E2BIG from the kernel helper. Production BPF must publish the stop reason.
func TestThrotlKernelPendingCapacityFailure(t *testing.T) {
	requireThrotlKernelTest(t)
	fixture := newThrotlKernelFixture(t)
	disk := fixture.createDisk(t)
	cgroup := newThrotlKernelCgroup(t, fixture.ctx, disk.device)
	cgroup.setIOPS(t, "write", throtlKernelIOPS)
	hooks, err := loadThrotlHooks()
	if errors.Is(err, types.ErrNotSupported) && os.Getenv(throtlKernelRequiredEnvironment) != "1" {
		t.Skip(err)
	}
	require.NoError(t, err)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })
	object, err := loadIOControlBPF("throtl_tracing.o", hooks.constants())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, object.Close()) })

	// Fill before attaching: unrelated throttle admissions must not consume
	// slots during setup. Once attached, any real admission must report E2BIG.
	items := make([]bpf.MapItem, throtlPendingEntries)
	for index := range items {
		items[index] = bpf.MapItem{
			Key:   ioControlTestBytes(uint64(index + 1)),
			Value: ioControlTestBytes(uint64(0)),
		}
	}
	mapID := object.MapIDByName(throtlPendingMap)
	require.NotZero(t, mapID)
	require.NoError(t, object.WriteMapItems(mapID, items))
	require.NoError(t, attachThrotlHooks(object))
	runThrotlKernelWorkload(t, cgroup, disk.path, "write", 1)

	status, err := readIOControlStatus(object)
	require.NoError(t, err)
	require.Equal(t, ioControlTestStatus(throtlFailurePendingInsert, -int32(unix.E2BIG)), status)
	require.ErrorIs(t, throtlFailure(status.Failure), types.ErrTracingStopped)
	require.ErrorContains(t, throtlFailure(status.Failure), "capacity exhausted (10240 entries)")
}

// Disposable device, cgroup, and workload helpers.

func requireThrotlKernelTest(t *testing.T) {
	t.Helper()
	required := os.Getenv(throtlKernelRequiredEnvironment)
	if required == "" && os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("run make integration for live blk-throttle qualification")
	}
	if required != "" && required != "1" {
		t.Fatalf("%s must be exactly 1, got %q",
			throtlKernelRequiredEnvironment, required)
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatalf("blk-throttle qualification requires linux/amd64, got %s/%s",
			runtime.GOOS, runtime.GOARCH)
	}
	if os.Geteuid() != 0 {
		t.Fatal("blk-throttle qualification requires root")
	}
	for _, command := range []string{"bash", "dmsetup", "fio", "losetup"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("blk-throttle qualification requires %s: %v", command, err)
		}
	}
	output := runIOControlKernelCommand(t, t.Context(), time.Second, "fio", "--version")
	if !strings.HasPrefix(output, "fio-") {
		t.Fatalf("unexpected fio implementation: %q", output)
	}
	// Each qualification runs in its own process and must initialize BPF.
	require.NoError(t, bpf.Init(&bpf.Option{}))
	t.Cleanup(bpf.Shutdown)
}

func startThrotlKernelSession(
	t *testing.T,
	parent context.Context,
) (*throtlTracing, *throtlSession) {
	t.Helper()
	hooks, err := loadThrotlHooks()
	if errors.Is(err, types.ErrNotSupported) && os.Getenv(throtlKernelRequiredEnvironment) != "1" {
		t.Skip(err)
	}
	require.NoError(t, err)
	possibleCPUs, err := cebpf.PossibleCPU()
	require.NoError(t, err)

	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	tracing := &throtlTracing{}
	go func() {
		done <- tracing.startWithAttribution(
			ctx,
			loadIOControlBPF,
			hooks,
			possibleCPUs,
			func() (map[string]*pod.Container, error) { return nil, nil },
		)
	}()

	finished := false
	t.Cleanup(func() {
		cancel()
		if !finished {
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("stop blk-throttle session: %v", err)
				}
			case <-time.After(throtlKernelStateTimeout):
				t.Error("timeout stopping blk-throttle session")
			}
		}
		bpf.DefaultObjDir = oldDir
	})

	var session *throtlSession
	waitIOControlKernelState(t, ctx, "blk-throttle session publication", func() bool {
		select {
		case err := <-done:
			finished = true
			if err != nil {
				t.Fatalf("start blk-throttle session: %v", err)
			}
			t.Fatal("blk-throttle session stopped before publication")
		default:
		}
		tracing.mu.Lock()
		defer tracing.mu.Unlock()
		session = tracing.session
		return session != nil
	})
	return tracing, session
}

func newThrotlKernelFixture(t *testing.T) *throtlKernelFixture {
	t.Helper()
	ctx := ioControlKernelContext(t)
	directory := t.TempDir()
	backing := filepath.Join(directory, "backing.img")
	file, err := os.OpenFile(backing, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(64<<20))
	require.NoError(t, file.Close())
	// Setup can fail after binding a loop. Keep its private backing identity
	// available to cleanup even when no device name is returned.
	fixture := &throtlKernelFixture{ctx: ctx, backing: backing}
	t.Cleanup(func() { fixture.cleanup(t) })
	t.Logf("blk-throttle owned resources: backing=%s", backing)
	fixture.loop = runIOControlKernelCommand(
		t, ctx, throtlKernelCommandTimeout,
		"losetup", "--find", "--show", backing,
	)
	return fixture
}

func (f *throtlKernelFixture) createDisk(t *testing.T) *throtlKernelDisk {
	t.Helper()
	require.NoError(t, f.ctx.Err())
	require.Nil(t, f.disk)
	name := fmt.Sprintf("huatuo-blk_throtl-%d", os.Getpid())
	table := fmt.Sprintf("0 %d linear %s 0", (64<<20)/512, f.loop)
	// Creation can time out after DM exists. Register ownership before the
	// command and remove by UUID so a name collision cannot remove another disk.
	disk := &throtlKernelDisk{
		uuid: fmt.Sprintf("HUATUO-BLK-THROTL-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}
	f.disk = disk
	t.Logf("blk-throttle owned DM: name=%s uuid=%s", name, disk.uuid)
	runIOControlKernelCommand(
		t, f.ctx, throtlKernelCommandTimeout,
		"dmsetup", "create", name, "--uuid", disk.uuid, "--table", table,
	)
	info := runIOControlKernelCommand(
		t, f.ctx, throtlKernelCommandTimeout,
		"dmsetup", "info", "--columns", "--noheadings", "--separator", ":",
		"-o", "major,minor", name,
	)
	fields := strings.Split(info, ":")
	if len(fields) != 2 {
		t.Fatalf("decode dm device identity %q", info)
	}
	major, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 32)
	require.NoError(t, err)
	minor, err := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 32)
	require.NoError(t, err)
	path := fmt.Sprintf("/dev/dm-%d", minor)
	waitIOControlKernelState(t, f.ctx, "dm device node", func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
	disk.path = path
	disk.device = fmt.Sprintf("%d:%d", major, minor)
	disk.major = uint32(major)
	disk.minor = uint32(minor)
	return disk
}

func (f *throtlKernelFixture) removeDisk(t *testing.T) {
	t.Helper()
	require.NotNil(t, f.disk)
	runIOControlKernelCommand(
		t, f.ctx, throtlKernelCommandTimeout,
		"dmsetup", "remove", "--retry", "--uuid", f.disk.uuid,
	)
	f.disk = nil
}

func (f *throtlKernelFixture) cleanup(t *testing.T) {
	t.Helper()
	if f.disk != nil {
		if err := cleanupIOControlKernelCommand(
			"dmsetup", "remove", "--retry", "--uuid", f.disk.uuid,
		); err != nil {
			t.Errorf("remove test dm device: %v", err)
		}
		f.disk = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), throtlKernelCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "losetup",
		"--associated", f.backing, "--list", "--noheadings", "--output", "NAME")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Errorf("find loop for test backing %s: %v: %s",
			f.backing, err, strings.TrimSpace(stderr.String()))
		return
	}
	// Only current associations with our private backing may be detached.
	for _, loop := range strings.Fields(string(output)) {
		if err := cleanupIOControlKernelCommand("losetup", "-d", loop); err != nil {
			t.Errorf("detach test loop: %v", err)
		}
	}
	f.loop = ""
}

func newThrotlKernelCgroup(
	t *testing.T,
	ctx context.Context,
	device string,
) *throtlKernelCgroup {
	t.Helper()
	require.NoError(t, ctx.Err())
	root := os.Getenv(throtlKernelCgroupRootEnvironment)
	if root == "" {
		root = "/sys/fs/cgroup/blkio"
		if _, err := os.Stat(filepath.Join(
			root, "blkio.throttle.write_iops_device",
		)); err != nil {
			root = "/sys/fs/cgroup"
		}
	}
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	if controllers, err := os.ReadFile(filepath.Join(root, "cgroup.controllers")); err == nil {
		if !slices.Contains(strings.Fields(string(controllers)), "io") {
			if os.Getenv(throtlKernelRequiredEnvironment) == "1" {
				t.Fatal("cgroup v2 io controller is unavailable")
			}
			t.Skip("cgroup v2 io controller is unavailable")
		}
		control := filepath.Join(root, "cgroup.subtree_control")
		enabled, err := os.ReadFile(control)
		require.NoError(t, err)
		if !slices.Contains(strings.Fields(string(enabled)), "io") {
			require.NoError(t, os.WriteFile(control, []byte("+io\n"), 0o600))
			// Remove our delegation only after the owned child has been removed.
			t.Cleanup(func() { require.NoError(t, os.WriteFile(control, []byte("-io\n"), 0o600)) })
		}
	}
	name := fmt.Sprintf("huatuo-blk_throtl-%d-%d", os.Getpid(), time.Now().UnixNano())
	path := filepath.Join(root, name)
	require.NoError(t, os.Mkdir(path, 0o755))

	cgroup := &throtlKernelCgroup{ctx: ctx, path: path, device: device}
	if _, err := os.Stat(filepath.Join(path, "io.max")); err == nil {
		cgroup.unified = true
		cgroup.moveFile = filepath.Join(path, "cgroup.procs")
		cgroup.writeFile = filepath.Join(path, "io.max")
		cgroup.readFile = cgroup.writeFile
	} else {
		cgroup.moveFile = filepath.Join(path, "tasks")
		cgroup.writeFile = filepath.Join(
			path, "blkio.throttle.write_iops_device",
		)
		cgroup.readFile = filepath.Join(
			path, "blkio.throttle.read_iops_device",
		)
	}
	for _, file := range []string{
		cgroup.moveFile, cgroup.writeFile, cgroup.readFile,
	} {
		if _, err := os.Stat(file); err != nil {
			_ = os.Remove(path)
			t.Fatalf("cgroup IO controller is unavailable at %s: %v", file, err)
		}
	}
	t.Cleanup(func() { cgroup.cleanup(t) })
	t.Logf("blk-throttle owned cgroup: %s", path)
	return cgroup
}

func (c *throtlKernelCgroup) setIOPS(
	t *testing.T,
	operation string,
	iops int,
) {
	t.Helper()
	require.NoError(t, c.ctx.Err())
	file := c.writeFile
	key := "wiops"
	if operation == "read" {
		file = c.readFile
		key = "riops"
	}
	value := fmt.Sprintf("%s %d\n", c.device, iops)
	if c.unified {
		value = fmt.Sprintf("%s %s=%d\n", c.device, key, iops)
	}
	require.NoError(t, os.WriteFile(file, []byte(value), 0o600))
}

func (c *throtlKernelCgroup) cleanup(t *testing.T) {
	t.Helper()
	if c.removed {
		return
	}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		t.Errorf("remove test cgroup %s: %v", c.path, err)
	}
	c.removed = true
}

func (c *throtlKernelCgroup) remove(t *testing.T) {
	t.Helper()
	require.False(t, c.removed)
	require.NoError(t, os.Remove(c.path))
	c.removed = true
}

func runThrotlKernelWorkload(
	t *testing.T,
	cgroup *throtlKernelCgroup,
	device string,
	operation string,
	operations int,
) time.Duration {
	t.Helper()
	if operation != "read" && operation != "write" {
		t.Fatalf("unsupported workload operation %q", operation)
	}
	arguments := []string{
		"-c", `printf '%s\n' "$$" > "$1"; shift; exec "$@"`,
		"blk_throtl-workload", cgroup.moveFile,
		"fio",
		"--name=blk_throtl",
		"--filename=" + device,
		"--rw=" + operation,
		"--bs=4096",
		"--size=" + strconv.Itoa(operations*4096),
		"--ioengine=sync",
		"--iodepth=1",
		"--direct=1",
		"--numjobs=1",
		"--thread=1",
		"--eta=never",
		"--output-format=json",
	}
	output := runIOControlKernelCommand(
		t, cgroup.ctx, throtlKernelCommandTimeout,
		"bash", arguments...,
	)
	// Compare wait time with the IO job, excluding fio startup and calibration.
	var result struct {
		Jobs []struct {
			RuntimeMS int64 `json:"job_runtime"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &result))
	require.Len(t, result.Jobs, 1)
	return time.Duration(result.Jobs[0].RuntimeMS) * time.Millisecond
}

func requireThrotlKernelMetricAbsent(
	t *testing.T,
	metrics []*metric.Data,
	device string,
	operation string,
) {
	t.Helper()
	for _, data := range metrics {
		got := data
		if (got.Name() == throtlDelayedCountName ||
			got.Name() == throtlAverageWaitName) &&
			got.Labels()["disk"] == filepath.Base(device) &&
			got.Labels()["operation"] == operation {
			t.Fatalf("unexpected blk-throttle metric for %s %s: %+v",
				device, operation, got)
		}
	}
}

func requireThrotlKernelInterval(
	t *testing.T,
	metrics []*metric.Data,
	device string,
	operation string,
	wantCount int,
	elapsed time.Duration,
) {
	t.Helper()
	values := make(map[string]map[string]float64)
	for _, data := range metrics {
		got := data
		if got.Labels()["disk"] != filepath.Base(device) ||
			got.Labels()["operation"] != operation {
			continue
		}
		if got.Name() != throtlDelayedCountName &&
			got.Name() != throtlAverageWaitName {
			continue
		}
		scope := got.Labels()["scope"]
		if values[scope] == nil {
			values[scope] = make(map[string]float64)
		}
		values[scope][got.Name()] = got.Value
	}
	for _, scope := range []string{ioControlHostScope, ioControlOtherScope} {
		require.Contains(t, values, scope)
		require.Contains(t, values[scope], throtlDelayedCountName)
		require.Contains(t, values[scope], throtlAverageWaitName)
		require.Equal(t, float64(wantCount),
			values[scope][throtlDelayedCountName])
		if wantCount == 0 {
			require.Zero(t, values[scope][throtlAverageWaitName])
		}
	}
	hostAverage := values[ioControlHostScope][throtlAverageWaitName]
	require.Equal(t, hostAverage,
		values[ioControlOtherScope][throtlAverageWaitName])
	if wantCount == 0 {
		return
	}
	require.Greater(t, hostAverage, float64(100),
		"2 IOPS must produce an observable throttle wait")
	accountedMS := hostAverage * float64(wantCount)
	elapsedMS := float64(elapsed.Microseconds()) / 1000
	require.GreaterOrEqual(t, accountedMS, elapsedMS*0.5)
	require.LessOrEqual(t, accountedMS, elapsedMS+250)
}

func requireThrotlKernelHealthy(
	t *testing.T,
	session *throtlSession,
) {
	t.Helper()
	status, err := readIOControlStatus(session.object)
	require.NoError(t, err)
	require.Equal(t, ioControlStatus{}, status)
}

func readThrotlKernelRows(
	t *testing.T,
	session *throtlSession,
	disk *throtlKernelDisk,
) throtlKernelRows {
	t.Helper()
	var rows throtlKernelRows

	aggregates, err := session.object.DumpMapByName(throtlWaitAggregateMap)
	require.NoError(t, err)
	for _, item := range aggregates {
		var key ioControlWaitKey
		require.NoError(t, decodeBPFMapData(item.Key, &key))
		if key.Major == disk.major && key.Minor == disk.minor {
			rows.aggregate++
		}
	}

	owners, err := session.object.DumpMapByName(throtlBLKGOwnerMap)
	require.NoError(t, err)
	for _, item := range owners {
		var owner throtlTestBLKGOwner
		require.NoError(t, decodeBPFMapData(item.Value, &owner))
		if owner.Major == disk.major && owner.Minor == disk.minor {
			rows.owner++
		}
	}

	// Pending entries contain only bio addresses and timestamps, not disk
	// identities. Other devices' in-flight waits are not this fixture's state.
	return rows
}

func requireThrotlKernelDeviceRestart(
	t *testing.T,
	tracing *throtlTracing,
	session *throtlSession,
) {
	t.Helper()
	waitIOControlKernelState(t, session.breaker, "block device change", func() bool {
		err := checkIOControlDeviceChanges(session.object)
		if errors.Is(err, errIOControlDevicesChanged) {
			return true
		}
		require.NoError(t, err)
		return false
	})
	metrics, err := tracing.Update()
	require.ErrorIs(t, err, errIOControlDevicesChanged)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Empty(t, metrics)
	waitIOControlKernelState(t, t.Context(), "blk-throttle session withdrawal", func() bool {
		tracing.mu.Lock()
		defer tracing.mu.Unlock()
		return tracing.session == nil
	})
}
