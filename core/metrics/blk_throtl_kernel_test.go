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
	blkThrotlKernelRequiredEnvironment   = "TEST_BLK_THROTL_REQUIRED"
	blkThrotlKernelCgroupRootEnvironment = "TEST_BLK_THROTL_CGROUP_ROOT"
	blkThrotlKernelIOPS                  = 2
	blkThrotlKernelOperations            = 4
	blkThrotlKernelCommandTimeout        = 15 * time.Second
	blkThrotlKernelStateTimeout          = 10 * time.Second
)

type blkThrotlKernelDisk struct {
	uuid   string
	path   string
	device string
	major  uint32
	minor  uint32
}

type blkThrotlKernelFixture struct {
	ctx     context.Context
	backing string
	loop    string
	disk    *blkThrotlKernelDisk
}

type blkThrotlKernelCgroup struct {
	ctx       context.Context
	path      string
	moveFile  string
	device    string
	unified   bool
	removed   bool
	writeFile string
	readFile  string
}

type blkThrotlKernelRows struct {
	aggregate int
	owner     int
}

// Real IO, cgroup retirement, and device session-restart qualification.

// Integration owns the block and cgroup objects. Required mode also fails
// when the kernel lacks blk-throttle, instead of reporting a skipped test.
func TestBlkThrotlKernelThrottleLifecycle(t *testing.T) {
	requireBlkThrotlKernelTest(t)
	fixture := newBlkThrotlKernelFixture(t)
	tracing, session := startBlkThrotlKernelSession(t, fixture.ctx)
	disk := fixture.createDisk(t)
	cgroup := newBlkThrotlKernelCgroup(t, fixture.ctx, disk.device)
	requireBlkThrotlKernelDeviceRestart(t, tracing, session)
	tracing, session = startBlkThrotlKernelSession(t, fixture.ctx)

	// Establish the initial interval, then prove ordinary direct IO does not
	// produce a blk-throttle metric.
	metrics, err := tracing.Update()
	require.NoError(t, err)
	requireBlkThrotlKernelMetricAbsent(t, metrics, disk.path, "write")

	runBlkThrotlKernelWorkload(t, cgroup, disk.path, "write", 2)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireBlkThrotlKernelMetricAbsent(t, metrics, disk.path, "write")

	// Fixed-rate serial IO gives an exact delayed count and makes total wait
	// comparable with workload wall time. The empty updates prove interval
	// deltas are emitted once.
	cgroup.setIOPS(t, "write", blkThrotlKernelIOPS)
	elapsed := runBlkThrotlKernelWorkload(
		t, cgroup, disk.path, "write", blkThrotlKernelOperations,
	)
	rows := readBlkThrotlKernelRows(t, session, disk)
	require.Equal(t, 1, rows.aggregate)
	require.Equal(t, 1, rows.owner)
	aggregates, err := session.object.DumpMapByName(blkThrotlWaitAggregateMap)
	require.NoError(t, err)
	var retiredIdentity ioControlRetiredKey
	for _, item := range aggregates {
		var key ioControlWaitKey
		require.NoError(t, decodeBPFMapData(item.Key, &key))
		if key.Major == disk.major && key.Minor == disk.minor && key.Operation == 1 {
			retiredIdentity = ioControlRetiredKey{
				CSS: key.CSS, CSSSerial: key.CSSSerial, Major: key.Major, Minor: key.Minor,
			}
		}
	}
	require.NotZero(t, retiredIdentity.CSS)
	cgroup.remove(t)
	// pd_free publishes the marker before deleting its owner. Observe owner
	// removal first, so a missing marker fails instead of looking unreleased.
	isRetired := false
	t.Run("retired tail", func(t *testing.T) {
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			require.NoError(t, fixture.ctx.Err())
			owners, err := session.hashReader.dump(session.object,
				session.object.MapIDByName(blkThrotlBLKGOwnerMap))
			require.NoError(t, err)
			hasOldOwner := false
			for _, item := range owners {
				var owner blkThrotlTestBLKGOwner
				require.NoError(t, decodeBPFMapData(item.Value, &owner))
				if ioControlRetiredKey(owner) == retiredIdentity {
					hasOldOwner = true
					break
				}
			}
			if !hasOldOwner {
				retired, err := captureIOControlRetired(session.object, blkThrotlRetiredMap)
				require.NoError(t, err)
				require.Contains(t, retired, retiredIdentity, "released owner must publish its retirement marker")
				isRetired = true
				rows = readBlkThrotlKernelRows(t, session, disk)
				require.Equal(t, 1, rows.aggregate, "release must retain uncollected cumulative counters")
				require.Zero(t, rows.owner, "released pointers must not retain an owner")
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Skip("old owner is still present; blk-throttle pd_free has not completed within the observation window")
	})
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireBlkThrotlKernelInterval(
		t, metrics, disk.path, "write", blkThrotlKernelOperations, elapsed,
	)
	requireBlkThrotlKernelHealthy(t, session)

	metrics, err = tracing.Update()
	require.NoError(t, err)
	if isRetired {
		requireBlkThrotlKernelMetricAbsent(t, metrics, disk.path, "write")
		rows = readBlkThrotlKernelRows(t, session, disk)
		require.Zero(t, rows.aggregate, "successful scrape must reclaim the retired counters")
	} else {
		for _, data := range metrics {
			require.Zero(t, data.Value, "no interval may repeat the already consumed waits")
		}
	}
	requireBlkThrotlKernelHealthy(t, session)

	// The replacement must publish its own interval without inheriting the
	// retired generation's consumed counters.
	cgroup = newBlkThrotlKernelCgroup(t, fixture.ctx, disk.device)
	cgroup.setIOPS(t, "read", blkThrotlKernelIOPS)
	elapsed = runBlkThrotlKernelWorkload(
		t, cgroup, disk.path, "read", blkThrotlKernelOperations,
	)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireBlkThrotlKernelInterval(
		t, metrics, disk.path, "read", blkThrotlKernelOperations, elapsed,
	)
	requireBlkThrotlKernelHealthy(t, session)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireBlkThrotlKernelInterval(t, metrics, disk.path, "read", 0, 0)

	// Removing a disk with a live owner discards the whole session's interval.
	fixture.removeDisk(t)
	requireBlkThrotlKernelDeviceRestart(t, tracing, session)
	cgroup.remove(t)

	// A replacement disk collects fresh data in a new session.
	disk = fixture.createDisk(t)
	cgroup = newBlkThrotlKernelCgroup(t, fixture.ctx, disk.device)
	cgroup.setIOPS(t, "write", blkThrotlKernelIOPS)
	tracing, session = startBlkThrotlKernelSession(t, fixture.ctx)
	elapsed = runBlkThrotlKernelWorkload(t, cgroup, disk.path, "write", 2)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireBlkThrotlKernelInterval(
		t, metrics, disk.path, "write", 2, elapsed,
	)
	rows = readBlkThrotlKernelRows(t, session, disk)
	require.Equal(t, 1, rows.aggregate)
	require.Equal(t, 1, rows.owner)
	requireBlkThrotlKernelHealthy(t, session)

	cgroup.remove(t)
	fixture.removeDisk(t)
	requireBlkThrotlKernelDeviceRestart(t, tracing, session)
}

// Pending-map capacity failure qualification.

// Fill the test session's pending map so a real blk-throttle admission receives
// E2BIG from the kernel helper. Production BPF must publish the stop reason.
func TestBlkThrotlKernelPendingCapacityFailure(t *testing.T) {
	requireBlkThrotlKernelTest(t)
	fixture := newBlkThrotlKernelFixture(t)
	disk := fixture.createDisk(t)
	cgroup := newBlkThrotlKernelCgroup(t, fixture.ctx, disk.device)
	cgroup.setIOPS(t, "write", blkThrotlKernelIOPS)
	hooks, err := loadBlkThrotlHooks()
	if errors.Is(err, types.ErrNotSupported) && os.Getenv(blkThrotlKernelRequiredEnvironment) != "1" {
		t.Skip(err)
	}
	require.NoError(t, err)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })
	object, err := loadIOControlBPF("blk_throtl_tracing.o", hooks.constants())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, object.Close()) })

	// Fill before attaching: unrelated blk-throttle admissions must not consume
	// slots during setup. Once attached, any real admission must report E2BIG.
	items := make([]bpf.MapItem, blkThrotlPendingEntries)
	for index := range items {
		items[index] = bpf.MapItem{
			Key:   ioControlTestBytes(uint64(index + 1)),
			Value: ioControlTestBytes(uint64(0)),
		}
	}
	mapID := object.MapIDByName(blkThrotlPendingMap)
	require.NotZero(t, mapID)
	require.NoError(t, object.WriteMapItems(mapID, items))
	require.NoError(t, attachBlkThrotlHooks(object))
	runBlkThrotlKernelWorkload(t, cgroup, disk.path, "write", 1)

	status, err := readIOControlStatus(object)
	require.NoError(t, err)
	require.Equal(t, ioControlTestStatus(blkThrotlFailurePendingInsert, -int32(unix.E2BIG)), status)
	require.ErrorIs(t, blkThrotlFailure(status.Failure), types.ErrTracingStopped)
	require.ErrorContains(t, blkThrotlFailure(status.Failure), "capacity exhausted (10240 entries)")
}

