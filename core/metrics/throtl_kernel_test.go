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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	name   string
	path   string
	device string
	major  uint32
	minor  uint32
}

type throtlKernelFixture struct {
	loop string
	disk *throtlKernelDisk
}

type throtlKernelCgroup struct {
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
	pending   int
	td        int
}

func (r throtlKernelRows) equal(other throtlKernelRows) bool {
	return r.aggregate == other.aggregate &&
		r.owner == other.owner &&
		r.pending == other.pending &&
		r.td == other.td
}

// Real IO and object-retirement qualification.

// TestThrotlKernelThrottleLifecycle is opt-in because it needs root and
// creates kernel block and cgroup objects. Required mode fails on a missing
// prerequisite instead of reporting an unsupported environment as a pass.
func TestThrotlKernelThrottleLifecycle(t *testing.T) {
	requireThrotlKernelTest(t)
	tracing, session := startThrotlKernelSession(t)
	fixture := newThrotlKernelFixture(t)
	disk := fixture.createDisk(t)
	cgroup := newThrotlKernelCgroup(t, disk.device)

	// Establish the initial interval, then prove ordinary direct IO does not
	// produce a throttle metric.
	metrics, err := tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelMetricAbsent(t, metrics, disk.device, "write")

	runThrotlKernelWorkload(t, cgroup, disk.path, "write", 2)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelMetricAbsent(t, metrics, disk.device, "write")

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
		t, metrics, disk.device, "write", throtlKernelOperations, elapsed,
	)
	requireThrotlKernelHealthy(t, session)

	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(t, metrics, disk.device, "write", 0, 0)

	// Removing a blkg must delete its aggregate and owner rows while leaving
	// the disk active. This write-only owner also proves that its absent read
	// aggregate is an idempotent delete.
	td := requireThrotlKernelActiveTD(t, session, disk)
	require.Equal(t, throtlKernelRows{
		aggregate: 1,
		owner:     1,
		td:        1,
	}, readThrotlKernelRows(t, session, td))
	cgroup.remove(t)
	waitThrotlKernelRows(t, session, td, throtlKernelRows{td: 1})
	requireThrotlKernelHealthy(t, session)

	// A replacement cgroup must start from an empty counter and attribute a
	// different operation without inheriting the removed owner's interval.
	cgroup = newThrotlKernelCgroup(t, disk.device)
	cgroup.setIOPS(t, "read", throtlKernelIOPS)
	elapsed = runThrotlKernelWorkload(
		t, cgroup, disk.path, "read", throtlKernelOperations,
	)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(
		t, metrics, disk.device, "read", throtlKernelOperations, elapsed,
	)
	require.Equal(t, throtlKernelRows{
		aggregate: 1,
		owner:     1,
		td:        1,
	}, readThrotlKernelRows(t, session, td))
	requireThrotlKernelHealthy(t, session)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(t, metrics, disk.device, "read", 0, 0)

	// Removing the dm disk with a live owner retires its td. Update discards
	// that td's completed data and cleans its maps.
	fixture.removeDisk(t)
	waitThrotlKernelTDState(t, session, td, throtlTDRetired)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelMetricAbsent(t, metrics, disk.device, "read")
	waitThrotlKernelRows(t, session, td, throtlKernelRows{})
	requireThrotlKernelHealthy(t, session)
	cgroup.remove(t)

	// A replacement disk with the same public identity must collect fresh data.
	disk = fixture.createDisk(t)
	cgroup = newThrotlKernelCgroup(t, disk.device)
	cgroup.setIOPS(t, "write", throtlKernelIOPS)
	elapsed = runThrotlKernelWorkload(t, cgroup, disk.path, "write", 2)
	metrics, err = tracing.Update()
	require.NoError(t, err)
	requireThrotlKernelInterval(
		t, metrics, disk.device, "write", 2, elapsed,
	)
	newTD := requireThrotlKernelActiveTD(t, session, disk)
	require.Equal(t, throtlKernelRows{
		aggregate: 1,
		owner:     1,
		td:        1,
	}, readThrotlKernelRows(t, session, newTD))
	requireThrotlKernelHealthy(t, session)

	cgroup.remove(t)
	waitThrotlKernelRows(t, session, newTD, throtlKernelRows{td: 1})
	fixture.removeDisk(t)
	waitThrotlKernelTDState(t, session, newTD, throtlTDRetired)
	_, err = tracing.Update()
	require.NoError(t, err)
	require.Equal(t, throtlKernelRows{},
		readThrotlKernelRows(t, session, newTD))
}

// Pending-map capacity failure qualification.

