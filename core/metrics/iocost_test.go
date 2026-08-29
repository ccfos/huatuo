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

// IOCOST tests retain capture, hooks, metrics, and qualification contracts.
package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/btf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// BPF accounting helpers and lifecycle guards.

// IOCOST BPF helper tests use deterministic kernel reads and map operations.

// Packed wait accounting and device layout.

// These tests run the production admission and wake functions with deterministic
// kernel reads and map helpers. Missing calls model skipped probes; kernel load
// and real IRQ concurrency are covered separately by qualification.

// Exercise the shared disk reader against each available bio layout.
func TestIOCostDeviceFieldSelection(t *testing.T) {
	source := ioControlBPFSource(t, "iocost_tracing.c")
	reader := ioControlTestSourceSection(t, source,
		"static __always_inline int iocost_read_bio(",
		"static __always_inline void iocost_retire_aggregate")
	diskReader := ioControlTestSourceSection(t, ioControlBPFSource(t, "include/bpf_blkio.h"),
		"struct block_device___5_12 {", "\n#endif")
	diskReader = strings.ReplaceAll(diskReader, " __attribute__((preserve_access_index))", "")
	program := `
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
typedef uint64_t u64;
typedef uint32_t u32;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define BPF_CORE_READ_INTO(dst, src, field) (*(dst) = (src) ? (src)->field : 0, (src) ? 0 : -1)
#define IOCOST_CORE_READ(dst, src, field) (BPF_CORE_READ_INTO(dst, src, field) == 0)
#define bpf_core_field_exists(field) has_disk
#define REQ_OP_MASK 0xff
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
struct gendisk { int major, first_minor; };
struct block_device { struct gendisk *bd_disk; };
struct bio {
    union { struct gendisk *bi_disk; struct block_device *bi_bdev; };
    u32 bi_opf;
};
static bool has_disk;
` + diskReader + reader + `
int main(int argc, char **argv)
{
    if (argc != 2)
        return 1;
    has_disk = atoi(argv[1]);
    struct gendisk disk = { .major = 8, .first_minor = 16 };
    struct block_device bdev = { .bd_disk = &disk };
    struct bio io = {};
    if (has_disk)
        io.bi_disk = &disk;
    else
        io.bi_bdev = &bdev;
    if (bio_disk(&io) != &disk)
        return 2;
    io.bi_disk = NULL;
    if (bio_disk(&io) || bio_disk(NULL))
        return 3;
    u32 operation = 0;
    for (u32 op = REQ_OP_READ; op <= REQ_OP_WRITE; op++) {
        io.bi_opf = op | (1U << 8);
        if (iocost_read_bio(&io, &operation) != 1 || operation != op)
            return 4;
    }
    io.bi_opf = 3;
    if (iocost_read_bio(&io, &operation) != 0)
        return 5;
    if (iocost_read_bio(NULL, &operation) != -1)
        return 6;
    return 0;
}
`
	executable := compileIOControlTestC(t, "cc", "device", program, "-fno-strict-aliasing")
	for _, test := range []struct {
		name string
		disk string
	}{
		{name: "bio disk", disk: "1"},
		{name: "bio bdev", disk: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.CommandContext(t.Context(), executable, test.disk).CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}
}

func TestIOCostDiagnosticFixtureFiltering(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..",
		"integration", "testdata", "iocost_tracing_test.c"))
	require.NoError(t, err)
	source := string(data)
	program := `
#include <stdbool.h>
#include <stdint.h>
typedef uint64_t u64;
typedef uint32_t u32;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
static u64 iocost_diag_css_serial = 300;
static u32 iocost_diag_major = 8, iocost_diag_first_minor = 16;
static u32 iocost_diag_fault_mask;
` + ioControlTestSourceSection(t, source, "#define IOCOST_DIAG_FAULT_MAP_FULL", "volatile const u64 iocost_diag_css_serial") +
		ioControlTestSourceSection(t, source, "static __always_inline bool iocost_diag_fault_mask_valid", "static __always_inline bool\niocost_diag_claim_fault") + `
int main(void)
{
    u32 valid_masks[] = {0, IOCOST_DIAG_FAULT_MAP_FULL,
        IOCOST_DIAG_FAULT_COLLISION, IOCOST_DIAG_FAULT_DELETE_FAILURE};
    for (u32 i = 0; i < sizeof(valid_masks) / sizeof(valid_masks[0]); i++) {
        iocost_diag_fault_mask = valid_masks[i];
        if (!iocost_diag_fixture_matches(300, 8, 16))
            return 1;
    }
    if (iocost_diag_fixture_matches(301, 8, 16) ||
        iocost_diag_fixture_matches(300, 9, 16) ||
        iocost_diag_fixture_matches(300, 8, 17))
        return 2;
    iocost_diag_fault_mask = IOCOST_DIAG_FAULT_MAP_FULL | IOCOST_DIAG_FAULT_COLLISION;
    if (iocost_diag_fixture_matches(300, 8, 16) ||
        iocost_diag_fault_mask_valid(8))
        return 3;
    iocost_diag_fault_mask = 0;
    iocost_diag_css_serial = 0;
    if (iocost_diag_fixture_matches(0, 8, 16))
        return 4;
    return 0;
}
`
	executable := compileIOControlTestC(t, "cc", "filter", program)
	output, err := exec.CommandContext(t.Context(), executable).CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestIOCostWakePackedPublication(t *testing.T) {
	source := ioControlBPFSource(t, "iocost_tracing.c")
	section := func(start, end string) string {
		return ioControlTestSourceSection(t, source, start, end)
	}
	program := `
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
typedef uint64_t u64;
typedef int64_t s64;
typedef uint32_t u32;
typedef int32_t s32;
#define SEC(name)
#define PT_REGS_RC(ctx) ((ctx)->ret)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define PT_REGS_PARM4_CORE(ctx) ((ctx)->arg4)
#define BPF_KPROBE_READ_RET_IP(caller, ctx) ((caller) = 1)
#define IOCOST_CORE_READ(dst, src, field) (*(dst) = (src)->field, true)
#define compat_bpf_core_field_offset(field) 0
#define COMPAT_BPF_ANY 0
#define COMPAT_BPF_NOEXIST 1
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
#define IOCOST_ENOENT 2
#define IOCOST_EEXIST 17
struct pt_regs { u64 ret, arg1, arg4; };
struct bio { int unused; };
struct ioc_gq { int unused; };
struct wait_queue_entry { int unused; };
struct iocg_wait___iocost { struct wait_queue_entry wait; struct bio *bio; };
struct iocg_wake_ctx___iocost { struct ioc_gq *iocg; };
` + ioControlBPFSource(t, "include/bpf_iocontrol.h") +
		section("struct iocost_owner_state {", "#define IOCOST_ASSERT_OFFSET") + `
static int iocost_pending_map, iocost_owner_state_map;
static int iocost_wait_agg_map, iocost_wake_frame_map, io_control_status_map;
static struct iocost_pending pending;
static struct iocost_owner_state owner;
static struct iocost_wake_frame frame;
static struct io_control_status status;
static struct io_control_status *io_control_status_value(void) { return &status; }
static u64 aggregate, aggregate_at_delete;
static bool pending_live;
static long delete_ret;
static long update_ret;
static int missing_aggregate;
static u64 clock_ns = 1;
static struct bio bio;
static struct ioc_gq iocg;
static struct iocg_wait___iocost wait = { .bio = &bio };
static struct iocg_wake_ctx___iocost wake_ctx = { .iocg = &iocg };

static void *bpf_map_lookup_elem(void *map, const void *key)
{
    if (map == &iocost_pending_map)
        return pending_live ? &pending : NULL;
    if (map == &iocost_owner_state_map)
        return &owner;
    if (map == &iocost_wait_agg_map) {
        return missing_aggregate ? NULL : &aggregate;
    }
    if (map == &iocost_wake_frame_map)
        return &frame;
    if (map == &io_control_status_map)
        return &status;
    return NULL;
}

static long bpf_map_delete_elem(void *map, const void *key)
{
    aggregate_at_delete = aggregate;
    if (!delete_ret)
        pending_live = false;
    return delete_ret;
}
static long bpf_map_update_elem(void *map, const void *key, const void *value, u64 flags)
{
    if (update_ret)
        return update_ret;
    if (pending_live && flags == COMPAT_BPF_NOEXIST)
        return -IOCOST_EEXIST;
    pending = *(const struct iocost_pending *)value;
    pending_live = true;
    return 0;
}
static u64 bpf_ktime_get_ns(void) { return clock_ns; }
static bool iocost_enqueue_caller(u64 caller) { return true; }
static struct bio *iocost_tail_waiter(struct ioc_gq *group)
{
    return &bio;
}
static int iocost_read_bio(struct bio *bio, u32 *operation)
{
    *operation = REQ_OP_READ;
    return 1;
}
static struct iocost_owner_state *iocost_ensure_owner(struct ioc_gq *group, struct bio *bio)
{
    return &owner;
}
static bool iocost_ensure_aggregate(const struct io_control_wait_key *key) { return true; }
` + section("static __always_inline bool iocost_unhealthy",
		"static __always_inline bool iocost_range_contains") +
		section("SEC(\"kprobe/iocg_kick_waitq\")",
			"SEC(\"kprobe/ioc_pd_free\")") + `
static void admit(void)
{
    struct pt_regs ctx = { .arg1 = (u64)&iocg };
    kprobe_iocg_kick_waitq(&ctx);
}
static void enter(void)
{
    struct pt_regs ctx = { .arg1 = (u64)&wait.wait, .arg4 = (u64)&wake_ctx };
    kprobe_iocg_wake_fn(&ctx);
}
int main(int argc, char **argv)
{
    if (argc == 2) {
        // Return the map helper's raw register value to the admission probe.
        update_ret = strtol(argv[1], NULL, 10);
        admit();
        return fwrite(&status, sizeof(status), 1, stdout) != 1;
    }
    if (argc != 6)
        return 1;
    aggregate = strtoull(argv[1], NULL, 10);
    delete_ret = strtol(argv[3], NULL, 10);
    u64 wait_ns = strtoull(argv[2], NULL, 10);
    int scenario = strtol(argv[5], NULL, 10);
    owner = (struct iocost_owner_state){
        .css = 1, .css_serial = 1, .major = 8, .minor = 16,
    };
    struct pt_regs ctx = {};
    admit();
    clock_ns += wait_ns;
    if (scenario != 2)
        enter();
    if (scenario == 1 || scenario == 2) {
        /* Skip the old return, or both wake probes; reuse the same bio. */
        clock_ns = 1000001;
        admit();
        clock_ns += wait_ns;
        enter();
    } else if (scenario == 3) {
        /* Budget retries restore the original start, not this wake's time. */
        u64 before = aggregate;
        for (int retry = 0; retry < 3; retry++) {
            ctx.ret = (u32)-1;
            kretprobe_iocg_wake_fn(&ctx);
            if (!pending_live || pending.start_ns != 1 || frame.bio_ptr ||
                aggregate != before || status.failure)
                return 2;
            clock_ns += wait_ns;
            enter();
        }
        ctx.ret = 0;
    } else if (scenario == 4) {
        /* The issuer reused the bio before the old successful return ran. */
        clock_ns = 1000001;
        admit();
    }
    missing_aggregate = strtol(argv[4], NULL, 10);
    kretprobe_iocg_wake_fn(&ctx);
    u64 result[] = {
        aggregate, status.failure != 0, pending_live, frame.bio_ptr != 0,
        aggregate_at_delete,
        (status.failure >> 32) == IOCOST_FAILURE_PENDING_DELETE,
        (status.failure >> 32) == IOCOST_FAILURE_IDENTITY,
        (u64)(s64)(s32)status.failure,
        pending_live ? pending.start_ns : 0,
    };
    return fwrite(result, sizeof(result), 1, stdout) != 1;
}
`
	executable := compileIOControlTestC(t, "clang", "iocost-packed", program,
		"-I", filepath.Join("..", "..", "bpf", "include"),
		"-Werror=incompatible-pointer-types", "-static")

	for _, extension := range []string{"signed", "zero-extended"} {
		t.Run("map full/"+extension, func(t *testing.T) {
			raw := -int64(unix.E2BIG)
			if extension == "zero-extended" {
				raw = int64(uint32(raw))
			}
			output, err := exec.CommandContext(t.Context(), executable,
				strconv.FormatInt(raw, 10)).Output()
			require.NoError(t, err)
			var rawStatus ioControlStatus
			require.NoError(t, decodeBPFMapData(output, &rawStatus))
			require.Equal(t, ioControlTestStatus(ioCostFailurePendingInsert, -int32(unix.E2BIG)), rawStatus)
			require.ErrorIs(t, ioCostFailure(rawStatus.Failure), types.ErrTracingStopped)
		})
	}

	tests := []struct {
		name         string
		before       uint64
		waitNS       uint64
		deleteErr    int
		zeroExtended bool
		missing      int
		scenario     int
		want         ioControlWaitCounters
	}{
		{name: "sub unit wait", waitNS: 9999, want: ioControlWaitCounters{IOCount: 1}},
		{name: "truncate wait", waitNS: 19999, want: ioControlWaitCounters{IOCount: 1, Wait10US: 1}},
		{name: "missed return", waitNS: 20000, scenario: 1, want: ioControlWaitCounters{IOCount: 1, Wait10US: 2}},
		{name: "both wake probes missed", waitNS: 20000, scenario: 2, want: ioControlWaitCounters{IOCount: 1, Wait10US: 2}},
		{
			name: "repeated budget retries", waitNS: 20000, scenario: 3,
			want: ioControlWaitCounters{IOCount: 1, Wait10US: 8},
		},
		{name: "bio reused before return", waitNS: 20000, scenario: 4, want: ioControlWaitCounters{IOCount: 1, Wait10US: 2}},
		{
			name: "count wraps", before: ((1<<26)-1)<<38 | 7, waitNS: 20000,
			want: ioControlWaitCounters{Wait10US: 9},
		},
		{
			name: "upper wait bit is independent of count", before: 5<<38 | ((1 << 37) - 1), waitNS: 20000,
			want: ioControlWaitCounters{IOCount: 6, Wait10US: (1 << 37) + 1},
		},
		{
			name: "wait wraps without count carry", before: 5<<38 | ((1 << 38) - 1), waitNS: 20000,
			want: ioControlWaitCounters{IOCount: 6, Wait10US: 1},
		},
		{
			name: "delete failure prevents publication", before: 5<<38 | 7, waitNS: 123456, deleteErr: -5,
			want: ioControlWaitCounters{IOCount: 5, Wait10US: 7},
		},
		{
			name: "zero extended delete errno", before: 5<<38 | 7, waitNS: 123456,
			deleteErr: -int(unix.ENOENT), zeroExtended: true,
			want: ioControlWaitCounters{IOCount: 5, Wait10US: 7},
		},
		{
			name: "missing aggregate", before: 5<<38 | 7, waitNS: 123456, missing: 1,
			want: ioControlWaitCounters{IOCount: 5, Wait10US: 7},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deleteErr := int64(test.deleteErr)
			if test.zeroExtended {
				deleteErr = int64(uint32(deleteErr))
			}
			command := exec.CommandContext(t.Context(), executable,
				strconv.FormatUint(test.before, 10), strconv.FormatUint(test.waitNS, 10),
				strconv.FormatInt(deleteErr, 10), strconv.Itoa(test.missing),
				strconv.Itoa(test.scenario))
			output, err := command.Output()
			require.NoError(t, err)
			require.Len(t, output, 9*8)
			got, err := decodeIOControlWaitCounters(output[:8], 1)
			require.NoError(t, err)
			require.Equal(t, []ioControlWaitCounters{test.want}, got)
			var failed uint64
			if test.deleteErr != 0 {
				failed = 1
			}
			want := []uint64{failed, failed, 0, test.before, failed, 0}
			want = append(want, uint64(test.deleteErr))
			var pendingStart uint64
			if test.deleteErr != 0 {
				pendingStart = 1
			} else if test.scenario == 4 {
				want[1] = 1
				pendingStart = 1000001
			}
			want = append(want, pendingStart)
			for index, value := range want {
				require.Equal(t, value, binary.LittleEndian.Uint64(output[(index+1)*8:]),
					"state word %d", index+1)
			}
		})
	}
}