// Disposable device, cgroup, and workload helpers.

func requireBlkThrotlKernelTest(t *testing.T) {
	t.Helper()
	required := os.Getenv(blkThrotlKernelRequiredEnvironment)
	if required == "" && os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("run make integration for live blk-throttle qualification")
	}
	if required != "" && required != "1" {
		t.Fatalf("%s must be exactly 1, got %q",
			blkThrotlKernelRequiredEnvironment, required)
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

func startBlkThrotlKernelSession(
	t *testing.T,
	parent context.Context,
) (*blkThrotlTracing, *blkThrotlSession) {
	t.Helper()
	hooks, err := loadBlkThrotlHooks()
	if errors.Is(err, types.ErrNotSupported) && os.Getenv(blkThrotlKernelRequiredEnvironment) != "1" {
		t.Skip(err)
	}
	require.NoError(t, err)
	possibleCPUs, err := cebpf.PossibleCPU()
	require.NoError(t, err)

	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	tracing := &blkThrotlTracing{}
	go func() {
		done <- tracing.startWithAttribution(
			ctx,
			loadIOControlBPF,
			hooks,
			possibleCPUs,
			func() (map[string]*pod.Container, error) { return nil, nil },
			ioControlAttachRetryInterval,
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
			case <-time.After(blkThrotlKernelStateTimeout):
				t.Error("timeout stopping blk-throttle session")
			}
		}
		bpf.DefaultObjDir = oldDir
	})

	var session *blkThrotlSession
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