// Fill the test session's pending map so a real throttle admission receives
// E2BIG from the kernel helper. Production BPF must publish the stop reason.
func TestThrotlKernelPendingCapacityFailure(t *testing.T) {
	requireThrotlKernelTest(t)
	_, session := startThrotlKernelSession(t)
	fixture := newThrotlKernelFixture(t)
	disk := fixture.createDisk(t)
	cgroup := newThrotlKernelCgroup(t, disk.device)
	cgroup.setIOPS(t, "write", throtlKernelIOPS)

	items := make([]bpf.MapItem, throtlPendingEntries)
	for index := range items {
		items[index] = bpf.MapItem{
			Key: encodeThrotlTestData(t, throtlPendingKey{
				TD: 1, Bio: uint64(index + 1),
			}),
			Value: encodeThrotlTestData(t, uint64(0)),
		}
	}
	mapID := session.object.MapIDByName(throtlPendingMap)
	require.NotZero(t, mapID)
	require.NoError(t, session.object.WriteMapItems(mapID, items))
	runThrotlKernelWorkload(t, cgroup, disk.path, "write", 1)

	status, err := session.readStatus()
	require.NoError(t, err)
	require.Equal(t, throtlStatus{
		Reason: throtlFailurePendingInsert,
		Errno:  -int32(unix.E2BIG),
	}, status)
	require.ErrorIs(t, status.failure(), types.ErrTracingStopped)
	require.ErrorContains(t, status.failure(), "capacity exhausted (10240 entries)")
}

// Disposable device, cgroup, and workload helpers.