// pd_free releases the pointer cache but retains counters until collection.
func TestIOCostLifecycleReclaimsCgroupOwner(t *testing.T) {
	source := ioControlBPFSource(t, "iocost_tracing.c")
	section := func(start, end string) string {
		return ioControlTestSourceSection(t, source, start, end)
	}
	freeStart := strings.Index(source, "SEC(\"kprobe/ioc_pd_free\")")
	require.GreaterOrEqual(t, freeStart, 0)
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
typedef uint64_t u64;
typedef uint32_t u32;
typedef uint8_t u8;
#define SEC(name)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define compat_bpf_core_field_offset(field) ((u64)&(field))
#define IOCOST_EEXIST 17
#define IOCOST_ENOENT 2
#define COMPAT_BPF_NOEXIST 1
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
#define IOCOST_CORE_READ(dst, src, field) (*(dst) = (src)->field, true)
struct pt_regs { u64 arg1; };
struct cgroup_subsys_state { u64 serial_nr; };
struct blkcg { struct cgroup_subsys_state css; };
struct blkcg_gq { struct blkcg *blkcg; };
struct blkg_policy_data { struct blkcg_gq *blkg; };
struct gendisk { int major, first_minor; };
struct bio { struct gendisk *disk; };
struct ioc_gq { u64 padding; struct blkg_policy_data pd; };
struct ioc_gq___iocost { u64 padding; struct blkg_policy_data pd; };
` + ioControlBPFSource(t, "include/bpf_iocontrol.h") +
		section("struct iocost_owner_state {", "#define IOCOST_ASSERT_OFFSET") + `
static int iocost_owner_state_map, iocost_retired_map;
static struct iocost_owner_state owner;
static struct io_control_status status;
static struct io_control_status *io_control_status_value(void) { return &status; }
static struct io_control_retired_key retired[3];
static u32 retired_count;
static bool owner_live, zero_extended_errors;
static long update_ret, retire_ret;

static void *bpf_map_lookup_elem(void *map, const void *key)
{
    assert(map == &iocost_owner_state_map);
    return owner_live ? &owner : NULL;
}
static long bpf_map_update_elem(void *map, const void *key, const void *value, u64 flags)
{
    if (map == &iocost_retired_map) {
        if (retire_ret)
            return retire_ret;
        const struct io_control_retired_key *identity = key;
        assert(identity->css == owner.css && identity->css_serial == owner.css_serial);
        assert(identity->major == owner.major && identity->minor == owner.minor);
        assert(*(const u8 *)value == 1);
        retired[retired_count++] = *identity;
        return 0;
    }
    assert(map == &iocost_owner_state_map && flags == COMPAT_BPF_NOEXIST);
    if (update_ret)
        return update_ret;
    owner = *(const struct iocost_owner_state *)value;
    owner_live = true;
    return 0;
}
static long bpf_map_delete_elem(void *map, const void *key)
{
    if (map == &iocost_owner_state_map) {
        assert(owner_live);
        owner_live = false;
        return 0;
    }
    assert(0 && "pd_free must preserve cumulative operation rows");
    return -5;
}
static struct gendisk *bio_disk(struct bio *bio)
{
    return bio ? bio->disk : NULL;
}
` + section("static __always_inline bool iocost_unhealthy",
		"static __always_inline void iocost_classify_pending_update_ret") +
		section("static __always_inline void iocost_retire_aggregate",
			"static __always_inline bool iocost_ensure_aggregate") +
		source[freeStart:] + `
int main(int argc, char **argv)
{
    zero_extended_errors = argc > 1;
    struct blkcg group = { .css = { .serial_nr = 0x400 } };
    struct blkcg_gq blkg = { .blkcg = &group };
    struct gendisk disk = { .major = 8, .first_minor = 16 };
    struct ioc_gq iocg = { .pd = { .blkg = &blkg } };
    struct bio io = { .disk = &disk };
    assert(iocost_ensure_owner(&iocg, &io));
    assert(owner.css_serial == 0x400 && owner.major == 8 && owner.minor == 16);
    update_ret = -5;
    group.css.serial_nr++;
    assert(iocost_ensure_owner(&iocg, &io));
    assert(owner.css_serial == 0x400);
    struct pt_regs ctx = { .arg1 = (u64)&iocg.pd };
    kprobe_ioc_pd_free(&ctx);
    assert(!owner_live && retired_count == 1 && !status.failure);
    update_ret = 0;
    assert(iocost_ensure_owner(&iocg, &io));
    assert(owner.css_serial == 0x401);
    kprobe_ioc_pd_free(&ctx);
    assert(!owner_live && retired_count == 2 && !status.failure);
    assert(retired[0].css == retired[1].css && retired[0].css_serial != retired[1].css_serial);
    disk.first_minor++;
    assert(iocost_ensure_owner(&iocg, &io));
    kprobe_ioc_pd_free(&ctx);
    assert(!owner_live && retired_count == 3 && !status.failure);
    assert(retired[1].css_serial == retired[2].css_serial && retired[1].minor != retired[2].minor);
    assert(iocost_ensure_owner(&iocg, &io));
    retire_ret = zero_extended_errors ? (long)(u32)-7 : -7;
    kprobe_ioc_pd_free(&ctx);
    assert(!owner_live && retired_count == 3);
    assert((status.failure >> 32) == IOCOST_FAILURE_RETIRE);
    assert((int)(u32)status.failure == -7);
    status.failure = 0;
    update_ret = zero_extended_errors ? (long)(u32)-5 : -5;
    assert(!iocost_ensure_owner(&iocg, &io));
    assert((status.failure >> 32) == IOCOST_FAILURE_OWNER_INSERT);
    assert((int)(u32)status.failure == -5);
    return 0;
}
`
	executable := compileIOControlTestC(t, "clang", "iocost-lifecycle", program,
		"-I", filepath.Join("..", "..", "bpf", "include"), "-static")
	for _, args := range [][]string{nil, {"zero-extended"}} {
		output, err := exec.CommandContext(t.Context(), executable, args...).CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
}

const (
	ioCostCaptureTestCSS       = uint64(0x300)
	ioCostCaptureTestCSSSerial = uint64(0x400)
)

type ioCostCaptureTestLane = ioControlWaitCounters

type ioCostCaptureTestDump struct {
	items   []bpf.MapItem
	err     error
	release <-chan struct{}
	entered chan<- string
}

type ioCostCaptureTestRead struct {
	value   []byte
	err     error
	release <-chan struct{}
	entered chan<- string
}

type fakeIOCostCaptureBPF struct {
	bpf.BPF

	mu              sync.Mutex
	mapIDs          map[string]uint32
	mapNames        map[uint32]string
	defaultItems    map[string][]bpf.MapItem
	dumpResponses   map[string][]ioCostCaptureTestDump
	statusValue     []byte
	statusResponses []ioCostCaptureTestRead
	deviceChanges   uint64
	deleteErrors    map[string][]error
	deletedKeys     map[string][][]byte
}

func newFakeIOCostCaptureBPF(possibleCPUs int) *fakeIOCostCaptureBPF {
	object := &fakeIOCostCaptureBPF{
		mapIDs: map[string]uint32{
			ioCostWaitAggregateMap: 1,
			ioCostRetiredMap:       2,
			ioControlStatusMap:     4,
		},
		mapNames: map[uint32]string{
			1: ioCostWaitAggregateMap,
			2: ioCostRetiredMap,
			4: ioControlStatusMap,
		},
		defaultItems:  make(map[string][]bpf.MapItem),
		dumpResponses: make(map[string][]ioCostCaptureTestDump),
		deleteErrors:  make(map[string][]error),
		deletedKeys:   make(map[string][][]byte),
		statusValue:   ioControlTestBytes(ioControlStatus{}),
	}
	lanes := make([]ioCostCaptureTestLane, possibleCPUs)
	for cpu := range lanes {
		lanes[cpu] = ioCostCaptureStableLane(
			uint64(cpu+1), uint64(cpu+1)*1_000,
		)
	}
	object.defaultItems[ioCostWaitAggregateMap] = []bpf.MapItem{
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestCSS,
			ioCostCaptureTestCSSSerial,
			0,
			0,
			lanes...,
		),
	}
	return object
}

func (object *fakeIOCostCaptureBPF) MapIDByName(name string) uint32 {
	object.mu.Lock()
	defer object.mu.Unlock()
	return object.mapIDs[name]
}

func (object *fakeIOCostCaptureBPF) DumpMap(
	mapID uint32,
) ([]bpf.MapItem, error) {
	object.mu.Lock()
	name := object.mapNames[mapID]
	response := ioCostCaptureTestDump{
		items: cloneIOCostCaptureTestItems(object.defaultItems[name]),
	}
	if queued := object.dumpResponses[name]; len(queued) != 0 {
		response = queued[0]
		object.dumpResponses[name] = queued[1:]
	}
	object.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("unexpected IOCOST map ID %d", mapID)
	}
	if response.release != nil {
		if response.entered != nil {
			response.entered <- name
		}
		<-response.release
	}
	return cloneIOCostCaptureTestItems(response.items), response.err
}

func (object *fakeIOCostCaptureBPF) dumpBatch(mapID uint32) ([]bpf.MapItem, error) {
	return object.DumpMap(mapID)
}

func (object *fakeIOCostCaptureBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	return object.DumpMap(object.MapIDByName(name))
}

func (object *fakeIOCostCaptureBPF) DeleteMapItems(mapID uint32, keys [][]byte) error {
	object.mu.Lock()
	defer object.mu.Unlock()
	name := object.mapNames[mapID]
	if name == "" {
		return fmt.Errorf("unexpected IOCOST deletion map ID %d", mapID)
	}
	if queued := object.deleteErrors[name]; len(queued) != 0 {
		object.deleteErrors[name] = queued[1:]
		if queued[0] != nil {
			return queued[0]
		}
	}
	for _, key := range keys {
		if name == ioCostRetiredMap {
			for _, item := range object.defaultItems[ioCostWaitAggregateMap] {
				if bytes.HasPrefix(item.Key, key) {
					return errors.New("retired marker deleted before its cumulative rows")
				}
			}
		}
		object.deletedKeys[name] = append(object.deletedKeys[name], append([]byte(nil), key...))
		items := object.defaultItems[name]
		for index, item := range items {
			if bytes.Equal(item.Key, key) {
				object.defaultItems[name] = slices.Delete(items, index, index+1)
				break
			}
		}
	}
	return nil
}

func (object *fakeIOCostCaptureBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	object.mu.Lock()
	name := object.mapNames[mapID]
	response := ioCostCaptureTestRead{value: append([]byte(nil), object.statusValue...)}
	if len(response.value) == ioCostStatusSize {
		binary.LittleEndian.PutUint64(response.value[8:], object.deviceChanges)
	}
	if len(object.statusResponses) != 0 {
		response = object.statusResponses[0]
		object.statusResponses = object.statusResponses[1:]
	}
	object.mu.Unlock()
	if name != ioControlStatusMap || len(key) != ioCostUint32Size ||
		binary.LittleEndian.Uint32(key) != 0 {
		return nil, fmt.Errorf("unexpected IOCOST status read %s key %x", name, key)
	}
	if response.release != nil {
		if response.entered != nil {
			response.entered <- name
		}
		<-response.release
	}
	return append([]byte(nil), response.value...), response.err
}

func (object *fakeIOCostCaptureBPF) queueDumps(
	name string,
	responses ...ioCostCaptureTestDump,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.dumpResponses[name] = append(
		object.dumpResponses[name], responses...)
}

func (object *fakeIOCostCaptureBPF) setDefaultItems(
	name string,
	items []bpf.MapItem,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.defaultItems[name] = cloneIOCostCaptureTestItems(items)
}

func (object *fakeIOCostCaptureBPF) queueStatus(
	responses ...ioCostCaptureTestRead,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.statusResponses = append(object.statusResponses, responses...)
}

func cloneIOCostCaptureTestItems(items []bpf.MapItem) []bpf.MapItem {
	cloned := make([]bpf.MapItem, len(items))
	for index, item := range items {
		cloned[index] = bpf.MapItem{
			Key:   append([]byte(nil), item.Key...),
			Value: append([]byte(nil), item.Value...),
		}
	}
	return cloned
}

func ioCostCaptureStableLane(count, wait10US uint64) ioCostCaptureTestLane {
	return ioCostCaptureTestLane{
		IOCount:  count,
		Wait10US: wait10US,
	}
}

func ioCostCaptureTestAggregateItem(css, cssSerial uint64, operation, reserved uint32,
	lanes ...ioCostCaptureTestLane,
) bpf.MapItem {
	return ioControlWaitTestItem(ioControlWaitKey{
		CSS: css, CSSSerial: cssSerial, Major: 8, Minor: 16,
		Operation: operation, Reserved: reserved,
	}, lanes...)
}

type ioCostCaptureTestSession struct {
	session *ioCostSession
	cancel  context.CancelFunc

	mu          sync.Mutex
	cancelCalls int
}

func newIOCostCaptureTestSession(
	t *testing.T,
	object *fakeIOCostCaptureBPF,
	possibleCPUs int,
) *ioCostCaptureTestSession {
	t.Helper()
	ioControlTestDiskstats(t)
	breaker, cancel := context.WithCancelCause(t.Context())
	harness := &ioCostCaptureTestSession{cancel: func() { cancel(nil) }}
	harness.session = &ioCostSession{
		object:          object,
		hashReader:      ioControlHashReader{lookupBatch: object.dumpBatch},
		possibleCPUs:    possibleCPUs,
		breaker:         breaker,
		previous:        make(ioControlWaitSnapshot),
		containerSource: emptyIOCostContainerSource,
		cancel: func(cause error) {
			harness.mu.Lock()
			harness.cancelCalls++
			harness.mu.Unlock()
			cancel(cause)
		},
	}
	t.Cleanup(harness.cancel)
	return harness
}

func (session *ioCostCaptureTestSession) cancelCount() int {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.cancelCalls
}

const (
	ioCostOwnerStateMap                 = "iocost_owner_state_map"
	ioCostPendingMap                    = "iocost_pending_map"
	ioCostWakeFrameMap                  = "iocost_wake_frame_map"
	ioCostUint32Size                    = 4
	ioCostUint64Size                    = 8
	ioCostOwnerStateSize                = 24
	ioCostStatusSize                    = 16
	ioCostWaitKeySize                   = 32
	ioCostPendingMapMaxEntries   uint32 = 10240
	ioCostWakeFrameMapMaxEntries uint32 = 1
	ioCostStatusMapMaxEntries    uint32 = 1
	ioCostPendingSize                   = 16
	ioCostWakeFrameSize                 = 64
)

type ioCostOwnerState struct {
	CSS       uint64
	CSSSerial uint64
	Major     uint32
	Minor     uint32
}

// ioCostPending is the value of iocost_pending_map.
type ioCostPending struct {
	StartNS   uint64
	Operation uint32
	Reserved  uint32
}

// ioCostWakeFrame is one possible-CPU lane of iocost_wake_frame_map.
type ioCostWakeFrame struct {
	BioPtr  uint64
	EndNS   uint64
	Pending ioCostPending
	Key     ioControlWaitKey
}

func decodeIOCostPending(data []byte) (ioCostPending, error) {
	if err := requireIOCostDataSize(data, ioCostPendingSize); err != nil {
		return ioCostPending{}, err
	}
	value := ioCostPending{
		StartNS:   binary.LittleEndian.Uint64(data[0:8]),
		Operation: binary.LittleEndian.Uint32(data[8:12]),
		Reserved:  binary.LittleEndian.Uint32(data[12:16]),
	}
	if value.Reserved != 0 {
		return ioCostPending{}, nonzeroIOCostReserved(
			"iocost_pending", value.Reserved)
	}
	return value, nil
}

func decodeIOCostWakeFrame(data []byte) (ioCostWakeFrame, error) {
	if err := requireIOCostDataSize(data, ioCostWakeFrameSize); err != nil {
		return ioCostWakeFrame{}, err
	}
	pending, err := decodeIOCostPending(data[16:32])
	if err != nil {
		return ioCostWakeFrame{}, err
	}
	var key ioControlWaitKey
	if err := decodeBPFMapData(data[32:64], &key); err != nil {
		return ioCostWakeFrame{}, err
	}
	if key.Reserved != 0 {
		return ioCostWakeFrame{}, nonzeroIOCostReserved("io_control_wait_key", key.Reserved)
	}
	value := ioCostWakeFrame{
		BioPtr:  binary.LittleEndian.Uint64(data[0:8]),
		EndNS:   binary.LittleEndian.Uint64(data[8:16]),
		Pending: pending,
		Key:     key,
	}
	return value, nil
}

func decodeIOCostWakeFrames(
	data []byte,
	possibleCPUs int,
) ([]ioCostWakeFrame, error) {
	expectedSize, err := ioCostPerCPUDataSize(possibleCPUs, ioCostWakeFrameSize)
	if err != nil {
		return nil, err
	}
	if err := requireIOCostDataSize(data, expectedSize); err != nil {
		return nil, fmt.Errorf("possible-CPU wake frame data: %w", err)
	}
	values := make([]ioCostWakeFrame, possibleCPUs)
	for cpu := range possibleCPUs {
		offset := cpu * ioCostWakeFrameSize
		value, err := decodeIOCostWakeFrame(
			data[offset : offset+ioCostWakeFrameSize])
		if err != nil {
			return nil, fmt.Errorf(
				"decode CPU %d wake frame: %w", cpu, err)
		}
		values[cpu] = value
	}
	return values, nil
}

// Interval attribution and exported metric contracts.

// IOCOST metric tests cover interval accounting, attribution, and collector registration.

// Interval deltas, scope attribution, and public metric contracts.
func nonzeroIOCostReserved(structure string, value uint32) error {
	return fmt.Errorf("%s reserved field is %d, want 0", structure, value)
}

func ioCostPerCPUDataSize(possibleCPUs, valueSize int) (int, error) {
	if possibleCPUs <= 0 {
		return 0, fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	if valueSize <= 0 {
		return 0, fmt.Errorf("invalid possible-CPU value size: %d", valueSize)
	}
	maxInt := int(^uint(0) >> 1)
	if possibleCPUs > maxInt/valueSize {
		return 0, fmt.Errorf("possible CPU data size overflows int: %d * %d",
			possibleCPUs, valueSize)
	}
	return possibleCPUs * valueSize, nil
}

func requireIOCostDataSize(data []byte, expected int) error {
	if len(data) != expected {
		return fmt.Errorf("data size %d, want %d", len(data), expected)
	}
	return nil
}

func TestIOCostDiagnosticStatusGoldenBytes(t *testing.T) {
	data := []byte{
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x07, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	got, err := decodeIOCostDiagnosticStatus(data)
	require.NoError(t, err)
	require.Equal(t, ioCostDiagnosticStatus{
		StartGuardPasses: 1, WakeEntryHits: 2,
		WakeReturnZero: 3, WakeReturnMinusOne: 4, SettledCount: 5,
		MapFullInjections: 6, CollisionInjections: 7,
		DeleteFailureInjections: 8,
	}, got)
}

type ioCostIntervalTestRow struct {
	css       uint64
	cssSerial uint64
	major     uint32
	minor     uint32
	operation uint32
	lanes     []ioCostCaptureTestLane
}

func ioCostDefaultIntervalTestRow(
	count uint64,
	wait10US uint64,
) ioCostIntervalTestRow {
	return ioCostIntervalTestRow{
		css:       ioCostCaptureTestCSS,
		cssSerial: ioCostCaptureTestCSSSerial,
		major:     8,
		minor:     16,
		operation: 0,
		lanes: []ioCostCaptureTestLane{
			ioCostCaptureStableLane(count, wait10US),
		},
	}
}

func setIOCostIntervalTestRows(object *fakeIOCostCaptureBPF, rows ...ioCostIntervalTestRow) {
	aggregates := make([]bpf.MapItem, 0, len(rows))
	for _, row := range rows {
		item := ioCostCaptureTestAggregateItem(row.css, row.cssSerial, row.operation, 0, row.lanes...)
		binary.LittleEndian.PutUint32(item.Key[16:20], row.major)
		binary.LittleEndian.PutUint32(item.Key[20:24], row.minor)
		aggregates = append(aggregates, item)
	}
	object.setDefaultItems(ioCostWaitAggregateMap, aggregates)
}

type ioCostIntervalTestFixture struct {
	collector *iocostTracing
	harness   *ioCostCaptureTestSession
	object    *fakeIOCostCaptureBPF
	source    *ioControlAttributionTestSource
}

func newIOCostIntervalTestFixture(
	t *testing.T,
	possibleCPUs int,
	containers map[string]*pod.Container,
) *ioCostIntervalTestFixture {
	t.Helper()
	object := newFakeIOCostCaptureBPF(possibleCPUs)
	harness := newIOCostCaptureTestSession(t, object, possibleCPUs)
	source := &ioControlAttributionTestSource{containers: containers}
	harness.session.containerSource = source.read
	harness.session.previous = make(ioControlWaitSnapshot)
	return &ioCostIntervalTestFixture{
		collector: &iocostTracing{session: harness.session},
		harness:   harness,
		object:    object,
		source:    source,
	}
}

func ioCostMetricValues(
	t *testing.T,
	metrics []*metric.Data,
) map[string]float64 {
	t.Helper()
	values := make(map[string]float64, len(metrics))
	for _, data := range metrics {
		kind := data.Labels()["scope"]
		if strings.HasPrefix(data.Name(), "container_") {
			kind = "container"
		}
		values[kind+"/"+data.Name()] = data.Value
	}
	return values
}

func requireIOCostHostAndOtherInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Len(t, metrics, 4)
	require.InDeltaMapValues(t, map[string]float64{
		"host/waitq_io_count":   count,
		"host/average_wait_ms":  averageMS,
		"other/waitq_io_count":  count,
		"other/average_wait_ms": averageMS,
	}, ioCostMetricValues(t, metrics), 1e-12)
}

func requireIOCostHostAndContainerInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Len(t, metrics, 4)
	require.Equal(t, map[string]float64{
		"host/waitq_io_count":                 count,
		"host/average_wait_ms":                averageMS,
		"container/container_waitq_io_count":  count,
		"container/container_average_wait_ms": averageMS,
	}, ioCostMetricValues(t, metrics))
}

// Compiled object, userspace ABI, and session lifecycle.

// This file freezes the compiled IOCOST object, userspace ABI, and session
// lifecycle contracts.

const (
	ioCostOwnerStateMapMaxEntries    uint32 = 4096
	ioCostWaitAggregateMapMaxEntries        = 2 * ioCostOwnerStateMapMaxEntries

	ioCostDiagnosticObjectName = "iocost_tracing_test.o"

	ioCostDiagnosticStatusMap  = "iocost_diag_stat_map"
	ioCostDiagnosticScratchMap = "iocost_diag_fault_scratch_map"

	ioCostDiagnosticFaultMaskConstant  = "iocost_diag_fault_mask"
	ioCostDiagnosticMajorConstant      = "iocost_diag_major"
	ioCostDiagnosticFirstMinorConstant = "iocost_diag_first_minor"
	ioCostDiagnosticCSSSerialConstant  = "iocost_diag_css_serial"

	ioCostDiagnosticStatusSize = 8 * ioCostUint64Size

	ioCostDiagnosticFaultMapFull       uint32 = 1
	ioCostDiagnosticFaultCollision     uint32 = 2
	ioCostDiagnosticFaultDeleteFailure uint32 = 4
)

type ioCostDiagnosticStatus struct {
	StartGuardPasses        uint64
	WakeEntryHits           uint64
	WakeReturnZero          uint64
	WakeReturnMinusOne      uint64
	SettledCount            uint64
	MapFullInjections       uint64
	CollisionInjections     uint64
	DeleteFailureInjections uint64
}

func TestLoadIOCostKernelProfile(t *testing.T) {
	queries := [][]string{
		{ioCostKickSymbol, ioCostWakeSymbol, ioCostPDFreeSymbol},
		{ioCostThrottleCaller, ioCostOverBudgetCaller},
	}
	modes := []symbol.KsymbolLookupMode{symbol.KsymbolLookupDefault, symbol.KsymbolLookupRange}
	calls := 0
	got, err := loadIOCostKernelProfileWith(func(
		names []string,
		mode symbol.KsymbolLookupMode,
	) (map[string]symbol.KsymbolRange, error) {
		require.Less(t, calls, len(queries))
		require.ElementsMatch(t, queries[calls], names)
		require.Equal(t, modes[calls], mode)
		calls++
		return syntheticIOCostKallsymsProfile(), nil
	})
	require.NoError(t, err)
	require.Equal(t, len(queries), calls)
	require.Equal(t, &ioCostKernelProfile{
		throttleRange:   symbol.KsymbolRange{Start: 0x5000, End: 0x5100},
		overBudgetRange: symbol.KsymbolRange{Start: 0x6000, End: 0x6080},
	}, got)

	failure := errors.New("kallsyms unavailable")
	for failAt := 1; failAt <= 2; failAt++ {
		calls := 0
		got, err = loadIOCostKernelProfileWith(func(
			[]string,
			symbol.KsymbolLookupMode,
		) (map[string]symbol.KsymbolRange, error) {
			calls++
			if calls == failAt {
				return nil, failure
			}
			return syntheticIOCostKallsymsProfile(), nil
		})
		require.ErrorIs(t, err, failure)
		require.NotErrorIs(t, err, types.ErrTracingStopped)
		require.Nil(t, got)
	}

	for _, test := range []struct {
		name          string
		symbol        string
		hasInvalidEnd bool
	}{
		{name: "missing throttle caller", symbol: ioCostThrottleCaller},
		{name: "missing kick hook", symbol: ioCostKickSymbol},
		{name: "missing wake hook", symbol: ioCostWakeSymbol},
		{name: "missing free hook", symbol: ioCostPDFreeSymbol},
		{name: "throttle range end", symbol: ioCostThrottleCaller, hasInvalidEnd: true},
		{name: "budget range end", symbol: ioCostOverBudgetCaller, hasInvalidEnd: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := loadIOCostKernelProfileWith(func(
				[]string,
				symbol.KsymbolLookupMode,
			) (map[string]symbol.KsymbolRange, error) {
				profile := syntheticIOCostKallsymsProfile()
				if test.hasInvalidEnd {
					value := profile[test.symbol]
					value.End = 0
					profile[test.symbol] = value
				} else {
					delete(profile, test.symbol)
				}
				return profile, nil
			})
			require.ErrorIs(t, err, types.ErrNotSupported)
			require.NotErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorContains(t, err, test.symbol)
			require.Nil(t, got)
		})
	}
}

// Entry/return samples share one slot per CPU, so RT waitq locks are unsupported.
func TestIOCostWaitQueueLockSupport(t *testing.T) {
	for _, name := range []string{"raw_spinlock", "rt_mutex", "rt_mutex_base"} {
		t.Run(name, func(t *testing.T) {
			spec := newSyntheticIOCostBTFSpec(t, syntheticIOCost510Profile,
				func(fixture *syntheticIOCostBTFFixture) {
					field := "lock"
					if name == "raw_spinlock" {
						field = "rlock"
					}
					lock := &btf.Struct{Name: "spinlock", Size: 8, Members: []btf.Member{
						{Name: field, Type: &btf.Struct{Name: name, Size: 8}},
					}}
					fixture.structures["wait_queue_head"].Members = append(
						fixture.structures["wait_queue_head"].Members,
						btf.Member{Name: "lock", Type: &btf.Typedef{Name: "spinlock_t", Type: lock}},
					)
				})
			err := checkIOCostWaitQueueLock(spec)
			if name == "raw_spinlock" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, types.ErrNotSupported)
				require.ErrorContains(t, err, "PREEMPT_RT")
			}
		})
	}
}

func TestResolveIOCostKallsymsBuildsHookProfile(t *testing.T) {
	tests := []struct {
		name       string
		optional   *symbol.KsymbolRange
		wantBudget symbol.KsymbolRange
	}{
		{
			name: "required and optional callers",
			optional: &symbol.KsymbolRange{
				Start: 0x6000,
				End:   0x6080,
			},
			wantBudget: symbol.KsymbolRange{Start: 0x6000, End: 0x6080},
		},
		{
			name: "optional caller absent",
		},
		{
			name: "adjacent half-open caller ranges",
			optional: &symbol.KsymbolRange{
				Start: 0x5100,
				End:   0x5180,
			},
			wantBudget: symbol.KsymbolRange{Start: 0x5100, End: 0x5180},
		},
		{
			name: "callers share one address range",
			optional: &symbol.KsymbolRange{
				Start: 0x5000,
				End:   0x5100,
			},
			wantBudget: symbol.KsymbolRange{Start: 0x5000, End: 0x5100},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := syntheticIOCostKallsymsProfile()
			if test.optional == nil {
				delete(profile, ioCostOverBudgetCaller)
			} else {
				profile[ioCostOverBudgetCaller] = *test.optional
			}

			got, err := resolveIOCostKallsyms(profile, profile)
			require.NoError(t, err)
			require.Equal(t, &ioCostKernelProfile{
				throttleRange:   symbol.KsymbolRange{Start: 0x5000, End: 0x5100},
				overBudgetRange: test.wantBudget,
			}, got)
		})
	}
}

func syntheticIOCostKallsymsProfile() map[string]symbol.KsymbolRange {
	return map[string]symbol.KsymbolRange{
		ioCostKickSymbol:       {Start: 0x1000},
		ioCostWakeSymbol:       {Start: 0x2000},
		ioCostPDFreeSymbol:     {Start: 0x4000},
		ioCostThrottleCaller:   {Start: 0x5000, End: 0x5100},
		ioCostOverBudgetCaller: {Start: 0x6000, End: 0x6080},
	}
}

type syntheticIOCostDeviceProfile uint8

const (
	syntheticIOCost510Profile syntheticIOCostDeviceProfile = iota
	syntheticIOCostMainlineProfile
)

type syntheticIOCostBTFFixture struct {
	types      []btf.Type
	structures map[string]*btf.Struct
}

func newSyntheticIOCostBTFSpec(
	t *testing.T,
	device syntheticIOCostDeviceProfile,
	mutate func(*syntheticIOCostBTFFixture),
) *btf.Spec {
	t.Helper()
	fixture := newSyntheticIOCostBTFFixture(device)
	if mutate != nil {
		mutate(fixture)
	}

	builder, err := btf.NewBuilder(fixture.types)
	require.NoError(t, err, "build synthetic IOCOST BTF fixture")
	raw, err := builder.Marshal(nil, nil)
	require.NoError(t, err, "marshal synthetic IOCOST BTF fixture")
	spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
	require.NoError(t, err, "load synthetic IOCOST BTF fixture")
	return spec
}

func newSyntheticIOCostBTFFixture(
	device syntheticIOCostDeviceProfile,
) *syntheticIOCostBTFFixture {
	void := (*btf.Void)(nil)
	signedInt := &btf.Int{Name: "int", Size: 4, Encoding: btf.Signed}
	unsignedInt := &btf.Int{
		Name:     "unsigned int",
		Size:     4,
		Encoding: btf.Unsigned,
	}
	boolean := &btf.Int{Name: "_Bool", Size: 1, Encoding: btf.Bool}
	unsignedLongLong := &btf.Int{
		Name:     "long long unsigned int",
		Size:     8,
		Encoding: btf.Unsigned,
	}

	listHead := &btf.Struct{Name: "list_head", Size: 16}
	waitQueueEntry := &btf.Struct{Name: "wait_queue_entry", Size: 32}
	waitQueueHead := &btf.Struct{Name: "wait_queue_head", Size: 24}
	bio := &btf.Struct{Name: "bio", Size: 32}
	iocgWait := &btf.Struct{Name: "iocg_wait", Size: 64}
	iocgWakeContext := &btf.Struct{Name: "iocg_wake_ctx", Size: 8}
	iocg := &btf.Struct{Name: "ioc_gq", Size: 80}
	disk := &btf.Struct{Name: "gendisk", Size: 40}
	blockDevice := &btf.Struct{Name: "block_device", Size: 8}
	policyData := &btf.Struct{Name: "blkg_policy_data", Size: 32}
	blkg := &btf.Struct{Name: "blkcg_gq", Size: 8}
	blkcg := &btf.Struct{Name: "blkcg", Size: 16}
	css := &btf.Struct{Name: "cgroup_subsys_state", Size: 16}
	registers := &btf.Struct{Name: "pt_regs", Size: 40, Members: []btf.Member{
		{Name: "ax", Type: unsignedLongLong},
		{Name: "cx", Type: unsignedLongLong, Offset: 64},
		{Name: "di", Type: unsignedLongLong, Offset: 128},
		{Name: "si", Type: unsignedLongLong, Offset: 192},
		{Name: "sp", Type: unsignedLongLong, Offset: 256},
	}}

	wakeCallback := &btf.FuncProto{
		Return: signedInt,
		Params: []btf.FuncParam{
			{Type: &btf.Pointer{Target: waitQueueEntry}},
			{Type: unsignedInt},
			{Type: signedInt},
			{Type: &btf.Pointer{Target: void}},
		},
	}
	listHead.Members = []btf.Member{
		{Name: "next", Type: &btf.Pointer{Target: listHead}, Offset: 0},
		{Name: "prev", Type: &btf.Pointer{Target: listHead}, Offset: 64},
	}
	waitQueueHead.Members = []btf.Member{
		{Name: "head", Type: listHead, Offset: 64},
	}
	waitQueueEntry.Members = []btf.Member{
		{Name: "entry", Type: listHead, Offset: 0},
		{Name: "func", Type: &btf.Pointer{Target: wakeCallback}, Offset: 128},
		{Name: "private", Type: &btf.Pointer{Target: void}, Offset: 192},
	}
	iocgWait.Members = []btf.Member{
		{Name: "wait", Type: waitQueueEntry, Offset: 64},
		{Name: "bio", Type: &btf.Pointer{Target: bio}, Offset: 320},
		{Name: "committed", Type: boolean, Offset: 384},
	}
	iocgWakeContext.Members = []btf.Member{
		{Name: "iocg", Type: &btf.Pointer{Target: iocg}, Offset: 0},
	}
	iocg.Members = []btf.Member{
		{Name: "pd", Type: policyData, Offset: 64},
		{Name: "waitq", Type: waitQueueHead, Offset: 320},
	}
	bio.Members = []btf.Member{
		{Name: "bi_opf", Type: unsignedInt, Offset: 64},
	}
	policyData.Members = []btf.Member{
		{Name: "blkg", Type: &btf.Pointer{Target: blkg}, Offset: 0},
	}
	blkg.Members = []btf.Member{
		{Name: "blkcg", Type: &btf.Pointer{Target: blkcg}, Offset: 0},
	}
	blkcg.Members = []btf.Member{
		{Name: "css", Type: css, Offset: 0},
	}
	css.Members = []btf.Member{
		{Name: "serial_nr", Type: unsignedLongLong, Offset: 0},
	}
	disk.Members = []btf.Member{
		{Name: "major", Type: signedInt, Offset: 0},
		{Name: "first_minor", Type: signedInt, Offset: 32},
		{Name: "disk_name", Offset: 64, Type: &btf.Array{
			Type:  &btf.Int{Name: "char", Size: 1, Encoding: btf.Signed},
			Index: unsignedInt, Nelems: 32,
		}},
	}

	if device == syntheticIOCost510Profile {
		bio.Members = append(bio.Members, btf.Member{
			Name: "bi_disk", Type: &btf.Pointer{Target: disk}, Offset: 128,
		})
	}
	if device == syntheticIOCostMainlineProfile {
		bio.Members = append(bio.Members, btf.Member{
			Name: "bi_bdev", Type: &btf.Pointer{Target: blockDevice}, Offset: 192,
		})
		blockDevice.Members = append(blockDevice.Members, btf.Member{
			Name: "bd_disk", Type: &btf.Pointer{Target: disk}, Offset: 0,
		})
	}

	structures := map[string]*btf.Struct{
		"list_head":           listHead,
		"wait_queue_entry":    waitQueueEntry,
		"wait_queue_head":     waitQueueHead,
		"bio":                 bio,
		"iocg_wait":           iocgWait,
		"iocg_wake_ctx":       iocgWakeContext,
		"ioc_gq":              iocg,
		"gendisk":             disk,
		"block_device":        blockDevice,
		"blkg_policy_data":    policyData,
		"blkcg_gq":            blkg,
		"blkcg":               blkcg,
		"cgroup_subsys_state": css,
		"pt_regs":             registers,
	}
	// Model kernel BTF with structure records but no function records.
	return &syntheticIOCostBTFFixture{
		types: []btf.Type{
			listHead,
			waitQueueEntry,
			waitQueueHead,
			bio,
			iocgWait,
			iocgWakeContext,
			iocg,
			disk,
			blockDevice,
			policyData,
			blkg,
			blkcg,
			css,
			registers,
		},
		structures: structures,
	}
}

func TestIOCostTransactionFailuresRebaselineBeforeDelta(t *testing.T) {
	for _, failure := range []string{"dump", "status", "delta", "unstable"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newIOCostIntervalTestFixture(t, 1, nil)
			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(3, 900))
			data, err := fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndOtherInterval(t, data, 3, 3)
			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(3, 1000))
			switch failure {
			case "dump":
				fixture.object.queueDumps(ioCostWaitAggregateMap,
					ioCostCaptureTestDump{err: unix.EINTR})
			case "status":
				fixture.object.queueStatus(ioCostCaptureTestRead{err: unix.EINTR})
			case "delta":
			case "unstable":
				items := fixture.object.defaultItems[ioCostWaitAggregateMap]
				for range ioControlSnapshotAttempts {
					fixture.object.queueDumps(ioCostWaitAggregateMap,
						ioCostCaptureTestDump{items: append(cloneIOCostCaptureTestItems(items), items[0])})
				}
			}
			data, err = fixture.collector.Update()
			require.Error(t, err)
			require.Nil(t, data)
			require.Nil(t, fixture.collector.session.previous)
			require.Zero(t, fixture.harness.cancelCount())

			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(5, 1700))
			data, err = fixture.collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Nil(t, data)
			require.NotNil(t, fixture.collector.session.previous)
			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(7, 2300))
			data, err = fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndOtherInterval(t, data, 2, 3)
		})
	}
}

func TestIOCostIntervalCgroupChurnAndZeroDelta(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	row := ioCostDefaultIntervalTestRow(3, 900)
	for generation := uint64(0); generation < 10; generation++ {
		row.cssSerial = ioCostCaptureTestCSSSerial + generation
		setIOCostIntervalTestRows(fixture.object, row)
		data, err := fixture.collector.Update()
		require.NoError(t, err)
		requireIOCostHostAndOtherInterval(t, data, 3, 3)
		require.Len(t, fixture.collector.session.previous, 1)
		data, err = fixture.collector.Update()
		require.NoError(t, err)
		requireIOCostHostAndOtherInterval(t, data, 0, 0)
		setIOCostIntervalTestRows(fixture.object)
		data, err = fixture.collector.Update()
		require.NoError(t, err)
		require.Empty(t, data)
		require.Empty(t, fixture.collector.session.previous)
	}
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostRetiredIntervals(t *testing.T) {
	identity := ioControlRetiredKey{
		CSS:       ioCostCaptureTestCSS,
		CSSSerial: ioCostCaptureTestCSSSerial,
		Major:     8,
		Minor:     16,
	}
	marker := bpf.MapItem{Key: ioControlTestBytes(identity), Value: []byte{1}}
	for _, test := range []struct {
		name          string
		isSampled     bool
		deleteMap     string
		deleteResults []error
	}{
		{name: "first collection after release"},
		{name: "final increment only", isSampled: true},
		{
			name:          "counter deletion retry",
			deleteMap:     ioCostWaitAggregateMap,
			deleteResults: []error{unix.EIO},
		},
		{
			name:          "marker deletion retry",
			deleteMap:     ioCostRetiredMap,
			deleteResults: []error{unix.EIO},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := newIOControlAttributionTestContainer(
				"exited-container", identity.CSS, time.Unix(100, 0),
			)
			fixture := newIOCostIntervalTestFixture(t, 2, map[string]*pod.Container{container.ID: container})
			if test.isSampled {
				baseline := ioCostDefaultIntervalTestRow(3, 900)
				baseline.lanes = append(baseline.lanes, ioCostCaptureStableLane(0, 0))
				setIOCostIntervalTestRows(fixture.object, baseline)
				metrics, err := fixture.collector.Update()
				require.NoError(t, err)
				requireIOCostHostAndContainerInterval(t, metrics, 3, 3)
			}
			row := ioCostDefaultIntervalTestRow(5, 1_500)
			row.lanes = append(row.lanes, ioCostCaptureStableLane(2, 600))
			setIOCostIntervalTestRows(fixture.object, row)
			fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
			fixture.object.deleteErrors[test.deleteMap] = test.deleteResults
			metrics, err := fixture.collector.Update()
			require.NoError(t, err)
			count := float64(7)
			if test.isSampled {
				count -= 3
			}
			requireIOCostHostAndOtherInterval(t, metrics, count, 3)
			require.NotNil(t, fixture.harness.session.previous,
				"reclamation errors must not invalidate a published baseline")
			if test.deleteMap != "" {
				require.NotEmpty(t, fixture.object.defaultItems[ioCostRetiredMap])
			}
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			for _, data := range metrics {
				require.Zero(t, data.Value, "a retry cannot republish the final increment")
			}
			require.Empty(t, fixture.object.defaultItems[ioCostWaitAggregateMap])
			require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
			require.Empty(t, fixture.harness.session.previous)
			require.Zero(t, fixture.harness.cancelCount())
		})
	}

	t.Run("retirement during capture waits for the next collection", func(t *testing.T) {
		fixture := newIOCostIntervalTestFixture(t, 1, nil)
		row := ioCostDefaultIntervalTestRow(3, 900)
		setIOCostIntervalTestRows(fixture.object, row)
		entered := make(chan string, 1)
		release := make(chan struct{})
		fixture.object.queueDumps(ioCostWaitAggregateMap, ioCostCaptureTestDump{
			items:   cloneIOCostCaptureTestItems(fixture.object.defaultItems[ioCostWaitAggregateMap]),
			entered: entered,
			release: release,
		})
		var metrics []*metric.Data
		done := make(chan error, 1)
		go func() {
			var err error
			metrics, err = fixture.collector.Update()
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("IOCOST did not reach the cumulative snapshot")
		}
		// Publish retirement after the older counter snapshot was captured.
		// Reading R after that snapshot would discard this final increment.
		row.lanes[0] = ioCostCaptureStableLane(5, 1_500)
		setIOCostIntervalTestRows(fixture.object, row)
		fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
		close(release)
		require.NoError(t, <-done)
		requireIOCostHostAndOtherInterval(t, metrics, 3, 3)
		require.Empty(t, fixture.object.deletedKeys)
		metrics, err := fixture.collector.Update()
		require.NoError(t, err)
		requireIOCostHostAndOtherInterval(t, metrics, 2, 3)
		require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
	})

	t.Run("partial reclamation preserves other generations and devices", func(t *testing.T) {
		container := newIOControlAttributionTestContainer(
			"current-container", identity.CSS, time.Unix(100, 0),
		)
		fixture := newIOCostIntervalTestFixture(t, 1, map[string]*pod.Container{container.ID: container})
		read := ioCostDefaultIntervalTestRow(2, 200)
		write := ioCostDefaultIntervalTestRow(3, 600)
		write.operation = 1
		generation := ioCostDefaultIntervalTestRow(5, 1_500)
		generation.cssSerial++
		disk := ioCostDefaultIntervalTestRow(7, 2_800)
		disk.minor++
		setIOCostIntervalTestRows(
			fixture.object,
			read,
			write,
			generation,
			disk,
		)
		fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
		fixture.object.deleteErrors[ioCostWaitAggregateMap] = []error{nil, unix.EIO}
		metrics, err := fixture.collector.Update()
		require.NoError(t, err)
		counts := make(map[string]float64)
		for _, data := range metrics {
			if data.Name() != ioCostWaitCountName && data.Name() != "container_"+ioCostWaitCountName {
				continue
			}
			scope := data.Labels()["scope"]
			if strings.HasPrefix(data.Name(), "container_") {
				scope = "container"
			}
			counts[scope+"/"+data.Labels()["operation"]] = data.Value
		}
		require.Equal(t, map[string]float64{
			"host/read": 7, "host/write": 3,
			"other/read": 2, "other/write": 3,
			"container/read": 5,
		}, counts)
		require.Len(t, fixture.object.defaultItems[ioCostWaitAggregateMap], 3)
		require.Len(t, fixture.object.defaultItems[ioCostRetiredMap], 1)
		require.Len(t, fixture.harness.session.previous, 3)
		metrics, err = fixture.collector.Update()
		require.NoError(t, err)
		for _, data := range metrics {
			require.Zero(t, data.Value)
		}
		require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
		require.Len(t, fixture.object.defaultItems[ioCostWaitAggregateMap], 2)
		for key := range fixture.harness.session.previous {
			require.True(t, key.CSSSerial == generation.cssSerial || key.Minor == disk.minor)
		}
	})

	t.Run("failed collection retains final counters until rebaseline", func(t *testing.T) {
		fixture := newIOCostIntervalTestFixture(t, 1, nil)
		fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
		setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(3, 900))
		fixture.object.queueDumps(ioCostWaitAggregateMap, ioCostCaptureTestDump{err: unix.EINTR})
		_, err := fixture.collector.Update()
		require.ErrorIs(t, err, unix.EINTR)
		require.Empty(t, fixture.object.deletedKeys)
		require.Nil(t, fixture.harness.session.previous)
		metrics, err := fixture.collector.Update()
		require.ErrorIs(t, err, metric.ErrNoData)
		require.Nil(t, metrics)
		require.NotNil(t, fixture.harness.session.previous)
		metrics, err = fixture.collector.Update()
		require.NoError(t, err)
		require.Empty(t, metrics, "failed windows must not be backfilled")
		require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
	})
}

func TestIOCostTransactionTerminalErrorsAndTopologyPriority(t *testing.T) {
	for _, failure := range []string{
		"ABI", "health", "device", "teardown after ABI",
		"device during capture", "device during capture with unchanged count",
	} {
		t.Run(failure, func(t *testing.T) {
			fixture := newIOCostIntervalTestFixture(t, 1, nil)
			duringCapture := strings.HasPrefix(failure, "device during capture")
			switch failure {
			case "ABI", "teardown after ABI":
				item := fixture.object.defaultItems[ioCostWaitAggregateMap][0]
				item.Key = item.Key[:len(item.Key)-1]
				fixture.object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
				if failure == "teardown after ABI" {
					changed := ioControlTestBytes(ioControlStatus{})
					binary.LittleEndian.PutUint64(changed[8:], 1)
					fixture.object.queueStatus(ioCostCaptureTestRead{value: ioControlTestBytes(ioControlStatus{})},
						ioCostCaptureTestRead{value: changed})
				}
			case "health":
				fixture.object.statusValue = ioControlTestBytes(ioControlTestStatus(ioCostFailureWakeFrame, 0))
			case "device":
				fixture.object.deviceChanges = 1
			case "device during capture", "device during capture with unchanged count":
				setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(100, 1_000))
				_, err := fixture.collector.Update()
				require.NoError(t, err)
				count := uint64(1)
				if failure == "device during capture with unchanged count" {
					count = 100
				}
				setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(count, 10))
				fixture.object.queueStatus(
					ioCostCaptureTestRead{value: ioControlTestBytes(ioControlStatus{})},
					ioCostCaptureTestRead{value: ioControlTestBytes(ioControlStatus{DevicesChanged: 1})},
				)
			}
			data, err := fixture.collector.Update()
			if duringCapture {
				requireIOCostHostAndOtherInterval(t, data, 0, 0)
			} else {
				require.Nil(t, data)
			}
			require.Nil(t, fixture.collector.session.previous)
			require.Equal(t, 1, fixture.harness.cancelCount())
			if failure == "device" || failure == "teardown after ABI" || duringCapture {
				require.ErrorIs(t, err, errIOControlDevicesChanged)
				require.NotErrorIs(t, err, types.ErrTracingStopped)
			} else {
				require.ErrorIs(t, err, types.ErrTracingStopped)
			}
			data, err = fixture.collector.Update()
			require.Nil(t, data)
			require.ErrorIs(t, err, metric.ErrNoData)
		})
	}
}

func TestIOCostDeviceReadFailureDefersTerminalStop(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	_, err := fixture.collector.Update()
	require.NoError(t, err)
	require.NotEmpty(t, fixture.collector.session.previous)

	failure := ioControlTestBytes(ioControlTestStatus(ioCostFailureWakeFrame, 0))
	changed := append([]byte(nil), failure...)
	binary.LittleEndian.PutUint64(changed[8:], 1)
	fixture.object.queueStatus(
		ioCostCaptureTestRead{value: failure},
		ioCostCaptureTestRead{err: unix.ENOMEM},
		ioCostCaptureTestRead{value: changed},
	)

	metrics, err := fixture.collector.Update()
	require.ErrorIs(t, err, unix.ENOMEM)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Nil(t, metrics)
	require.Nil(t, fixture.collector.session.previous)
	require.Nil(t, context.Cause(fixture.collector.session.breaker))
	require.Zero(t, fixture.harness.cancelCount())

	metrics, err = fixture.collector.Update()
	require.ErrorIs(t, err, errIOControlDevicesChanged)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Nil(t, metrics)
	require.ErrorIs(t, context.Cause(fixture.collector.session.breaker), errIOControlDevicesChanged)
	require.Equal(t, 1, fixture.harness.cancelCount())
}

func TestIOCostNoDataForMissingOrAlreadyCanceledSession(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		collector := &iocostTracing{}
		if canceled {
			harness := newIOCostCaptureTestSession(t, newFakeIOCostCaptureBPF(1), 1)
			harness.cancel()
			collector.session = harness.session
		}
		data, err := collector.Update()
		require.Nil(t, data)
		require.ErrorIs(t, err, metric.ErrNoData)
	}
}

func TestIOCostTransactionUpdateMutexCancelWithdrawClose(t *testing.T) {
	object := &closeAwareIOCostCaptureBPF{fakeIOCostCaptureBPF: newFakeIOCostCaptureBPF(1)}
	harness := newIOCostCaptureTestSession(t, object.fakeIOCostCaptureBPF, 1)
	harness.session.object = object
	harness.session.hashReader.lookupBatch = object.dumpBatch
	collector := &iocostTracing{session: harness.session}
	entered := make(chan string, 1)
	release := make(chan struct{})
	releaseRead := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseRead)
	object.queueDumps(ioCostWaitAggregateMap, ioCostCaptureTestDump{
		items: object.defaultItems[ioCostWaitAggregateMap], entered: entered, release: release,
	})
	result := make(chan error, 1)
	go func() { _, err := collector.Update(); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Update did not start its map read")
	}
	harness.cancel()
	cleanupStarted := make(chan struct{})
	cleaned := make(chan struct{})
	go func() {
		close(cleanupStarted)
		collector.withdrawSession()
		_ = object.Close()
		close(cleaned)
	}()
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not start")
	}
	// Keep the read blocked while cleanup has an opportunity to withdraw it.
	select {
	case <-cleaned:
		t.Fatal("session cleanup completed while its map read was blocked")
	case <-time.After(20 * time.Millisecond):
	}
	closeCalls, _, _ := object.closeState()
	require.Zero(t, closeCalls)
	releaseRead()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Update did not finish after releasing its map read")
	}
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not finish after releasing its map read")
	}
	require.Nil(t, collector.session)
	closeCalls, readAfterClose, closeDuringRead := object.closeState()
	require.Equal(t, 1, closeCalls)
	require.False(t, readAfterClose)
	require.False(t, closeDuringRead)
}

func TestIOCostAttributionReusedCSSKeepsCounterBaselinesSeparate(t *testing.T) {
	oldContainer := newIOControlAttributionTestContainer(
		"old-container",
		ioCostCaptureTestCSS,
		time.Unix(100, 0),
	)
	newContainer := newIOControlAttributionTestContainer(
		"new-container",
		ioCostCaptureTestCSS,
		time.Unix(200, 0),
	)
	fixture := newIOCostIntervalTestFixture(
		t,
		1,
		map[string]*pod.Container{oldContainer.ID: oldContainer},
	)
	setIOCostIntervalTestRows(
		fixture.object,
		ioCostDefaultIntervalTestRow(10, 1000),
	)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 10, 1)

	fixture.source.containers = map[string]*pod.Container{
		newContainer.ID: newContainer,
	}
	newRow := ioCostDefaultIntervalTestRow(1, 200)
	newRow.cssSerial++
	setIOCostIntervalTestRows(fixture.object, newRow)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 1, 2)
	require.NotContains(t, fixture.collector.session.previous, ioControlWaitKey{
		CSS: newRow.css, Major: newRow.major, Minor: newRow.minor, CSSSerial: ioCostCaptureTestCSSSerial,
	})
	require.Contains(t, fixture.collector.session.previous, ioControlWaitKey{
		CSS: newRow.css, Major: newRow.major, Minor: newRow.minor, CSSSerial: newRow.cssSerial,
	})

	newRow.lanes = []ioCostCaptureTestLane{
		ioCostCaptureStableLane(2, 500),
	}
	setIOCostIntervalTestRows(fixture.object, newRow)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 1, 3)

	newRow.lanes = []ioCostCaptureTestLane{
		ioCostCaptureStableLane(3, 900),
	}
	setIOCostIntervalTestRows(fixture.object, newRow)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 1, 4)
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostMetricContract(t *testing.T) {
	container := newIOControlAttributionTestContainer(
		"container-a", ioCostCaptureTestCSS, time.Unix(100, 0))
	fixture := newIOCostIntervalTestFixture(t, 1, map[string]*pod.Container{container.ID: container})
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	got := make(map[string]struct{})
	for _, data := range metrics {
		require.Equal(t, metric.MetricTypeGauge, data.Type())
		require.NotEmpty(t, data.Help())
		got[prometheus.BuildFQName(metric.DefaultNamespace, ioCostTracingName, data.Name())] = struct{}{}
	}
	require.Equal(t, map[string]struct{}{
		"huatuo_bamai_iocost_waitq_io_count":            {},
		"huatuo_bamai_iocost_average_wait_ms":           {},
		"huatuo_bamai_iocost_container_waitq_io_count":  {},
		"huatuo_bamai_iocost_container_average_wait_ms": {},
	}, got)
}

func TestIOCostContainerFailuresPreserveHostIntervals(t *testing.T) {
	for _, failure := range []string{"query", "labels", "empty"} {
		t.Run(failure, func(t *testing.T) {
			container := newIOControlAttributionTestContainer(
				"stable-container", ioCostCaptureTestCSS, time.Unix(100, 0),
			)
			fixture := newIOCostIntervalTestFixture(t, 1,
				map[string]*pod.Container{container.ID: container})
			switch failure {
			case "query":
				// Even a nonempty result is unusable when its source failed.
				fixture.source.err = errors.New("kubelet not running")
			case "labels":
				container.Labels[ioControlHostNamespaceKey] = 123
			case "empty":
				fixture.source.containers = nil
			}

			// An unavailable catalog must not block the first or later intervals.
			for count := uint64(3); count <= 6; count += 3 {
				setIOCostIntervalTestRows(fixture.object,
					ioCostDefaultIntervalTestRow(count, count*300))
				metrics, err := fixture.collector.Update()
				require.NoError(t, err)
				requireIOCostHostAndOtherInterval(t, metrics, 3, 3)
				require.NotNil(t, fixture.collector.session.previous)
				require.Zero(t, fixture.harness.cancelCount())
			}

			container.Labels[ioControlHostNamespaceKey] = "host"
			fixture.source.err = nil
			fixture.source.containers = map[string]*pod.Container{container.ID: container}
			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(8, 2600))
			metrics, err := fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndContainerInterval(t, metrics, 2, 4)

			// A later outage must not reset an established container baseline.
			fixture.source.err = errors.New("kubelet request timeout")
			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(9, 2900))
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndOtherInterval(t, metrics, 1, 3)

			// Only a raw capture failure requires rebaselining. Unavailable
			// metadata must not prevent that recovery from completing either.
			captureErr := errors.New("temporary capture failure")
			fixture.object.queueDumps(ioCostWaitAggregateMap,
				ioCostCaptureTestDump{err: captureErr})
			_, err = fixture.collector.Update()
			require.ErrorIs(t, err, captureErr)
			if failure == "labels" {
				fixture.source.err = nil
				container.Labels[ioControlHostNamespaceKey] = 123
			}
			metrics, err = fixture.collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Nil(t, metrics)
			require.NotNil(t, fixture.collector.session.previous)
			require.Zero(t, fixture.harness.cancelCount())
		})
	}
}

type closeAwareIOCostCaptureBPF struct {
	*fakeIOCostCaptureBPF

	mu              sync.Mutex
	activeReads     int
	closed          bool
	closeCalls      int
	readAfterClose  bool
	closeDuringRead bool
}

func (object *closeAwareIOCostCaptureBPF) beginRead() {
	object.mu.Lock()
	defer object.mu.Unlock()
	if object.closed {
		object.readAfterClose = true
	}
	object.activeReads++
}

func (object *closeAwareIOCostCaptureBPF) endRead() {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.activeReads--
}

func (object *closeAwareIOCostCaptureBPF) DumpMap(
	mapID uint32,
) ([]bpf.MapItem, error) {
	object.beginRead()
	defer object.endRead()
	return object.fakeIOCostCaptureBPF.DumpMap(mapID)
}

func (object *closeAwareIOCostCaptureBPF) dumpBatch(mapID uint32) ([]bpf.MapItem, error) {
	return object.DumpMap(mapID)
}

func (object *closeAwareIOCostCaptureBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	object.beginRead()
	defer object.endRead()
	return object.fakeIOCostCaptureBPF.ReadMap(mapID, key)
}

func (object *closeAwareIOCostCaptureBPF) Close() error {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.closeCalls++
	if object.activeReads != 0 {
		object.closeDuringRead = true
	}
	object.closed = true
	return nil
}

func (object *closeAwareIOCostCaptureBPF) closeState() (
	closeCalls int,
	readAfterClose bool,
	closeDuringRead bool,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	return object.closeCalls, object.readAfterClose, object.closeDuringRead
}

type fakeIOCostBPF struct {
	bpf.BPF

	mu              sync.Mutex
	options         []bpf.AttachOption
	attachCalls     int
	attachFailAt    int
	attachErr       error
	statusMapID     uint32
	statusValue     []byte
	statusErrors    []error
	readCalls       int
	closeCalls      int
	closeErr        error
	closed          bool
	sessionVisible  func() bool
	visibleAtAttach bool
	visibleAtClose  bool
}

func newFakeIOCostBPF() *fakeIOCostBPF {
	return &fakeIOCostBPF{
		statusMapID: 1,
		statusValue: ioControlTestBytes(ioControlStatus{}),
	}
}

func (object *fakeIOCostBPF) AttachWithOptions(
	options []bpf.AttachOption,
) error {
	visible := false
	if object.sessionVisible != nil {
		visible = object.sessionVisible()
	}
	object.mu.Lock()
	defer object.mu.Unlock()
	object.attachCalls++
	object.visibleAtAttach = object.visibleAtAttach || visible
	for _, option := range options {
		object.options = append(object.options, option)
		if object.attachFailAt == len(object.options) {
			return object.attachErr
		}
	}
	return nil
}

func (object *fakeIOCostBPF) MapIDByName(name string) uint32 {
	object.mu.Lock()
	defer object.mu.Unlock()
	if name == ioControlStatusMap {
		return object.statusMapID
	}
	return 0
}

func (object *fakeIOCostBPF) ReadMap(
	_ uint32,
	_ []byte,
) ([]byte, error) {
	object.mu.Lock()
	object.readCalls++
	call := object.readCalls
	if object.closed {
		object.mu.Unlock()
		return nil, bpf.ErrClosed
	}
	var readErr error
	if call <= len(object.statusErrors) {
		readErr = object.statusErrors[call-1]
	}
	value := append([]byte(nil), object.statusValue...)
	object.mu.Unlock()

	if readErr != nil {
		return nil, readErr
	}
	return value, nil
}

func (*fakeIOCostBPF) DumpMap(
	uint32,
) ([]bpf.MapItem, error) {
	return nil, errors.New("unexpected IOCOST map dump")
}

func (object *fakeIOCostBPF) Close() error {
	visible := false
	if object.sessionVisible != nil {
		visible = object.sessionVisible()
	}
	object.mu.Lock()
	object.closeCalls++
	object.closed = true
	object.visibleAtClose = object.visibleAtClose || visible
	object.mu.Unlock()
	return object.closeErr
}

type fakeIOCostBPFState struct {
	options        []bpf.AttachOption
	attachCalls    int
	closeCalls     int
	visibleAtClose bool
}

func (object *fakeIOCostBPF) state() fakeIOCostBPFState {
	object.mu.Lock()
	defer object.mu.Unlock()
	return fakeIOCostBPFState{
		options:        append([]bpf.AttachOption(nil), object.options...),
		attachCalls:    object.attachCalls,
		closeCalls:     object.closeCalls,
		visibleAtClose: object.visibleAtClose,
	}
}

func ioCostSessionTestProfile() *ioCostKernelProfile {
	return ioCostTestKernelProfile(true)
}

func ioCostSessionTestAttachOptions(t *testing.T) []bpf.AttachOption {
	t.Helper()
	options, err := ioControlDeviceHooks()
	require.NoError(t, err)
	options = append(options, []bpf.AttachOption{
		{ProgramName: ioCostWakeReturnProgram, Symbol: ioCostWakeSymbol},
		{ProgramName: ioCostWakeEntryProgram, Symbol: ioCostWakeSymbol},
		{ProgramName: ioCostPDFreeProgram, Symbol: ioCostPDFreeSymbol},
	}...)
	return append(options, bpf.AttachOption{
		ProgramName: ioCostKickProgram,
		Symbol:      ioCostKickSymbol,
	})
}

func emptyIOCostContainerSource() (map[string]*pod.Container, error) {
	return nil, nil
}

func TestIOCostAttachFailureClosesObject(t *testing.T) {
	profile := ioCostSessionTestProfile()
	wantOptions := ioCostSessionTestAttachOptions(t)
	for failureAt := 1; failureAt <= len(wantOptions); failureAt++ {
		failureAt := failureAt
		t.Run(wantOptions[failureAt-1].ProgramName, func(t *testing.T) {
			attachErr := fmt.Errorf("attach failure: %w", unix.EPERM)
			closeErr := errors.New("close failure")
			object := newFakeIOCostBPF()
			object.attachFailAt = failureAt
			object.attachErr = attachErr
			if failureAt == len(wantOptions) {
				object.closeErr = closeErr
			}
			tracing := &iocostTracing{}
			object.sessionVisible = func() bool {
				tracing.mu.Lock()
				defer tracing.mu.Unlock()
				return tracing.session != nil
			}
			load := func(string, map[string]any) (bpf.BPF, error) {
				return object, nil
			}
			err := tracing.startWithProfile(
				t.Context(),
				load,
				profile,
				1,
				emptyIOCostContainerSource,
			)
			require.ErrorIs(t, err, attachErr)
			if failureAt == len(wantOptions) {
				require.ErrorIs(t, err, closeErr)
			}
			tracing.mu.Lock()
			require.Nil(t, tracing.session)
			tracing.mu.Unlock()

			state := object.state()
			require.Equal(t, wantOptions[:failureAt], state.options)
			require.Equal(t, 1, state.closeCalls)
			require.False(t, state.visibleAtClose)
			require.False(t, object.visibleAtAttach)
		})
	}
}

func TestIOCostStartPrePublishFailures(t *testing.T) {
	loadErr := errors.New("load failure")
	tests := []struct {
		name            string
		containerSource ioControlContainerSource
		load            ioControlBPFLoader
		wantErr         error
		wantContains    string
		wantLoadCalls   int
		wantCloseCalls  int
	}{
		{
			name:         "nil container source",
			wantContains: "container source is unavailable",
		},
		{
			name:            "load failure closes returned object",
			containerSource: emptyIOCostContainerSource,
			wantErr:         loadErr,
			wantLoadCalls:   1,
			wantCloseCalls:  1,
		},
		{
			name:            "nil object",
			containerSource: emptyIOCostContainerSource,
			wantContains:    "nil object",
			wantLoadCalls:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeIOCostBPF()
			loadCalls := 0
			load := test.load
			if load == nil {
				load = func(string, map[string]any) (bpf.BPF, error) {
					loadCalls++
					switch test.name {
					case "load failure closes returned object":
						return object, loadErr
					case "nil object":
						return nil, nil
					default:
						return object, nil
					}
				}
			}
			tracing := &iocostTracing{}
			err := tracing.startWithProfile(
				t.Context(), load, ioCostSessionTestProfile(), 1,
				test.containerSource,
			)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.ErrorContains(t, err, test.wantContains)
			}
			require.Equal(t, test.wantLoadCalls, loadCalls)
			state := object.state()
			require.Zero(t, state.attachCalls)
			require.Equal(t, test.wantCloseCalls, state.closeCalls)
			require.Nil(t, tracing.session)
		})
	}
}

func TestIOCostStatusFailureReasons(t *testing.T) {
	// Map errno distinguishes capacity exhaustion from other helper failures;
	// each reported contract failure must stop tracing rather than retry it.
	tests := []struct {
		name   string
		status ioControlStatus
		reason string
	}{
		{name: "healthy"},
		{
			name:   "pending capacity exhausted",
			status: ioControlTestStatus(ioCostFailurePendingInsert, -int32(unix.E2BIG)),
			reason: "iocost_pending_map capacity exhausted (10240 entries)",
		},
		{
			name:   "pending allocation failure",
			status: ioControlTestStatus(ioCostFailurePendingInsert, -int32(unix.ENOMEM)),
			reason: "insert pending: helper returned -12",
		},
		{
			name:   "duplicate pending restoration",
			status: ioControlTestStatus(ioCostFailurePendingCollision, 0),
			reason: "pending already exists while restoring an uncommitted waiter",
		},
		{
			name:   "required pending disappeared",
			status: ioControlTestStatus(ioCostFailurePendingDelete, -int32(unix.ENOENT)),
			reason: "pending missing or deletion failed: helper returned -2",
		},
		{
			name:   "aggregate allocation failure",
			status: ioControlTestStatus(ioCostFailureAggregateInsert, -int32(unix.ENOMEM)),
			reason: "aggregate creation or lookup failed: helper returned -12",
		},
		{
			name:   "retired capacity exhausted",
			status: ioControlTestStatus(ioCostFailureRetire, -int32(unix.E2BIG)),
			reason: "publish retired IOCOST identity failed (capacity 4096): helper returned -7",
		},
		{
			name:   "identity mismatch",
			status: ioControlTestStatus(ioCostFailureIdentity, 0),
			reason: "kernel identity could not be read, created or validated",
		},
		{
			name:   "wake contract",
			status: ioControlTestStatus(ioCostFailureWakeFrame, 0),
			reason: "wake entry/return state or return value violates the hook contract",
		},
		{
			name:   "time rollback",
			status: ioControlTestStatus(ioCostFailureTimeRollback, 0),
			reason: "wait end precedes its recorded start",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ioCostFailure(test.status.Failure)
			if test.reason == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorContains(t, err, test.reason)
			if int32(test.status.Failure) == -int32(unix.ENOMEM) {
				require.NotContains(t, err.Error(), "capacity exhausted")
			}
		})
	}
}

// Compose the attach and capture fakes to verify that Update's terminal cause
// survives Start's session cleanup.
type ioCostSessionCaptureBPF struct {
	*fakeIOCostBPF
	capture *fakeIOCostCaptureBPF
}

func (object *ioCostSessionCaptureBPF) MapIDByName(name string) uint32 {
	return object.capture.MapIDByName(name)
}

func (object *ioCostSessionCaptureBPF) ReadMap(id uint32, key []byte) ([]byte, error) {
	return object.capture.ReadMap(id, key)
}

func (object *ioCostSessionCaptureBPF) DumpMap(id uint32) ([]bpf.MapItem, error) {
	return object.capture.DumpMap(id)
}

func (object *ioCostSessionCaptureBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	return object.capture.DumpMapByName(name)
}

func (object *ioCostSessionCaptureBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	return object.capture.DeleteMapItems(id, keys)
}

func (object *ioCostSessionCaptureBPF) dumpBatch(id uint32) ([]bpf.MapItem, error) {
	return object.DumpMap(id)
}

func waitIOCostTestSession(t *testing.T, collector *iocostTracing) *ioCostSession {
	t.Helper()
	var session *ioCostSession
	require.Eventually(t, func() bool {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		session = collector.session
		if session != nil {
			if object, ok := session.object.(ioControlTestBPF); ok {
				session.hashReader.lookupBatch = object.dumpBatch
			}
		}
		return session != nil
	}, time.Second, time.Millisecond)
	return session
}

func TestIOCostStartSequenceAndCleanup(t *testing.T) {
	object := newFakeIOCostBPF()
	collector := &iocostTracing{}
	object.sessionVisible = func() bool {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		return collector.session != nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- collector.startWithProfile(ctx,
			func(name string, constants map[string]any) (bpf.BPF, error) {
				if name != ioCostObjectName {
					return nil, fmt.Errorf("object %s", name)
				}
				return object, nil
			}, ioCostSessionTestProfile(), 4, emptyIOCostContainerSource)
	}()
	session := waitIOCostTestSession(t, collector)
	require.Same(t, object, session.object)
	require.Equal(t, 4, session.possibleCPUs)
	require.NotNil(t, session.previous)
	require.Empty(t, session.previous)
	require.Equal(t, ioCostSessionTestAttachOptions(t), object.state().options)
	require.False(t, object.visibleAtAttach)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("IOCOST Start did not finish")
	}
	require.Nil(t, collector.session)
	state := object.state()
	require.Equal(t, 1, state.closeCalls)
	require.False(t, state.visibleAtClose)
}

func TestIOCostStartStopsOnUpdate(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    ioControlStatus
		updateErr error
		startErr  error
	}{
		{
			name: "device change", status: ioControlStatus{DevicesChanged: 1},
			updateErr: errIOControlDevicesChanged,
		},
		{
			name: "fatal accounting error", status: ioControlTestStatus(ioCostFailureWakeFrame, 0),
			updateErr: types.ErrTracingStopped, startErr: types.ErrTracingStopped,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &ioCostSessionCaptureBPF{
				fakeIOCostBPF: newFakeIOCostBPF(),
				capture:       newFakeIOCostCaptureBPF(1),
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			collector := &iocostTracing{}
			done := make(chan error, 1)
			go func() {
				done <- collector.startWithProfile(
					ctx,
					func(string, map[string]any) (bpf.BPF, error) { return object, nil },
					ioCostSessionTestProfile(),
					1,
					emptyIOCostContainerSource,
				)
			}()
			waitIOCostTestSession(t, collector)
			object.capture.statusValue = ioControlTestBytes(test.status)
			object.capture.deviceChanges = test.status.DevicesChanged
			data, err := collector.Update()
			require.Nil(t, data)
			require.ErrorIs(t, err, test.updateErr)
			select {
			case err := <-done:
				if test.startErr == nil {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, test.startErr)
				}
			case <-time.After(time.Second):
				t.Fatal("Update did not end IOCOST Start")
			}
			require.Nil(t, collector.session)
			require.Equal(t, 1, object.state().closeCalls)
		})
	}
}

func TestIOCostFactory(t *testing.T) {
	attr, err := newIOCost()
	require.NoError(t, err)
	require.NotNil(t, attr)
	require.Equal(t, 10, attr.Interval)
	require.Equal(t, tracing.FlagTracing|tracing.FlagMetric, attr.Flag)
	data, ok := attr.TracingData.(*iocostTracing)
	require.True(t, ok)
	require.NotNil(t, data)
	require.Same(t, data, attr.TracingData.(interface {
		Start(ctx context.Context) error
	}))
	require.Same(t, data, attr.TracingData.(metric.Collector))
}

func TestIOCostObjectContract(t *testing.T) {
	spec := loadIOCostObjectSpec(t)
	requireIOCostPrograms(t, spec)
	wantMaps := ioCostBusinessMapContracts()
	require.Len(t, spec.Maps, len(wantMaps)+1,
		"compiled object must contain only the business maps and .rodata")
	_, hasRodata := spec.Maps[".rodata"]
	require.True(t, hasRodata, "compiled object has no .rodata map")
	requireIOCostBusinessMaps(t, spec)
	require.NotContains(t, spec.Maps, ioCostDiagnosticStatusMap)
	require.NotContains(t, spec.Maps, ioCostDiagnosticScratchMap)
	requireIOCostProductionObjectExcludesDiagnostics(t, spec)
	require.Equal(t, ioCostProductionRodataContract(), ioCostRodataContract(t, spec))
	require.NoError(t, spec.RewriteConstants(ioCostTestKernelProfile(true).constants()))
}

func TestIOCostObject(t *testing.T) {
	production := loadIOCostObjectSpec(t)
	diagnostic := loadIOCostDiagnosticObjectSpec(t)
	t.Run("Relocations", func(t *testing.T) {
		testIOCostObjectRelocations(t, production, diagnostic)
	})
	t.Run("DiagnosticContract", func(t *testing.T) {
		testIOCostDiagnosticObjectContract(t, diagnostic)
	})
}

// Both disk layouts and same-named CSS records must relocate without BTF FUNCs.
func testIOCostObjectRelocations(t *testing.T, objects ...*ebpf.CollectionSpec) {
	t.Helper()
	for _, object := range objects {
		relocations := []*btf.CORERelocation{}
		instructions := []asm.Instruction{}
		for _, program := range object.Programs {
			for index := range program.Instructions {
				if relocation := btf.CORERelocationMetadata(&program.Instructions[index]); relocation != nil {
					relocations = append(relocations, relocation)
					instructions = append(instructions, program.Instructions[index])
				}
			}
		}
		require.NotEmpty(t, relocations)
		// Build accessor names from the compiled local BTF; its member indices
		// vary with the build headers. Target offsets are independent fixture values.
		wantOffsets := make(map[string]int64)
		fieldNames := make(map[string]string)
		for _, field := range []struct {
			typeName string
			path     string
			offset   int64
		}{
			{typeName: "bio", path: "bi_opf", offset: 8},
			{typeName: "bio", path: "bi_disk", offset: 16},
			{typeName: "bio___5_12", path: "bi_bdev", offset: 24},
			{typeName: "block_device", path: "bd_disk"},
			{typeName: "ioc_gq___iocost", path: "pd", offset: 8},
			{typeName: "ioc_gq___iocost", path: "pd.blkg", offset: 8},
			{typeName: "ioc_gq___iocost", path: "waitq", offset: 40},
			{typeName: "iocg_wait___iocost", path: "wait", offset: 8},
			{typeName: "iocg_wait___iocost", path: "bio", offset: 40},
			{typeName: "iocg_wake_ctx___iocost", path: "iocg"},
			{typeName: "wait_queue_head", path: "head.prev", offset: 16},
			{typeName: "wait_queue_entry", path: "entry"},
			{typeName: "blkcg_gq", path: "blkcg"},
			{typeName: "blkcg", path: "css"},
			{typeName: "blkcg", path: "css.serial_nr"},
			{typeName: "gendisk", path: "major"},
			{typeName: "gendisk", path: "first_minor", offset: 4},
			{typeName: "gendisk", path: "disk_name", offset: 8},
			{typeName: "pt_regs", path: "ax"},
			{typeName: "pt_regs", path: "cx", offset: 8},
			{typeName: "pt_regs", path: "di", offset: 16},
			{typeName: "pt_regs", path: "si", offset: 24},
			{typeName: "pt_regs", path: "sp", offset: 32},
		} {
			var local *btf.Struct
			require.NoError(t, object.Types.TypeByName(field.typeName, &local))
			id, err := object.Types.TypeID(local)
			require.NoError(t, err)
			accessor := "0"
			var current btf.Type = local
			for _, name := range strings.Split(field.path, ".") {
				structure, ok := btf.UnderlyingType(current).(*btf.Struct)
				require.True(t, ok, "local field "+field.typeName+"."+field.path)
				index := slices.IndexFunc(structure.Members, func(member btf.Member) bool {
					return member.Name == name
				})
				require.True(t, index >= 0, "local field "+field.typeName+"."+field.path)
				accessor += ":" + strconv.Itoa(index)
				current = structure.Members[index].Type
			}
			key := fmt.Sprintf(
				"CORERelocation(byte_off, %s[%s], local_id=%d)",
				local,
				accessor,
				id,
			)
			wantOffsets[key] = field.offset
			fieldNames[key] = field.typeName + "." + field.path
		}
		for _, test := range []struct {
			name      string
			device    syntheticIOCostDeviceProfile
			duplicate int
		}{
			{name: "bio disk", device: syntheticIOCost510Profile},
			{name: "bio bdev", device: syntheticIOCostMainlineProfile},
			{name: "unrelated CSS first", device: syntheticIOCost510Profile, duplicate: -1},
			{name: "unrelated CSS last", device: syntheticIOCost510Profile, duplicate: 1},
		} {
			t.Run(test.name, func(t *testing.T) {
				target := newSyntheticIOCostBTFSpec(t, test.device, func(fixture *syntheticIOCostBTFFixture) {
					if test.duplicate == 0 {
						return
					}
					duplicate := btf.Copy(fixture.structures["cgroup_subsys_state"]).(*btf.Struct)
					duplicate.Members[0].Offset = 64
					if test.duplicate < 0 {
						fixture.types = append([]btf.Type{duplicate}, fixture.types...)
					} else {
						fixture.types = append(fixture.types, duplicate)
					}
				})
				fixups, err := btf.CORERelocate(
					relocations,
					[]*btf.Spec{target},
					object.ByteOrder,
					object.Types.TypeID,
				)
				require.NoError(t, err)
				require.Len(t, fixups, len(relocations))
				seen := make(map[string]bool)
				hasLayoutSelector := false
				for index, fixup := range fixups {
					key := relocations[index].String()
					if strings.HasPrefix(key, "CORERelocation(field_exists,") {
						key = strings.Replace(key, "field_exists", "byte_off", 1)
						require.Equal(t, "bio.bi_disk", fieldNames[key])
						instruction := instructions[index]
						require.NoError(t, fixup.Apply(&instruction))
						want := int64(0)
						if test.device == syntheticIOCost510Profile {
							want = 1
						}
						require.Equal(t, want, instruction.Constant)
						hasLayoutSelector = true
						continue
					}
					want, ok := wantOffsets[key]
					require.True(t, ok, "unqualified relocation: "+key)
					field := fieldNames[key]
					seen[field] = true
					isUnselected := field == "bio.bi_disk"
					if test.device == syntheticIOCost510Profile {
						isUnselected = field == "bio___5_12.bi_bdev" || field == "block_device.bd_disk"
					}
					if isUnselected {
						continue // The existence check removes this branch before verification.
					}
					instruction := instructions[index]
					require.NoError(t, fixup.Apply(&instruction))
					require.NotEqualValues(
						t,
						btf.COREBadRelocationSentinel,
						instruction.Constant,
						"required field %s",
						field,
					)
					class := instruction.OpCode.Class()
					isMemoryAccess := class == asm.LdXClass || class == asm.StClass || class == asm.StXClass
					if isMemoryAccess {
						require.EqualValues(
							t,
							want,
							instruction.Offset,
							"required field "+field,
						)
					} else {
						require.Equal(
							t,
							want,
							instruction.Constant,
							"required field "+field,
						)
					}
				}
				require.True(t, hasLayoutSelector, "bio disk layout selector is missing")
				for _, field := range []string{
					"bio.bi_disk", "bio___5_12.bi_bdev", "blkcg.css.serial_nr", "iocg_wait___iocost.bio",
				} {
					require.True(t, seen[field], "required relocation missing: "+field)
				}
			})
		}
	}
}

func testIOCostDiagnosticObjectContract(t *testing.T, spec *ebpf.CollectionSpec) {
	t.Helper()
	requireIOCostPrograms(t, spec)
	requireIOCostBusinessMaps(t, spec)

	wantMaps := ioCostBusinessMapContracts()
	require.Len(t, spec.Maps, len(wantMaps)+3,
		"diagnostic object must contain the business maps, two diagnostic maps, and .rodata")
	requireIOControlTestMap(t, spec, ioControlTestMapContract{
		name: ioCostDiagnosticStatusMap, typeID: ebpf.Array,
		keySize: ioCostUint32Size, valueSize: ioCostDiagnosticStatusSize,
		maxEntries: 1,
	})
	status, ok := spec.Maps[ioCostDiagnosticStatusMap].Value.(*btf.Struct)
	require.True(t, ok, "diagnostic status must retain its BTF struct")
	fields := []string{
		"start_guard_passes", "wake_entry_hits", "wake_return_zero",
		"wake_return_minus_one", "settled_count", "map_full_injections",
		"collision_injections", "delete_failure_injections",
	}
	require.Len(t, status.Members, len(fields))
	for index, field := range fields {
		member := status.Members[index]
		require.Equal(t, field, member.Name)
		require.Equal(t, btf.Bits(index*64), member.Offset, field)
		size, err := btf.Sizeof(member.Type)
		require.NoError(t, err)
		require.Equal(t, ioCostUint64Size, size, field)
	}
	requireIOControlTestMap(t, spec, ioControlTestMapContract{
		name: ioCostDiagnosticScratchMap, typeID: ebpf.Hash,
		keySize: ioCostUint32Size, valueSize: ioCostUint64Size,
		maxEntries: 1,
	})
	_, hasRodata := spec.Maps[".rodata"]
	require.True(t, hasRodata, "diagnostic object has no .rodata map")

	want := ioCostProductionRodataContract()
	want[ioCostDiagnosticFaultMaskConstant] = 4
	want[ioCostDiagnosticMajorConstant] = 4
	want[ioCostDiagnosticFirstMinorConstant] = 4
	want[ioCostDiagnosticCSSSerialConstant] = 8
	require.Equal(t, want, ioCostRodataContract(t, spec))

	constants := ioCostTestKernelProfile(true).constants()
	constants[ioCostDiagnosticFaultMaskConstant] = ioCostDiagnosticFaultDeleteFailure
	constants[ioCostDiagnosticMajorConstant] = uint32(8)
	constants[ioCostDiagnosticFirstMinorConstant] = uint32(16)
	constants[ioCostDiagnosticCSSSerialConstant] = uint64(0x1020304050607080)
	require.NoError(t, spec.RewriteConstants(constants))
}

func ioCostBusinessMapContracts() []ioControlTestMapContract {
	return []ioControlTestMapContract{
		{
			name: ioCostOwnerStateMap, typeID: ebpf.Hash,
			keySize: ioCostUint64Size, valueSize: ioCostOwnerStateSize,
			maxEntries: ioCostOwnerStateMapMaxEntries,
		},
		{
			name: ioCostPendingMap, typeID: ebpf.Hash,
			keySize: ioCostUint64Size, valueSize: ioCostPendingSize,
			maxEntries: ioCostPendingMapMaxEntries,
		},
		{
			name: ioCostWaitAggregateMap, typeID: ebpf.PerCPUHash,
			keySize: ioCostWaitKeySize, valueSize: uint32(ioControlWaitCounterSize),
			maxEntries: ioCostWaitAggregateMapMaxEntries,
			flags:      unix.BPF_F_NO_PREALLOC,
		},
		{
			name:       ioCostRetiredMap,
			typeID:     ebpf.Hash,
			keySize:    24,
			valueSize:  1,
			maxEntries: ioCostOwnerStateMapMaxEntries,
		},
		{
			name: ioCostWakeFrameMap, typeID: ebpf.PerCPUArray,
			keySize: ioCostUint32Size, valueSize: ioCostWakeFrameSize,
			maxEntries: ioCostWakeFrameMapMaxEntries,
		},
		{
			name: ioControlStatusMap, typeID: ebpf.Array,
			keySize: ioCostUint32Size, valueSize: ioCostStatusSize,
			maxEntries: ioCostStatusMapMaxEntries,
		},
	}
}

func requireIOCostPrograms(t *testing.T, spec *ebpf.CollectionSpec) {
	t.Helper()
	want := []string{
		ioCostKickProgram,
		ioCostWakeEntryProgram,
		ioCostWakeReturnProgram,
		ioCostPDFreeProgram,
		"io_control_device_removed",
		"io_control_device_changed",
		"io_control_disk_added",
	}
	got := make([]string, 0, len(spec.Programs))
	for name := range spec.Programs {
		got = append(got, name)
	}
	require.Len(t, got, len(want))
	require.ElementsMatch(t, want, got)
}

func requireIOCostBusinessMaps(t *testing.T, spec *ebpf.CollectionSpec) {
	t.Helper()
	for _, contract := range ioCostBusinessMapContracts() {
		requireIOControlTestMap(t, spec, contract)
	}
}

func requireIOCostProductionObjectExcludesDiagnostics(
	t *testing.T,
	spec *ebpf.CollectionSpec,
) {
	t.Helper()
	for name := range spec.Maps {
		require.NotContains(t, name, "iocost_diag_",
			"production object contains a diagnostic map")
	}
	for name := range ioCostRodataContract(t, spec) {
		require.NotContains(t, name, "iocost_diag_",
			"production object contains a diagnostic constant")
	}
	for name, program := range spec.Programs {
		require.NotContains(t, name, "iocost_diag_",
			"production object contains a diagnostic program")
		for _, instruction := range program.Instructions {
			require.NotContains(t, instruction.Reference(), "iocost_diag_",
				"production program %s references diagnostic code", name)
		}
	}
}

func ioCostProductionRodataContract() map[string]uint32 {
	return map[string]uint32{
		ioCostThrottleCallerStartConstant:   8,
		ioCostThrottleCallerEndConstant:     8,
		ioCostOverBudgetCallerStartConstant: 8,
		ioCostOverBudgetCallerEndConstant:   8,
	}
}

func ioCostRodataContract(
	t *testing.T,
	spec *ebpf.CollectionSpec,
) map[string]uint32 {
	t.Helper()
	rodata, ok := spec.Maps[".rodata"]
	require.True(t, ok, "compiled object has no .rodata map")
	datasec, ok := rodata.Value.(*btf.Datasec)
	require.True(t, ok, ".rodata value BTF is %T", rodata.Value)
	require.Equal(t, ".rodata", datasec.Name)

	got := make(map[string]uint32, len(datasec.Vars))
	for _, info := range datasec.Vars {
		variable, ok := info.Type.(*btf.Var)
		require.True(t, ok, ".rodata entry is %T", info.Type)
		// ABI anchors retain types in BTF; they are not runtime settings.
		if strings.HasPrefix(variable.Name, "__bpf_abi_") {
			continue
		}
		require.Equal(t, btf.GlobalVar, variable.Linkage,
			"rodata constant %s linkage", variable.Name)
		_, duplicate := got[variable.Name]
		require.False(t, duplicate,
			"duplicate rodata constant %s", variable.Name)
		got[variable.Name] = info.Size
	}
	return got
}

func decodeIOCostDiagnosticStatus(
	data []byte,
) (ioCostDiagnosticStatus, error) {
	if err := requireIOCostDataSize(data, ioCostDiagnosticStatusSize); err != nil {
		return ioCostDiagnosticStatus{}, err
	}
	return ioCostDiagnosticStatus{
		StartGuardPasses:        binary.LittleEndian.Uint64(data[0:8]),
		WakeEntryHits:           binary.LittleEndian.Uint64(data[8:16]),
		WakeReturnZero:          binary.LittleEndian.Uint64(data[16:24]),
		WakeReturnMinusOne:      binary.LittleEndian.Uint64(data[24:32]),
		SettledCount:            binary.LittleEndian.Uint64(data[32:40]),
		MapFullInjections:       binary.LittleEndian.Uint64(data[40:48]),
		CollisionInjections:     binary.LittleEndian.Uint64(data[48:56]),
		DeleteFailureInjections: binary.LittleEndian.Uint64(data[56:]),
	}, nil
}

func loadIOCostObjectSpec(t *testing.T) *ebpf.CollectionSpec {
	t.Helper()
	object := filepath.Join("..", "..", "bpf", ioCostObjectName)
	spec, err := ebpf.LoadCollectionSpec(object)
	require.NoError(t, err)
	return spec
}

func loadIOCostDiagnosticObjectSpec(t *testing.T) *ebpf.CollectionSpec {
	t.Helper()
	root := filepath.Join("..", "..")
	object := filepath.Join(t.TempDir(), ioCostDiagnosticObjectName)
	command := exec.CommandContext(t.Context(), filepath.Join(root, "build", "clang.sh"),
		"-I", filepath.Join(root, "bpf", "include"),
		"-s", filepath.Join(root, "integration", "testdata", "iocost_tracing_test.c"),
		"-o", object)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	spec, err := ebpf.LoadCollectionSpec(object)
	require.NoError(t, err)
	return spec
}

func ioCostTestKernelProfile(
	optional bool,
) *ioCostKernelProfile {
	overBudget := symbol.KsymbolRange{}
	if optional {
		overBudget = symbol.KsymbolRange{Start: 0x3000, End: 0x3100}
	}
	profile := &ioCostKernelProfile{
		throttleRange:   symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
		overBudgetRange: overBudget,
	}
	return profile
}