func newBlkThrotlKernelFixture(t *testing.T) *blkThrotlKernelFixture {
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
	fixture := &blkThrotlKernelFixture{ctx: ctx, backing: backing}
	t.Cleanup(func() { fixture.cleanup(t) })
	t.Logf("blk-throttle owned resources: backing=%s", backing)
	fixture.loop = runIOControlKernelCommand(
		t, ctx, blkThrotlKernelCommandTimeout,
		"losetup", "--find", "--show", backing,
	)
	return fixture
}

func (f *blkThrotlKernelFixture) createDisk(t *testing.T) *blkThrotlKernelDisk {
	t.Helper()
	require.NoError(t, f.ctx.Err())
	require.Nil(t, f.disk)
	name := fmt.Sprintf("huatuo-blk_throtl-%d", os.Getpid())
	table := fmt.Sprintf("0 %d linear %s 0", (64<<20)/512, f.loop)
	// Creation can time out after DM exists. Register ownership before the
	// command and remove by UUID so a name collision cannot remove another disk.
	disk := &blkThrotlKernelDisk{
		uuid: fmt.Sprintf("HUATUO-BLK-THROTL-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}
	f.disk = disk
	t.Logf("blk-throttle owned DM: name=%s uuid=%s", name, disk.uuid)
	runIOControlKernelCommand(
		t, f.ctx, blkThrotlKernelCommandTimeout,
		"dmsetup", "create", name, "--uuid", disk.uuid, "--table", table,
	)
	info := runIOControlKernelCommand(
		t, f.ctx, blkThrotlKernelCommandTimeout,
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

func (f *blkThrotlKernelFixture) removeDisk(t *testing.T) {
	t.Helper()
	require.NotNil(t, f.disk)
	runIOControlKernelCommand(
		t, f.ctx, blkThrotlKernelCommandTimeout,
		"dmsetup", "remove", "--retry", "--uuid", f.disk.uuid,
	)
	f.disk = nil
}

func (f *blkThrotlKernelFixture) cleanup(t *testing.T) {
	t.Helper()
	if f.disk != nil {
		if err := cleanupIOControlKernelCommand(
			"dmsetup", "remove", "--retry", "--uuid", f.disk.uuid,
		); err != nil {
			t.Errorf("remove test dm device: %v", err)
		}
		f.disk = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), blkThrotlKernelCommandTimeout)
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

func newBlkThrotlKernelCgroup(
	t *testing.T,
	ctx context.Context,
	device string,
) *blkThrotlKernelCgroup {
	t.Helper()
	require.NoError(t, ctx.Err())
	root := os.Getenv(blkThrotlKernelCgroupRootEnvironment)
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
			if os.Getenv(blkThrotlKernelRequiredEnvironment) == "1" {
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

	cgroup := &blkThrotlKernelCgroup{ctx: ctx, path: path, device: device}
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

func (c *blkThrotlKernelCgroup) setIOPS(
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

func (c *blkThrotlKernelCgroup) cleanup(t *testing.T) {
	t.Helper()
	if c.removed {
		return
	}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		t.Errorf("remove test cgroup %s: %v", c.path, err)
	}
	c.removed = true
}

func (c *blkThrotlKernelCgroup) remove(t *testing.T) {
	t.Helper()
	require.False(t, c.removed)
	require.NoError(t, os.Remove(c.path))
	c.removed = true
}

func runBlkThrotlKernelWorkload(
	t *testing.T,
	cgroup *blkThrotlKernelCgroup,
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
		t, cgroup.ctx, blkThrotlKernelCommandTimeout,
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

func requireBlkThrotlKernelMetricAbsent(
	t *testing.T,
	metrics []*metric.Data,
	device string,
	operation string,
) {
	t.Helper()
	for _, data := range metrics {
		got := data
		if (got.Name() == blkThrotlDelayedCountName ||
			got.Name() == blkThrotlAverageWaitName) &&
			got.Labels()["disk"] == filepath.Base(device) &&
			got.Labels()["operation"] == operation {
			t.Fatalf("unexpected blk-throttle metric for %s %s: %+v",
				device, operation, got)
		}
	}
}

func requireBlkThrotlKernelInterval(
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
		if got.Name() != blkThrotlDelayedCountName &&
			got.Name() != blkThrotlAverageWaitName {
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
		require.Contains(t, values[scope], blkThrotlDelayedCountName)
		require.Contains(t, values[scope], blkThrotlAverageWaitName)
		require.Equal(t, float64(wantCount),
			values[scope][blkThrotlDelayedCountName])
		if wantCount == 0 {
			require.Zero(t, values[scope][blkThrotlAverageWaitName])
		}
	}
	hostAverage := values[ioControlHostScope][blkThrotlAverageWaitName]
	require.Equal(t, hostAverage,
		values[ioControlOtherScope][blkThrotlAverageWaitName])
	if wantCount == 0 {
		return
	}
	require.Greater(t, hostAverage, float64(100),
		"2 IOPS must produce an observable blk-throttle wait")
	accountedMS := hostAverage * float64(wantCount)
	elapsedMS := float64(elapsed.Microseconds()) / 1000
	require.GreaterOrEqual(t, accountedMS, elapsedMS*0.5)
	require.LessOrEqual(t, accountedMS, elapsedMS+250)
}

func requireBlkThrotlKernelHealthy(
	t *testing.T,
	session *blkThrotlSession,
) {
	t.Helper()
	status, err := readIOControlStatus(session.object)
	require.NoError(t, err)
	require.Equal(t, ioControlStatus{}, status)
}

func readBlkThrotlKernelRows(
	t *testing.T,
	session *blkThrotlSession,
	disk *blkThrotlKernelDisk,
) blkThrotlKernelRows {
	t.Helper()
	var rows blkThrotlKernelRows

	aggregates, err := session.object.DumpMapByName(blkThrotlWaitAggregateMap)
	require.NoError(t, err)
	for _, item := range aggregates {
		var key ioControlWaitKey
		require.NoError(t, decodeBPFMapData(item.Key, &key))
		if key.Major == disk.major && key.Minor == disk.minor {
			rows.aggregate++
		}
	}

	owners, err := session.object.DumpMapByName(blkThrotlBLKGOwnerMap)
	require.NoError(t, err)
	for _, item := range owners {
		var owner blkThrotlTestBLKGOwner
		require.NoError(t, decodeBPFMapData(item.Value, &owner))
		if owner.Major == disk.major && owner.Minor == disk.minor {
			rows.owner++
		}
	}

	// Pending entries contain only bio addresses and timestamps, not disk
	// identities. Other devices' in-flight waits are not this fixture's state.
	return rows
}

func requireBlkThrotlKernelDeviceRestart(
	t *testing.T,
	tracing *blkThrotlTracing,
	session *blkThrotlSession,
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