func requireThrotlKernelTest(t *testing.T) {
	t.Helper()
	required, present := os.LookupEnv(throtlKernelRequiredEnvironment)
	if !present || required == "" {
		t.Skip("live blk-throttle kernel qualification was not requested")
	}
	if required != "1" {
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
	output := runThrotlKernelCommand(t, time.Second, "fio", "--version")
	if !strings.HasPrefix(output, "fio-") {
		t.Fatalf("unexpected fio implementation: %q", output)
	}
}

func startThrotlKernelSession(
	t *testing.T,
) (*throtlTracing, *throtlSession) {
	t.Helper()
	hooks, err := loadThrotlHooks()
	require.NoError(t, err)
	possibleCPUs, err := cebpf.PossibleCPU()
	require.NoError(t, err)

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join(filepath.Dir(file), "..", "..", "bpf")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	tracing := &throtlTracing{}
	go func() {
		done <- tracing.startWithAttribution(
			ctx,
			bpf.LoadBPF,
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
	waitThrotlKernelState(t, "blk-throttle session publication", func() bool {
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
	directory := t.TempDir()
	backing := filepath.Join(directory, "backing.img")
	file, err := os.OpenFile(backing, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(64<<20))
	require.NoError(t, file.Close())
	loop := runThrotlKernelCommand(
		t, throtlKernelCommandTimeout,
		"losetup", "--find", "--show", backing,
	)
	fixture := &throtlKernelFixture{loop: loop}
	t.Cleanup(func() { fixture.cleanup(t) })
	return fixture
}

func (f *throtlKernelFixture) createDisk(t *testing.T) *throtlKernelDisk {
	t.Helper()
	require.Nil(t, f.disk)
	name := fmt.Sprintf("huatuo-blk_throtl-%d", os.Getpid())
	table := fmt.Sprintf("0 %d linear %s 0", (64<<20)/512, f.loop)
	runThrotlKernelCommand(
		t, throtlKernelCommandTimeout,
		"dmsetup", "create", name, "--table", table,
	)
	disk := &throtlKernelDisk{name: name}
	f.disk = disk
	info := runThrotlKernelCommand(
		t, throtlKernelCommandTimeout,
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
	waitThrotlKernelState(t, "dm device node", func() bool {
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
	runThrotlKernelCommand(
		t, throtlKernelCommandTimeout,
		"dmsetup", "remove", "--retry", f.disk.name,
	)
	f.disk = nil
}

func (f *throtlKernelFixture) cleanup(t *testing.T) {
	t.Helper()
	if f.disk != nil {
		if err := cleanupThrotlKernelCommand(
			"dmsetup", "remove", "--retry", f.disk.name,
		); err != nil {
			t.Errorf("remove test dm device: %v", err)
		}
		f.disk = nil
	}
	if f.loop != "" {
		if err := cleanupThrotlKernelCommand("losetup", "-d", f.loop); err != nil {
			t.Errorf("detach test loop: %v", err)
		}
		f.loop = ""
	}
}

func newThrotlKernelCgroup(
	t *testing.T,
	device string,
) *throtlKernelCgroup {
	t.Helper()
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
	name := fmt.Sprintf("huatuo-blk_throtl-%d-%d", os.Getpid(), time.Now().UnixNano())
	path := filepath.Join(root, name)
	require.NoError(t, os.Mkdir(path, 0o755))

	cgroup := &throtlKernelCgroup{path: path, device: device}
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
	return cgroup
}

func (c *throtlKernelCgroup) setIOPS(
	t *testing.T,
	operation string,
	iops int,
) {
	t.Helper()
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
	}
	started := time.Now()
	runThrotlKernelCommand(
		t, throtlKernelCommandTimeout,
		"bash", arguments...,
	)
	return time.Since(started)
}

func requireThrotlKernelMetricAbsent(
	t *testing.T,
	metrics []*metric.Data,
	device string,
	operation string,
) {
	t.Helper()
	for _, data := range metrics {
		got := inspectThrotlMetric(t, data)
		if (got.name == throtlDelayedCountName ||
			got.name == throtlAverageWaitName) &&
			got.labels["device"] == device &&
			got.labels["operation"] == operation {
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
		got := inspectThrotlMetric(t, data)
		if got.labels["device"] != device ||
			got.labels["operation"] != operation {
			continue
		}
		if got.name != throtlDelayedCountName &&
			got.name != throtlAverageWaitName {
			continue
		}
		scope := got.labels["scope"]
		if values[scope] == nil {
			values[scope] = make(map[string]float64)
		}
		values[scope][got.name] = got.value
	}
	for _, scope := range []string{throtlHostScope, throtlOtherScope} {
		require.Contains(t, values, scope)
		require.Contains(t, values[scope], throtlDelayedCountName)
		require.Contains(t, values[scope], throtlAverageWaitName)
		require.Equal(t, float64(wantCount),
			values[scope][throtlDelayedCountName])
		if wantCount == 0 {
			require.Zero(t, values[scope][throtlAverageWaitName])
		}
	}
	hostAverage := values[throtlHostScope][throtlAverageWaitName]
	require.Equal(t, hostAverage,
		values[throtlOtherScope][throtlAverageWaitName])
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
	status, err := session.readStatus()
	require.NoError(t, err)
	require.Equal(t, throtlStatus{}, status)
}

func requireThrotlKernelActiveTD(
	t *testing.T,
	session *throtlSession,
	disk *throtlKernelDisk,
) uint64 {
	t.Helper()
	items, err := session.object.DumpMapByName(throtlTDMap)
	require.NoError(t, err)
	var matches []uint64
	for _, item := range items {
		var td uint64
		var value throtlTDValue
		require.NoError(t, decodeBPFMapData(item.Key, &td))
		require.NoError(t, decodeBPFMapData(item.Value, &value))
		if value.Major == disk.major && value.Minor == disk.minor {
			require.Equal(t, throtlTDActive, value.State)
			matches = append(matches, td)
		}
	}
	require.Len(t, matches, 1,
		"one active td must own device %s", disk.device)
	require.NotZero(t, matches[0])
	return matches[0]
}

func readThrotlKernelRows(
	t *testing.T,
	session *throtlSession,
	td uint64,
) throtlKernelRows {
	t.Helper()
	var rows throtlKernelRows

	aggregates, err := session.object.DumpMapByName(throtlWaitAggregateMap)
	require.NoError(t, err)
	for _, item := range aggregates {
		var key throtlWaitKey
		require.NoError(t, decodeBPFMapData(item.Key, &key))
		if key.TD == td {
			rows.aggregate++
		}
	}

	owners, err := session.object.DumpMapByName(throtlBLKGOwnerMap)
	require.NoError(t, err)
	for _, item := range owners {
		var owner throtlTestBLKGOwner
		require.NoError(t, decodeBPFMapData(item.Value, &owner))
		if owner.TD == td {
			rows.owner++
		}
	}

	pending, err := session.object.DumpMapByName(throtlPendingMap)
	require.NoError(t, err)
	for _, item := range pending {
		var key throtlPendingKey
		require.NoError(t, decodeBPFMapData(item.Key, &key))
		if key.TD == td {
			rows.pending++
		}
	}

	tds, err := session.object.DumpMapByName(throtlTDMap)
	require.NoError(t, err)
	for _, item := range tds {
		var key uint64
		require.NoError(t, decodeBPFMapData(item.Key, &key))
		if key == td {
			rows.td++
		}
	}
	return rows
}

func waitThrotlKernelRows(
	t *testing.T,
	session *throtlSession,
	td uint64,
	want throtlKernelRows,
) {
	t.Helper()
	deadline := time.Now().Add(throtlKernelStateTimeout)
	var got throtlKernelRows
	for time.Now().Before(deadline) {
		got = readThrotlKernelRows(t, session, td)
		if got.equal(want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, statusErr := session.readStatus()
	t.Fatalf("timeout waiting for blk-throttle rows for td %#x: got %+v, want %+v, status=%+v, status_error=%v",
		td, got, want, status, statusErr)
}

func waitThrotlKernelTDState(
	t *testing.T,
	session *throtlSession,
	td uint64,
	want uint32,
) {
	t.Helper()
	waitThrotlKernelState(t, "blk-throttle td state", func() bool {
		items, err := session.object.DumpMapByName(throtlTDMap)
		require.NoError(t, err)
		for _, item := range items {
			var key uint64
			var value throtlTDValue
			require.NoError(t, decodeBPFMapData(item.Key, &key))
			require.NoError(t, decodeBPFMapData(item.Value, &value))
			if key == td {
				return value.State == want
			}
		}
		return false
	})
}

func waitThrotlKernelState(
	t *testing.T,
	description string,
	condition func() bool,
) {
	t.Helper()
	deadline := time.Now().Add(throtlKernelStateTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}

func runThrotlKernelCommand(
	t *testing.T,
	timeout time.Duration,
	name string,
	arguments ...string,
) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, arguments...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run %s %s: %v: %s", name,
			strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(output))
}

func cleanupThrotlKernelCommand(name string, arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(),
		throtlKernelCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name,
			strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
