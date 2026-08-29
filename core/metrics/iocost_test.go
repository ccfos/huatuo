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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// BPF accounting.

// Packed wait accounting and device layout.

// These tests run the production admission and wake functions with deterministic
// kernel reads and map helpers. Missing calls model skipped probes; kernel load
// and real IRQ concurrency are covered separately by qualification.

// Exercise the production device reader with each independent field layout.
// The fixture exposes both fields physically; field_exists selects what the
// modeled kernel provides, just as CO-RE does when loading the BPF object.
func TestIOCostDeviceFieldSelection(t *testing.T) {
	source := ioCostCSource(t)
	reader := ioCostSourceSection(t, source,
		"static __always_inline int iocost_read_bio(",
		"static __always_inline u64 iocost_allocate_ioc_id")
	program := `
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
typedef uint64_t u64;
typedef uint32_t u32;
typedef int32_t s32;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define IOCOST_CORE_READ(dst, src, field) (*(dst) = (src)->field, true)
#define compat_bpf_core_field_offset(field) 0
#define bpf_core_field_exists(field) field_exists(#field)
#define REQ_OP_MASK 0xff
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
struct blkcg_gq { int unused; };
struct request_queue { int unused; };
struct gendisk { int major, first_minor; struct request_queue *queue; };
struct block_device { struct gendisk *bd_disk; };
struct rq_qos { struct request_queue *q; struct gendisk *disk; };
struct rq_qos___iocost_mainline { struct request_queue *q; struct gendisk *disk; };
struct ioc { struct rq_qos rqos; };
struct ioc___iocost { struct rq_qos rqos; };
struct bio {
    struct gendisk *bi_disk; struct block_device *bi_bdev;
    u32 bi_opf; struct blkcg_gq *bi_blkg;
};
struct bio___iocost_mainline {
    struct gendisk *bi_disk; struct block_device *bi_bdev;
    u32 bi_opf; struct blkcg_gq *bi_blkg;
};
static bool has_bdev, has_rq_disk;
static bool field_exists(const char *field)
{
    if (strstr(field, "bi_bdev"))
        return has_bdev;
    if (strstr(field, "bi_disk"))
        return !has_bdev;
    if (strstr(field, "->disk"))
        return has_rq_disk;
    return !has_rq_disk;
}
` + reader + `
int main(int argc, char **argv)
{
    struct request_queue queue = {}, other_queue = {};
    struct gendisk disk = { .major = 8, .first_minor = 16, .queue = &queue };
    struct gendisk other_disk = { .major = 8, .first_minor = 32, .queue = &other_queue };
    struct block_device bdev = { .bd_disk = &disk };
    struct bio request_bio = { .bi_disk = &disk, .bi_bdev = &bdev };
    struct ioc controller = { .rqos = { .q = &queue, .disk = &disk } };
    u32 major = 0, minor = 0;
    bool mismatch;
    struct blkcg_gq group = {}, *observed = NULL;
    u32 operation = 0;
    request_bio.bi_blkg = &group;
    for (u32 op = REQ_OP_READ; op <= REQ_OP_WRITE; op++) {
        request_bio.bi_opf = op | (1U << 8);
        if (iocost_read_bio(&request_bio, &observed, &operation) != 1 ||
            observed != &group || operation != op)
            return 4;
    }
    request_bio.bi_blkg = NULL;
    request_bio.bi_opf = 3;
    if (iocost_read_bio(&request_bio, &observed, &operation) != 0)
        return 5;
    request_bio.bi_opf = REQ_OP_READ;
    if (iocost_read_bio(&request_bio, &observed, &operation) != -1 ||
        iocost_read_bio(NULL, &observed, &operation) != -1)
        return 6;
    if (argc != 4)
        return 1;
    has_bdev = atoi(argv[1]);
    has_rq_disk = atoi(argv[2]);
    mismatch = atoi(argv[3]);
    if (has_bdev)
        request_bio.bi_disk = NULL;
    else
        request_bio.bi_bdev = NULL;
    if (has_rq_disk)
        controller.rqos.q = NULL;
    else
        controller.rqos.disk = NULL;
    if (mismatch) {
        controller.rqos.disk = &other_disk;
        controller.rqos.q = &other_queue;
    }
    if (iocost_read_device(&controller, &request_bio, &major, &minor) == mismatch)
        return 2;
    if (!mismatch && (major != 8 || minor != 16))
        return 3;
    return 0;
}
`
	directory := t.TempDir()
	path := filepath.Join(directory, "device.c")
	require.NoError(t, os.WriteFile(path, []byte(program), 0o600))
	executable := filepath.Join(directory, "device")
	command := exec.CommandContext(t.Context(), "cc", "-O2", "-fno-strict-aliasing", "-o", executable, path)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	for _, test := range []struct {
		name   string
		bdev   string
		rqDisk string
	}{
		{"bio disk, rq queue", "0", "0"},
		{"bio bdev, rq queue", "1", "0"},
		{"bio bdev, rq disk", "1", "1"},
		{"bio disk, rq disk", "0", "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, mismatch := range []string{"0", "1"} {
				command := exec.CommandContext(t.Context(), executable, test.bdev, test.rqDisk, mismatch)
				output, err := command.CombinedOutput()
				require.NoError(t, err, "mismatch=%s: %s", mismatch, output)
			}
		})
	}
}

func TestIOCostDiagnosticFixtureFiltering(t *testing.T) {
	source := ioCostDiagnosticCSource(t)
	program := `
#include <stdbool.h>
#include <stdint.h>
typedef uint64_t u64;
typedef uint32_t u32;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
static u64 iocost_diag_iocg_ptr = 100, iocost_diag_css_serial = 300;
static u32 iocost_diag_major = 8, iocost_diag_first_minor = 16;
static u32 iocost_diag_fault_mask;
` + ioCostSourceSection(t, source, "#define IOCOST_DIAG_FAULT_MAP_FULL", "volatile const u64 iocost_diag_iocg_ptr") +
		ioCostSourceSection(t, source, "static __always_inline bool iocost_diag_target_iocg", "static __always_inline bool\niocost_diag_claim_fault") +
		ioCostSourceSection(t, source, "static __always_inline bool iocost_diag_counter_filter_valid", "#define DEFINE_IOCOST_DIAG_TARGET_COUNTER") + `
int main(void)
{
    u32 valid_masks[] = {0, IOCOST_DIAG_FAULT_MAP_FULL,
        IOCOST_DIAG_FAULT_COLLISION, IOCOST_DIAG_FAULT_DELETE_FAILURE};
    for (u32 i = 0; i < sizeof(valid_masks) / sizeof(valid_masks[0]); i++) {
        iocost_diag_fault_mask = valid_masks[i];
        if (!iocost_diag_fixture_matches(100, 300, 8, 16) ||
            !iocost_diag_counter_filter_valid(100))
            return 1;
    }
    if (iocost_diag_fixture_matches(101, 300, 8, 16) ||
        iocost_diag_fixture_matches(100, 301, 8, 16) ||
        iocost_diag_fixture_matches(100, 300, 9, 16) ||
        iocost_diag_fixture_matches(100, 300, 8, 17) ||
        iocost_diag_counter_filter_valid(101))
        return 2;
    iocost_diag_fault_mask = IOCOST_DIAG_FAULT_MAP_FULL | IOCOST_DIAG_FAULT_COLLISION;
    if (iocost_diag_fixture_matches(100, 300, 8, 16) ||
        iocost_diag_counter_filter_valid(100) ||
        iocost_diag_fault_mask_valid(8))
        return 3;
    iocost_diag_fault_mask = 0;
    iocost_diag_css_serial = 0;
    if (iocost_diag_fixture_matches(100, 0, 8, 16) ||
        iocost_diag_counter_filter_valid(100))
        return 4;
    iocost_diag_css_serial = 300;
    iocost_diag_iocg_ptr = 0;
    if (iocost_diag_fixture_matches(0, 300, 8, 16) ||
        iocost_diag_counter_filter_valid(0))
        return 5;
    return 0;
}
`
	directory := t.TempDir()
	path := filepath.Join(directory, "filter.c")
	require.NoError(t, os.WriteFile(path, []byte(program), 0o600))
	executable := filepath.Join(directory, "filter")
	command := exec.CommandContext(t.Context(), "cc", "-O2", "-o", executable, path)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	output, err = exec.CommandContext(t.Context(), executable).CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestIOCostWakePackedPublication(t *testing.T) {
	source := ioCostCSource(t)
	section := func(start, end string) string {
		return ioCostSourceSection(t, source, start, end)
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
struct ioc;
struct ioc_gq { int unused; };
struct wait_queue_entry { int unused; };
struct iocg_wait___iocost { struct wait_queue_entry wait; struct bio *bio; };
struct iocg_wake_ctx___iocost { struct ioc_gq *iocg; };
` + section("#define IOCOST_WAIT_COUNT_BITS", "#ifndef REQ_OP_MASK") +
		section("struct iocost_ioc_state {", "#define IOCOST_ASSERT_OFFSET") +
		section("struct iocost_live_identity {", "static __always_inline struct iocost_status") + `
static int iocost_pending_map, iocost_owner_state_map, iocost_ioc_state_map;
static int iocost_wait_agg_map, iocost_wake_frame_map, iocost_stat_map;
static struct iocost_pending pending;
static struct iocost_owner_state owner;
static struct iocost_ioc_state ioc;
static struct iocost_wake_frame frame;
static struct iocost_status status;
static u64 aggregate, aggregate_at_delete, aggregate_lookups;
static u64 ioc_lookups;
static u64 ioc_key = 1;
static bool pending_live;
static long delete_ret;
static long update_ret;
static int missing_aggregate;
static int ioc_value_change;
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
    if (map == &iocost_ioc_state_map) {
        if (++ioc_lookups > 1 &&
            (missing_aggregate == 2 || *(const u64 *)key != ioc_key))
            return NULL;
        if (ioc_lookups == 1 && ioc_value_change) {
            /* Reuse after lookup, before the caller reads the retained value. */
            ioc.ioc_id = ioc_value_change % 2 ? 2 : 0;
            if (ioc_value_change > 2)
                ioc_key = 2;
        }
        return &ioc;
    }
    if (map == &iocost_wait_agg_map) {
        aggregate_lookups++;
        return missing_aggregate ? NULL : &aggregate;
    }
    if (map == &iocost_wake_frame_map)
        return &frame;
    if (map == &iocost_stat_map)
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
static int iocost_read_bio(struct bio *bio, struct blkcg_gq **blkg, u32 *operation)
{
    *blkg = (void *)1;
    *operation = REQ_OP_READ;
    return 1;
}
static bool iocost_read_live_identity(struct ioc_gq *group, struct iocost_live_identity *out)
{
    *out = (struct iocost_live_identity){
        .ioc_ptr = 1, .iocg_ptr = (u64)group, .css = 1, .css_serial = 1,
        .blkg = (void *)1,
    };
    return true;
}
static bool iocost_read_device(struct ioc *ioc, struct bio *bio, u32 *major, u32 *minor)
{
    *major = 1;
    *minor = 1;
    return true;
}
static u64 iocost_ensure_ioc(u64 ptr, u32 major, u32 minor) { return 1; }
static bool iocost_ensure_owner(const struct iocost_live_identity *identity, u64 id) { return true; }
static bool iocost_ensure_aggregate(const struct iocost_wait_key *key) { return true; }
` + section("static __always_inline struct iocost_status *iocost_status_value",
		"static __always_inline bool iocost_range_contains") +
		section("static __always_inline bool iocost_ioc_state_valid",
			"static __always_inline u64 iocost_ensure_ioc") +
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
    if (argc != 7)
        return 1;
    aggregate = strtoull(argv[1], NULL, 10);
    delete_ret = strtol(argv[3], NULL, 10);
    u64 wait_ns = strtoull(argv[2], NULL, 10);
    int scenario = strtol(argv[6], NULL, 10);
    owner = (struct iocost_owner_state){
        .ioc_ptr = 1, .ioc_id = 1, .css = 1, .css_serial = 1,
    };
    ioc = (struct iocost_ioc_state){ .ioc_id = 1, .device = 1 };
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
        ctx.ret = (u32)-1;
        kretprobe_iocg_wake_fn(&ctx);
        if (!pending_live || pending.start_ns != 1 || frame.bio_ptr)
            return 2;
        enter();
        ctx.ret = 0;
    } else if (scenario == 4) {
        /* The issuer reused the bio before the old successful return ran. */
        clock_ns = 1000001;
        admit();
    }
    ioc_lookups = 0;
    missing_aggregate = strtol(argv[4], NULL, 10);
    ioc_value_change = strtol(argv[5], NULL, 10);
    kretprobe_iocg_wake_fn(&ctx);
    u64 result[] = {
        aggregate, status.failure != 0, pending_live, frame.bio_ptr != 0,
        aggregate_lookups, aggregate_at_delete,
        (status.failure >> 32) == IOCOST_FAILURE_PENDING_DELETE,
        (status.failure >> 32) == IOCOST_FAILURE_IDENTITY,
        (u64)(s64)(s32)status.failure,
        pending_live ? pending.start_ns : 0,
    };
    return fwrite(result, sizeof(result), 1, stdout) != 1;
}
`
	compiler, err := exec.LookPath("clang")
	require.NoError(t, err, "the project BPF toolchain is required")
	executable := filepath.Join(t.TempDir(), "iocost-packed")
	compile := exec.CommandContext(t.Context(), compiler,
		"-x", "c", "-O2", "-Werror=incompatible-pointer-types", "-static", "-o", executable, "-")
	compile.Stdin = strings.NewReader(program)
	output, err := compile.CombinedOutput()
	require.NoError(t, err, "%s", output)

	for _, extension := range []string{"signed", "zero-extended"} {
		t.Run("map full/"+extension, func(t *testing.T) {
			raw := -int64(unix.E2BIG)
			if extension == "zero-extended" {
				raw = int64(uint32(raw))
			}
			output, err := exec.CommandContext(t.Context(), executable,
				strconv.FormatInt(raw, 10)).Output()
			require.NoError(t, err)
			status, err := decodeIOCostStatus(output)
			require.NoError(t, err)
			require.Equal(t, ioCostStatus{Reason: ioCostFailurePendingInsert, Errno: -int32(unix.E2BIG)}, status)
			require.ErrorIs(t, status.failure(), types.ErrTracingStopped)
		})
	}

	tests := []struct {
		name         string
		before       uint64
		waitNS       uint64
		deleteErr    int
		zeroExtended bool
		missing      int
		iocChange    int
		scenario     int
		want         ioCostCumulative
	}{
		{name: "sub unit wait", waitNS: 9999, want: ioCostCumulative{IOCount: 1}},
		{name: "truncate wait", waitNS: 19999, want: ioCostCumulative{IOCount: 1, Wait10US: 1}},
		{name: "missed return", waitNS: 20000, scenario: 1, want: ioCostCumulative{IOCount: 1, Wait10US: 2}},
		{name: "both wake probes missed", waitNS: 20000, scenario: 2, want: ioCostCumulative{IOCount: 1, Wait10US: 2}},
		{name: "budget retry", waitNS: 20000, scenario: 3, want: ioCostCumulative{IOCount: 1, Wait10US: 2}},
		{name: "bio reused before return", waitNS: 20000, scenario: 4, want: ioCostCumulative{IOCount: 1, Wait10US: 2}},
		{
			name: "count wraps", before: ((1<<26)-1)<<38 | 7, waitNS: 20000,
			want: ioCostCumulative{Wait10US: 9},
		},
		{
			name: "upper wait bit is independent of count", before: 5<<38 | ((1 << 37) - 1), waitNS: 20000,
			want: ioCostCumulative{IOCount: 6, Wait10US: (1 << 37) + 1},
		},
		{
			name: "wait wraps without count carry", before: 5<<38 | ((1 << 38) - 1), waitNS: 20000,
			want: ioCostCumulative{IOCount: 6, Wait10US: 1},
		},
		{
			name: "delete failure prevents publication", before: 5<<38 | 7, waitNS: 123456, deleteErr: -5,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
		},
		{
			name: "zero extended delete errno", before: 5<<38 | 7, waitNS: 123456,
			deleteErr: -int(unix.ENOENT), zeroExtended: true,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
		},
		{
			name: "missing aggregate while IOC live", before: 5<<38 | 7, waitNS: 123456, missing: 1,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
		},
		{
			name: "IOC exits before aggregate lookup", before: 5<<38 | 7, waitNS: 123456, missing: 2,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
		},
		{
			name: "live IOC identity mismatch", before: 5<<38 | 7, waitNS: 123456, iocChange: 1,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
		},
		{
			name: "live IOC identity invalid", before: 5<<38 | 7, waitNS: 123456, iocChange: 2,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
		},
		{
			name: "retired IOC value reused by another key", before: 5<<38 | 7, waitNS: 123456, iocChange: 3,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
		},
		{
			name: "retired IOC value being initialized", before: 5<<38 | 7, waitNS: 123456, iocChange: 4,
			want: ioCostCumulative{IOCount: 5, Wait10US: 7},
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
				strconv.FormatInt(deleteErr, 10), strconv.Itoa(test.missing), strconv.Itoa(test.iocChange),
				strconv.Itoa(test.scenario))
			output, err := command.Output()
			require.NoError(t, err)
			require.Len(t, output, 10*8)
			got, err := decodeIOCostWaitCounters(output[:8], 1)
			require.NoError(t, err)
			require.Equal(t, []ioCostCumulative{test.want}, got)
			var failed uint64
			if test.deleteErr != 0 {
				failed = 1
			}
			want := []uint64{failed, failed, 0, 1 - failed, test.before, failed, 0}
			if test.missing == 1 {
				want = []uint64{1, 0, 0, 1, test.before, 0, 0}
			}
			if test.iocChange == 1 || test.iocChange == 2 {
				want = []uint64{1, 0, 0, 0, test.before, 0, 1}
			} else if test.iocChange > 2 {
				want = []uint64{0, 0, 0, 0, test.before, 0, 0}
			}
			want = append(want, uint64(test.deleteErr))
			if test.missing == 1 {
				missingErrno := -int64(unix.ENOENT)
				want[len(want)-1] = uint64(missingErrno)
			}
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

// IOC lifetime and identity boundaries.

// These tests execute production IOC lifecycle helpers with deterministic map
// operations. Device changes keep the live IOC row, and competing creation is
// modeled at the helper boundary; verifier and probe behavior need kernel tests.

func TestIOCostLifecyclePreservesIdentityUntilActualExit(t *testing.T) {
	source := ioCostCSource(t)
	section := func(start, end string) string {
		return ioCostSourceSection(t, source, start, end)
	}
	exitStart := strings.Index(source, "SEC(\"kprobe/ioc_pd_free\")")
	require.GreaterOrEqual(t, exitStart, 0, "missing lifecycle hooks")
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
typedef uint64_t u64;
typedef int64_t s64;
typedef uint32_t u32;
#define SEC(name)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define compat_bpf_core_field_offset(field) ((u64)&(field))
#define IOCOST_U32_MAX UINT32_MAX
#define IOCOST_EEXIST 17
#define IOCOST_ENOENT 2
#define COMPAT_BPF_NOEXIST 1
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
struct pt_regs { u64 arg1; };
struct rq_qos { u64 unused; };
struct blkg_policy_data { u64 unused; };
struct ioc___iocost { u64 padding; struct rq_qos rqos; };
struct ioc_gq___iocost { u64 padding; struct blkg_policy_data pd; };
` + section("struct iocost_ioc_state {", "#define IOCOST_ASSERT_OFFSET") +
		section("struct iocost_live_identity {",
			"static __always_inline struct iocost_status *iocost_status_value") + `
static int iocost_ioc_state_map, iocost_owner_state_map;
static int iocost_stat_map, iocost_id_seq_map, iocost_wait_agg_map;
static struct iocost_ioc_state row, *current;
static struct iocost_owner_state owner;
static struct iocost_status status;
static u64 sequences[2];
static u32 cpu, owner_updates, aggregate_deletes;
static bool owner_live, competing_create;
static bool zero_extended_errors;
static long update_ret;
static __always_inline u64 iocost_ensure_ioc(u64, u32, u32);

static u32 bpf_get_smp_processor_id(void) { return cpu; }

static void *bpf_map_lookup_elem(void *map, const void *key)
{
    if (map == &iocost_ioc_state_map)
        return current;
    if (map == &iocost_owner_state_map)
        return owner_live ? &owner : NULL;
    if (map == &iocost_stat_map)
        return &status;
    if (map == &iocost_id_seq_map)
        return &sequences[cpu];
    assert(false);
    return NULL;
}

static long bpf_map_update_elem(void *map, const void *key,
                                const void *value, u64 flags)
{
    if (map == &iocost_owner_state_map) {
        assert(flags == COMPAT_BPF_NOEXIST && !owner_live);
        owner = *(const struct iocost_owner_state *)value;
        owner_live = true;
        owner_updates++;
        return 0;
    }
    assert(map == &iocost_ioc_state_map);
    const struct iocost_ioc_state *next = value;
    if (flags == COMPAT_BPF_NOEXIST && competing_create) {
        // Interleave the other CPU at the map helper, not at the result branch.
        competing_create = false;
        cpu = 1;
        assert(iocost_ensure_ioc(*(const u64 *)key,
                                next->device >> 32, (u32)next->device));
        cpu = 0;
        return zero_extended_errors ? (long)(u32)-IOCOST_EEXIST : -IOCOST_EEXIST;
    }
    assert(flags == COMPAT_BPF_NOEXIST && !current);
    if (update_ret)
        return update_ret;
    row = *next;
    current = &row;
    return 0;
}

static long bpf_map_delete_elem(void *map, const void *key)
{
    if (map == &iocost_ioc_state_map) {
        assert(current);
        current = NULL;
    } else if (map == &iocost_owner_state_map) {
        assert(owner_live);
        owner_live = false;
    } else {
        assert(map == &iocost_wait_agg_map && !owner_live);
        const struct iocost_wait_key *wait = key;
        assert(wait->ioc_id == owner.ioc_id && wait->css_serial == owner.css_serial);
        assert(wait->operation == aggregate_deletes++);
        // A read-only IOCG has no write aggregate when its owner retires.
        if (wait->operation == REQ_OP_WRITE)
            return zero_extended_errors ? (long)(u32)-IOCOST_ENOENT : -IOCOST_ENOENT;
    }
    return 0;
}
` + section("static __always_inline struct iocost_status *iocost_status_value",
		"static __always_inline void iocost_classify_pending_update_ret") +
		section("static __always_inline u64 iocost_allocate_ioc_id",
			"static __always_inline bool iocost_ensure_aggregate") +
		source[exitStart:] + `
int main(int argc, char **argv)
{
    if (argc > 1 && strcmp(argv[1], "sequence") == 0) {
        // Execute the actual allocator at its last usable sequence. Repeated
        // exhaustion must neither wrap nor consume another ID.
        sequences[cpu] = IOCOST_U32_MAX - 1;
        assert(iocost_allocate_ioc_id() == (1ULL << 32 | IOCOST_U32_MAX));
        assert(sequences[cpu] == IOCOST_U32_MAX);
        assert(!iocost_allocate_ioc_id() && !iocost_allocate_ioc_id());
        assert(sequences[cpu] == IOCOST_U32_MAX);
        assert((status.failure >> 32) == IOCOST_FAILURE_IDENTITY);
        return 0;
    }
    bool competing = argc > 1 && strcmp(argv[1], "competing-create") == 0;
    competing_create = competing;
    zero_extended_errors = argc > 2;
    struct iocost_live_identity identity = {
        .ioc_ptr = 0x100, .iocg_ptr = 0x200, .css = 0x300, .css_serial = 0x400,
    };
    u64 id = iocost_ensure_ioc(identity.ioc_ptr, 8, 16);
    assert(id == (competing ? (2ULL << 32 | 1) : (1ULL << 32 | 1)));
    assert(iocost_ensure_owner(&identity, id));
    struct iocost_ioc_state before = *current;
    assert(iocost_ensure_ioc(identity.ioc_ptr, 8, 16) == id);
    // Updating display metadata must not need another map allocation.
    update_ret = -5;
    assert(iocost_ensure_ioc(identity.ioc_ptr, 259, 32) == id);
    assert(current->device == (259ULL << 32 | 32) && current->ioc_id == id);
    assert(iocost_ensure_owner(&identity, id));
    assert(owner_updates == 1 && aggregate_deletes == 0 && !status.failure);
    // Check the snapshots before and after the store with the Go decoder.
    assert(fwrite(&before, sizeof(before), 1, stdout) == 1);
    assert(fwrite(current, sizeof(*current), 1, stdout) == 1);
    update_ret = 0;

    struct pt_regs ctx = { .arg1 = identity.ioc_ptr + 8 };
    kprobe_ioc_rqos_exit(&ctx);
    assert(!current && owner_live);
    ctx.arg1 = identity.iocg_ptr + 8;
    kprobe_ioc_pd_free(&ctx);
    assert(!owner_live && aggregate_deletes == 2);
    u64 next = iocost_ensure_ioc(identity.ioc_ptr, 259, 32);
    assert(next && next != id && !status.failure);

    ctx.arg1 = identity.ioc_ptr + 8;
    kprobe_ioc_rqos_exit(&ctx);
    update_ret = -5;
    assert(!iocost_ensure_ioc(identity.ioc_ptr, 8, 16));
    assert(!current);
    assert((status.failure >> 32) == IOCOST_FAILURE_IOC_INSERT);
    assert((int)(u32)status.failure == -5);
    return 0;
}
`
	compiler, err := exec.LookPath("clang")
	require.NoError(t, err, "the project BPF toolchain is required")
	executable := filepath.Join(t.TempDir(), "iocost-lifecycle")
	compile := exec.CommandContext(t.Context(), compiler,
		"-x", "c", "-O2", "-static", "-o", executable, "-")
	compile.Stdin = strings.NewReader(program)
	output, err := compile.CombinedOutput()
	require.NoError(t, err, "%s", output)
	for _, mode := range []string{"ordinary", "competing-create"} {
		for _, extension := range []string{"signed", "zero-extended"} {
			t.Run(mode+"/"+extension, func(t *testing.T) {
				args := []string{mode}
				if extension == "zero-extended" {
					args = append(args, extension)
				}
				output, err := exec.CommandContext(t.Context(), executable, args...).CombinedOutput()
				require.NoError(t, err, "%s", output)
				require.Len(t, output, 32)
				old, err := decodeIOCostIOCState(output[:16])
				require.NoError(t, err)
				current, err := decodeIOCostIOCState(output[16:])
				require.NoError(t, err)
				require.Equal(t, old.IOCID, current.IOCID)
				require.Equal(t, uint64(8)<<32|16, old.Device)
				require.Equal(t, uint64(259)<<32|32, current.Device)
			})
		}
	}
	output, err = exec.CommandContext(t.Context(), executable, "sequence", "exhaustion").CombinedOutput()
	require.NoError(t, err, "%s", output)
}

// Raw snapshots.

const (
	ioCostCaptureTestIOCPtr    = uint64(0x100)
	ioCostCaptureTestIOCID     = uint64(0x1_0000_0001)
	ioCostCaptureTestIOCGPtr   = uint64(0x200)
	ioCostCaptureTestCSS       = uint64(0x300)
	ioCostCaptureTestCSSSerial = uint64(0x400)
)

type ioCostCaptureTestLane struct {
	count    uint64
	wait10US uint64
}

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

type ioCostCaptureTestCall struct {
	stage string
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
	calls           []ioCostCaptureTestCall
}

func newFakeIOCostCaptureBPF(possibleCPUs int) *fakeIOCostCaptureBPF {
	object := &fakeIOCostCaptureBPF{
		mapIDs: map[string]uint32{
			ioCostWaitAggregateMap: 1,
			ioCostOwnerStateMap:    2,
			ioCostIOCStateMap:      3,
			ioCostStatusMap:        4,
		},
		mapNames: map[uint32]string{
			1: ioCostWaitAggregateMap,
			2: ioCostOwnerStateMap,
			3: ioCostIOCStateMap,
			4: ioCostStatusMap,
		},
		defaultItems:  make(map[string][]bpf.MapItem),
		dumpResponses: make(map[string][]ioCostCaptureTestDump),
		statusValue:   encodeIOCostTestStatus(&ioCostStatus{}),
	}
	lanes := make([]ioCostCaptureTestLane, possibleCPUs)
	for cpu := range lanes {
		lanes[cpu] = ioCostCaptureStableLane(
			uint64(cpu+1), uint64(cpu+1)*1_000,
		)
	}
	object.defaultItems[ioCostWaitAggregateMap] = []bpf.MapItem{
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSSSerial,
			0,
			0,
			lanes...,
		),
	}
	object.defaultItems[ioCostOwnerStateMap] = []bpf.MapItem{
		ioCostCaptureTestOwnerItem(
			ioCostCaptureTestIOCGPtr,
			ioCostCaptureTestIOCPtr,
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSS,
			ioCostCaptureTestCSSSerial,
		),
	}
	object.defaultItems[ioCostIOCStateMap] = []bpf.MapItem{
		ioCostCaptureTestIOCItem(
			ioCostCaptureTestIOCPtr,
			ioCostCaptureTestIOCID,
			8,
			16,
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
	object.calls = append(object.calls, ioCostCaptureTestCall{stage: name})
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

func (object *fakeIOCostCaptureBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	object.mu.Lock()
	name := object.mapNames[mapID]
	object.calls = append(object.calls, ioCostCaptureTestCall{stage: name})
	response := ioCostCaptureTestRead{
		value: append([]byte(nil), object.statusValue...),
	}
	if len(object.statusResponses) != 0 {
		response = object.statusResponses[0]
		object.statusResponses = object.statusResponses[1:]
	}
	object.mu.Unlock()

	if name != ioCostStatusMap {
		return nil, fmt.Errorf("unexpected IOCOST status map ID %d", mapID)
	}
	if len(key) != ioCostUint32Size || binary.LittleEndian.Uint32(key) != 0 {
		return nil, fmt.Errorf("unexpected IOCOST status key %v", key)
	}
	if response.release != nil {
		if response.entered != nil {
			response.entered <- ioCostStatusMap
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

func (object *fakeIOCostCaptureBPF) removeMap(name string) {
	object.mu.Lock()
	defer object.mu.Unlock()
	delete(object.mapIDs, name)
}

func (object *fakeIOCostCaptureBPF) snapshotCalls() []ioCostCaptureTestCall {
	object.mu.Lock()
	defer object.mu.Unlock()
	return append([]ioCostCaptureTestCall(nil), object.calls...)
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
		count:    count,
		wait10US: wait10US,
	}
}

func ioCostCaptureTestUint64(value uint64) []byte {
	data := make([]byte, ioCostUint64Size)
	binary.LittleEndian.PutUint64(data, value)
	return data
}

func ioCostCaptureTestWaitKeyBytes(
	iocID uint64,
	cssSerial uint64,
	operation uint32,
	reserved uint32,
) []byte {
	data := make([]byte, ioCostWaitKeySize)
	binary.LittleEndian.PutUint64(data[0:8], iocID)
	binary.LittleEndian.PutUint64(data[8:16], cssSerial)
	binary.LittleEndian.PutUint32(data[16:20], operation)
	binary.LittleEndian.PutUint32(data[20:24], reserved)
	return data
}

func ioCostCaptureTestWaitValue(lanes ...ioCostCaptureTestLane) []byte {
	data := make([]byte, len(lanes)*ioCostWaitCounterSize)
	for cpu, lane := range lanes {
		offset := cpu * ioCostWaitCounterSize
		packed := (lane.count << 38) | (lane.wait10US & ((1 << 38) - 1))
		binary.LittleEndian.PutUint64(data[offset:offset+8], packed)
	}
	return data
}

func ioCostCaptureTestAggregateItem(
	iocID uint64,
	cssSerial uint64,
	operation uint32,
	reserved uint32,
	lanes ...ioCostCaptureTestLane,
) bpf.MapItem {
	return bpf.MapItem{
		Key: ioCostCaptureTestWaitKeyBytes(
			iocID, cssSerial, operation, reserved,
		),
		Value: ioCostCaptureTestWaitValue(lanes...),
	}
}

func ioCostCaptureTestIOCItem(
	iocPtr uint64,
	iocID uint64,
	major uint32,
	firstMinor uint32,
) bpf.MapItem {
	value := make([]byte, ioCostIOCStateSize)
	binary.LittleEndian.PutUint64(value[0:8], iocID)
	binary.LittleEndian.PutUint64(value[8:16], uint64(major)<<32|uint64(firstMinor))
	return bpf.MapItem{Key: ioCostCaptureTestUint64(iocPtr), Value: value}
}

func ioCostCaptureTestOwnerItem(
	iocgPtr uint64,
	iocPtr uint64,
	iocID uint64,
	css uint64,
	cssSerial uint64,
) bpf.MapItem {
	value := make([]byte, ioCostOwnerStateSize)
	binary.LittleEndian.PutUint64(value[0:8], iocPtr)
	binary.LittleEndian.PutUint64(value[8:16], iocID)
	binary.LittleEndian.PutUint64(value[16:24], css)
	binary.LittleEndian.PutUint64(value[24:32], cssSerial)
	return bpf.MapItem{Key: ioCostCaptureTestUint64(iocgPtr), Value: value}
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
	breaker, cancel := context.WithCancelCause(t.Context())
	harness := &ioCostCaptureTestSession{cancel: func() { cancel(nil) }}
	harness.session = &ioCostSession{
		object:       object,
		possibleCPUs: possibleCPUs,
		breaker:      breaker,
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

func ioCostCaptureTestStages(calls []ioCostCaptureTestCall) []string {
	stages := make([]string, len(calls))
	for index, call := range calls {
		stages[index] = call.stage
	}
	return stages
}

func TestIOCostSnapshotReadsAggregateAndLiveIndexes(t *testing.T) {
	object := newFakeIOCostCaptureBPF(2)
	harness := newIOCostCaptureTestSession(t, object, 2)

	snapshot, err := harness.session.captureRawSnapshot()
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Zero(t, harness.cancelCount())

	calls := object.snapshotCalls()
	require.Equal(t, []string{
		ioCostWaitAggregateMap,
		ioCostOwnerStateMap,
		ioCostIOCStateMap,
		ioCostStatusMap,
	}, ioCostCaptureTestStages(calls))
	require.Equal(t, "8:16", snapshot.LiveIOCs[ioCostCaptureTestIOCID])
	require.Equal(t, ioCostCaptureTestCSS, snapshot.LiveOwners[ioCostOwnerIdentity{
		IOCID: ioCostCaptureTestIOCID, CSSSerial: ioCostCaptureTestCSSSerial,
	}])
	require.Len(t, snapshot.Samples, 1)
}

func countIOCostCaptureTestStage(
	calls []ioCostCaptureTestCall,
	stage string,
) int {
	count := 0
	for _, call := range calls {
		if call.stage == stage {
			count++
		}
	}
	return count
}

func TestIOCostSnapshotRetriesWholeCaptureAfterUnstableHashDump(t *testing.T) {
	stages := []string{
		ioCostWaitAggregateMap,
		ioCostOwnerStateMap,
		ioCostIOCStateMap,
	}
	for index, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			object := newFakeIOCostCaptureBPF(1)
			items := object.defaultItems[stage]
			object.queueDumps(stage, ioCostCaptureTestDump{
				items: append(cloneIOCostCaptureTestItems(items), items[0]),
			})
			harness := newIOCostCaptureTestSession(t, object, 1)

			snapshot, err := harness.session.captureRawSnapshot()
			require.NoError(t, err)
			require.NotNil(t, snapshot)
			wantStages := append([]string(nil), stages[:index+1]...)
			wantStages = append(wantStages, ioCostCaptureAllStages()...)
			require.Equal(t, wantStages,
				ioCostCaptureTestStages(object.snapshotCalls()))
			require.Zero(t, harness.cancelCount())
		})
	}
}

func TestIOCostSnapshotStopsAfterThreeUnstableHashDumpsWithoutRestart(t *testing.T) {
	stages := []string{
		ioCostWaitAggregateMap,
		ioCostOwnerStateMap,
		ioCostIOCStateMap,
	}
	for index, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			object := newFakeIOCostCaptureBPF(1)
			items := object.defaultItems[stage]
			for range ioCostSnapshotAttempts {
				object.queueDumps(stage, ioCostCaptureTestDump{
					items: append(cloneIOCostCaptureTestItems(items), items[0]),
				})
			}
			harness := newIOCostCaptureTestSession(t, object, 1)

			snapshot, err := harness.session.captureRawSnapshot()
			require.Nil(t, snapshot)
			require.ErrorIs(t, err, errIOCostSnapshotUnstable)
			wantStages := make([]string, 0,
				ioCostSnapshotAttempts*(index+1))
			for range ioCostSnapshotAttempts {
				wantStages = append(wantStages, stages[:index+1]...)
			}
			require.Equal(t, wantStages,
				ioCostCaptureTestStages(object.snapshotCalls()))
			require.Zero(t, harness.cancelCount())
		})
	}
}

func TestIOCostSnapshotRetriesDuplicateLiveIdentities(t *testing.T) {
	for _, stage := range []string{ioCostIOCStateMap, ioCostOwnerStateMap} {
		for _, unstableDumps := range []int{1, ioCostSnapshotAttempts} {
			t.Run(fmt.Sprintf("%s/%d unstable dumps", stage, unstableDumps), func(t *testing.T) {
				object := newFakeIOCostCaptureBPF(1)
				items := cloneIOCostCaptureTestItems(object.defaultItems[stage])
				// A lookup can copy a recycled HASH value before iteration
				// finds its new key: different keys then carry one identity.
				reused := items[0]
				reused.Key = ioCostCaptureTestUint64(binary.LittleEndian.Uint64(reused.Key) + 1)
				items = append(items, reused)
				for range unstableDumps {
					object.queueDumps(stage, ioCostCaptureTestDump{items: items})
				}
				harness := newIOCostCaptureTestSession(t, object, 1)

				snapshot, err := harness.session.captureRawSnapshot()
				attempts := unstableDumps
				if unstableDumps == ioCostSnapshotAttempts {
					require.Nil(t, snapshot)
					require.ErrorIs(t, err, errIOCostSnapshotUnstable)
					require.NotErrorIs(t, err, errIOCostSessionInvalid)
				} else {
					require.NoError(t, err)
					require.Len(t, snapshot.Samples, 1)
					attempts++
				}
				var wantStages []string
				for range attempts {
					wantStages = append(wantStages, ioCostCaptureAllStages()...)
				}
				require.Equal(t, wantStages,
					ioCostCaptureTestStages(object.snapshotCalls()))
				require.Zero(t, harness.cancelCount())
			})
		}
	}
}

func TestIOCostSnapshotDropsOwnerAfterParentPointerReplacement(t *testing.T) {
	object := newFakeIOCostCaptureBPF(1)
	object.queueDumps(ioCostIOCStateMap, ioCostCaptureTestDump{
		items: []bpf.MapItem{ioCostCaptureTestIOCItem(
			ioCostCaptureTestIOCPtr,
			ioCostCaptureTestIOCID+1,
			8,
			16,
		)},
	})
	harness := newIOCostCaptureTestSession(t, object, 1)

	snapshot, err := harness.session.captureRawSnapshot()
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Empty(t, snapshot.Samples)
	require.Empty(t, snapshot.LiveOwners)
	require.Equal(t, map[uint64]string{
		ioCostCaptureTestIOCID + 1: "8:16",
	}, snapshot.LiveIOCs)
	require.Equal(t, ioCostCaptureAllStages(),
		ioCostCaptureTestStages(object.snapshotCalls()))
	require.Zero(t, harness.cancelCount())
}

func TestIOCostDecodeAggregateErrorDoesNotRetry(t *testing.T) {
	object := newFakeIOCostCaptureBPF(1)
	invalid := ioCostCaptureTestAggregateItem(
		ioCostCaptureTestIOCID,
		ioCostCaptureTestCSSSerial,
		0,
		0,
		ioCostCaptureStableLane(1, 2),
	)
	invalid.Key = invalid.Key[:len(invalid.Key)-1]
	object.queueDumps(ioCostWaitAggregateMap,
		ioCostCaptureTestDump{items: []bpf.MapItem{invalid}},
		ioCostCaptureTestDump{items: object.defaultItems[ioCostWaitAggregateMap]},
	)
	harness := newIOCostCaptureTestSession(t, object, 1)

	snapshot, err := harness.session.captureRawSnapshot()
	require.Nil(t, snapshot)
	require.ErrorIs(t, err, errIOCostSessionInvalid)
	require.Equal(t, []string{ioCostWaitAggregateMap},
		ioCostCaptureTestStages(object.snapshotCalls()))
	require.Equal(t, 1, harness.cancelCount())
}

func TestIOCostDecodeCaptureRejectsNonExactABI(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeIOCostCaptureBPF)
		stages    []string
	}{
		{
			name: "short aggregate key",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostWaitAggregateMap],
				)[0]
				item.Key = item.Key[:len(item.Key)-1]
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "long aggregate key",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostWaitAggregateMap],
				)[0]
				item.Key = append(item.Key, 0)
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "nonzero aggregate reserved",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostWaitAggregateMap],
				)[0]
				binary.LittleEndian.PutUint32(item.Key[20:24], 1)
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "unknown aggregate operation",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostWaitAggregateMap],
				)[0]
				binary.LittleEndian.PutUint32(item.Key[16:20], 2)
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "short per-CPU aggregate lanes",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostWaitAggregateMap],
				)[0]
				item.Value = item.Value[:len(item.Value)-1]
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "long per-CPU aggregate lanes",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostWaitAggregateMap],
				)[0]
				item.Value = append(item.Value, 0)
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "short owner key",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostOwnerStateMap],
				)[0]
				item.Key = item.Key[:len(item.Key)-1]
				object.setDefaultItems(ioCostOwnerStateMap, []bpf.MapItem{item})
			},
			stages: []string{
				ioCostWaitAggregateMap, ioCostOwnerStateMap,
				ioCostIOCStateMap, ioCostStatusMap,
			},
		},
		{
			name: "long owner value",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostOwnerStateMap],
				)[0]
				item.Value = append(item.Value, 0)
				object.setDefaultItems(ioCostOwnerStateMap, []bpf.MapItem{item})
			},
			stages: []string{
				ioCostWaitAggregateMap, ioCostOwnerStateMap,
				ioCostIOCStateMap, ioCostStatusMap,
			},
		},
		{
			name: "long IOC key",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostIOCStateMap],
				)[0]
				item.Key = append(item.Key, 0)
				object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{item})
			},
			stages: []string{
				ioCostWaitAggregateMap, ioCostOwnerStateMap,
				ioCostIOCStateMap, ioCostStatusMap,
			},
		},
		{
			name: "short IOC value",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := cloneIOCostCaptureTestItems(
					object.defaultItems[ioCostIOCStateMap],
				)[0]
				item.Value = item.Value[:len(item.Value)-1]
				object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{item})
			},
			stages: []string{
				ioCostWaitAggregateMap, ioCostOwnerStateMap,
				ioCostIOCStateMap, ioCostStatusMap,
			},
		},
		{
			name: "short status",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueStatus(ioCostCaptureTestRead{
					value: make([]byte, ioCostStatusSize-1),
				})
			},
			stages: []string{
				ioCostWaitAggregateMap, ioCostOwnerStateMap,
				ioCostIOCStateMap, ioCostStatusMap,
			},
		},
		{
			name: "long status",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueStatus(ioCostCaptureTestRead{
					value: make([]byte, ioCostStatusSize+1),
				})
			},
			stages: []string{
				ioCostWaitAggregateMap, ioCostOwnerStateMap,
				ioCostIOCStateMap, ioCostStatusMap,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeIOCostCaptureBPF(2)
			test.configure(object)
			harness := newIOCostCaptureTestSession(t, object, 2)

			snapshot, err := harness.session.captureRawSnapshot()
			require.Nil(t, snapshot)
			require.ErrorIs(t, err, errIOCostSessionInvalid)
			require.Equal(t, test.stages,
				ioCostCaptureTestStages(object.snapshotCalls()))
			require.Equal(t, 1, countIOCostCaptureTestStage(
				object.snapshotCalls(), ioCostWaitAggregateMap,
			))
			require.Equal(t, 1, harness.cancelCount())
		})
	}
}

func TestIOCostDecodePreservesOperationsAndPerCPULanes(t *testing.T) {
	const writeSerial = uint64(0x401)
	object := newFakeIOCostCaptureBPF(2)
	object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSSSerial,
			0,
			0,
			ioCostCaptureTestLane{
				count: 0x12, wait10US: 0x13,
			},
			ioCostCaptureTestLane{
				count: 0x22, wait10US: 0x23,
			},
		),
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestIOCID,
			writeSerial,
			1,
			0,
			ioCostCaptureStableLane(0x31, 0x32),
			ioCostCaptureStableLane(0x41, 0x42),
		),
	})
	object.setDefaultItems(ioCostOwnerStateMap, []bpf.MapItem{
		ioCostCaptureTestOwnerItem(
			ioCostCaptureTestIOCGPtr,
			ioCostCaptureTestIOCPtr,
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSS,
			ioCostCaptureTestCSSSerial,
		),
		ioCostCaptureTestOwnerItem(
			ioCostCaptureTestIOCGPtr+1,
			ioCostCaptureTestIOCPtr,
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSS+1,
			writeSerial,
		),
	})
	harness := newIOCostCaptureTestSession(t, object, 2)

	snapshot, err := harness.session.captureRawSnapshot()
	require.NoError(t, err)
	require.Equal(t, ioCostRawSample{
		CSS:       ioCostCaptureTestCSS,
		Device:    "8:16",
		Operation: "read",
		Counters: []ioCostCumulative{
			{IOCount: 0x12, Wait10US: 0x13},
			{IOCount: 0x22, Wait10US: 0x23},
		},
	}, snapshot.Samples[ioCostWaitKey{
		IOCID: ioCostCaptureTestIOCID, CSSSerial: ioCostCaptureTestCSSSerial,
		Operation: 0,
	}])
	require.Equal(t, ioCostRawSample{
		CSS:       ioCostCaptureTestCSS + 1,
		Device:    "8:16",
		Operation: "write",
		Counters: []ioCostCumulative{
			{IOCount: 0x31, Wait10US: 0x32},
			{IOCount: 0x41, Wait10US: 0x42},
		},
	}, snapshot.Samples[ioCostWaitKey{
		IOCID: ioCostCaptureTestIOCID, CSSSerial: writeSerial, Operation: 1,
	}])
	require.Zero(t, harness.cancelCount())
}

func TestIOCostDecodeRejectsInvalidLiveIdentities(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeIOCostCaptureBPF)
		stages    []string
	}{
		{
			name: "zero aggregate IOC ID",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{
					ioCostCaptureTestAggregateItem(
						0, ioCostCaptureTestCSSSerial, 0, 0,
						ioCostCaptureStableLane(1, 2),
					),
				})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "zero aggregate CSS serial",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{
					ioCostCaptureTestAggregateItem(
						ioCostCaptureTestIOCID, 0, 0, 0,
						ioCostCaptureStableLane(1, 2),
					),
				})
			},
			stages: []string{ioCostWaitAggregateMap},
		},
		{
			name: "zero IOC pointer",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{
					ioCostCaptureTestIOCItem(0, ioCostCaptureTestIOCID, 8, 16),
				})
			},
			stages: ioCostCaptureAllStages(),
		},
		{
			name: "zero IOC ID",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{
					ioCostCaptureTestIOCItem(ioCostCaptureTestIOCPtr, 0, 8, 16),
				})
			},
			stages: ioCostCaptureAllStages(),
		},
		{
			name: "zero owner identity",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostOwnerStateMap, []bpf.MapItem{
					ioCostCaptureTestOwnerItem(
						ioCostCaptureTestIOCGPtr,
						ioCostCaptureTestIOCPtr,
						ioCostCaptureTestIOCID,
						0,
						ioCostCaptureTestCSSSerial,
					),
				})
			},
			stages: ioCostCaptureAllStages(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeIOCostCaptureBPF(1)
			test.configure(object)
			harness := newIOCostCaptureTestSession(t, object, 1)

			snapshot, err := harness.session.captureRawSnapshot()
			require.Nil(t, snapshot)
			require.ErrorIs(t, err, errIOCostSessionInvalid)
			require.Equal(t, test.stages,
				ioCostCaptureTestStages(object.snapshotCalls()))
			require.Equal(t, 1, harness.cancelCount())
		})
	}
}

func ioCostCaptureAllStages() []string {
	return []string{
		ioCostWaitAggregateMap,
		ioCostOwnerStateMap,
		ioCostIOCStateMap,
		ioCostStatusMap,
	}
}

func TestIOCostJoinBuildsCompleteLiveSnapshot(t *testing.T) {
	const (
		secondIOCPtr    = uint64(0x101)
		secondIOCID     = uint64(0x1_0000_0002)
		secondIOCGPtr   = uint64(0x201)
		secondCSS       = uint64(0x301)
		secondCSSSerial = uint64(0x401)
		idleIOCPtr      = uint64(0x102)
		idleIOCID       = uint64(0x1_0000_0003)
		idleIOCGPtr     = uint64(0x202)
		idleCSS         = uint64(0x302)
		idleCSSSerial   = uint64(0x402)
	)
	object := newFakeIOCostCaptureBPF(1)
	object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSSSerial,
			0,
			0,
			ioCostCaptureStableLane(3, 30),
		),
		ioCostCaptureTestAggregateItem(
			secondIOCID,
			secondCSSSerial,
			1,
			0,
			ioCostCaptureStableLane(4, 40),
		),
	})
	object.setDefaultItems(ioCostOwnerStateMap, []bpf.MapItem{
		ioCostCaptureTestOwnerItem(
			ioCostCaptureTestIOCGPtr,
			ioCostCaptureTestIOCPtr,
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSS,
			ioCostCaptureTestCSSSerial,
		),
		ioCostCaptureTestOwnerItem(
			secondIOCGPtr, secondIOCPtr, secondIOCID,
			secondCSS, secondCSSSerial,
		),
		ioCostCaptureTestOwnerItem(
			idleIOCGPtr, idleIOCPtr, idleIOCID, idleCSS, idleCSSSerial,
		),
	})
	object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{
		ioCostCaptureTestIOCItem(
			ioCostCaptureTestIOCPtr, ioCostCaptureTestIOCID, 8, 16,
		),
		ioCostCaptureTestIOCItem(secondIOCPtr, secondIOCID, 259, 0),
		ioCostCaptureTestIOCItem(idleIOCPtr, idleIOCID, 7, 7),
	})
	harness := newIOCostCaptureTestSession(t, object, 1)

	snapshot, err := harness.session.captureRawSnapshot()
	require.NoError(t, err)
	require.Equal(t, map[uint64]string{
		ioCostCaptureTestIOCID: "8:16",
		secondIOCID:            "259:0",
		idleIOCID:              "7:7",
	}, snapshot.LiveIOCs)
	require.Equal(t, map[ioCostOwnerIdentity]uint64{
		{IOCID: ioCostCaptureTestIOCID, CSSSerial: ioCostCaptureTestCSSSerial}: ioCostCaptureTestCSS,
		{IOCID: secondIOCID, CSSSerial: secondCSSSerial}:                       secondCSS,
		{IOCID: idleIOCID, CSSSerial: idleCSSSerial}:                           idleCSS,
	}, snapshot.LiveOwners)
	require.Equal(t, map[ioCostWaitKey]ioCostRawSample{
		{
			IOCID: ioCostCaptureTestIOCID, CSSSerial: ioCostCaptureTestCSSSerial,
			Operation: 0,
		}: {
			CSS:       ioCostCaptureTestCSS,
			Device:    "8:16",
			Operation: "read",
			Counters:  []ioCostCumulative{{IOCount: 3, Wait10US: 30}},
		},
		{
			IOCID: secondIOCID, CSSSerial: secondCSSSerial, Operation: 1,
		}: {
			CSS:       secondCSS,
			Device:    "259:0",
			Operation: "write",
			Counters:  []ioCostCumulative{{IOCount: 4, Wait10US: 40}},
		},
	}, snapshot.Samples)
	require.Zero(t, harness.cancelCount())
}

func TestIOCostJoinAcceptsHealthyEmptyMaps(t *testing.T) {
	object := newFakeIOCostCaptureBPF(1)
	object.setDefaultItems(ioCostWaitAggregateMap, nil)
	object.setDefaultItems(ioCostOwnerStateMap, nil)
	object.setDefaultItems(ioCostIOCStateMap, nil)
	harness := newIOCostCaptureTestSession(t, object, 1)

	snapshot, err := harness.session.captureRawSnapshot()
	require.NoError(t, err)
	require.Empty(t, snapshot.Samples)
	require.Empty(t, snapshot.LiveIOCs)
	require.Empty(t, snapshot.LiveOwners)
	require.Equal(t, ioCostCaptureAllStages(),
		ioCostCaptureTestStages(object.snapshotCalls()))
	require.Zero(t, harness.cancelCount())
}

func TestIOCostJoinDropsDeletionRaceOrphans(t *testing.T) {
	const (
		ownerMissingID      = uint64(0x1_0000_0010)
		ownerMissingSerial  = uint64(0x410)
		parentMissingID     = uint64(0x1_0000_0011)
		parentMissingSerial = uint64(0x411)
		allMissingID        = uint64(0x1_0000_0012)
		allMissingSerial    = uint64(0x412)
	)
	object := newFakeIOCostCaptureBPF(1)
	object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSSSerial,
			0,
			0,
			ioCostCaptureStableLane(1, 10),
		),
		ioCostCaptureTestAggregateItem(
			ownerMissingID, ownerMissingSerial, 0, 0,
			ioCostCaptureStableLane(2, 20),
		),
		ioCostCaptureTestAggregateItem(
			parentMissingID, parentMissingSerial, 0, 0,
			ioCostCaptureStableLane(3, 30),
		),
		ioCostCaptureTestAggregateItem(
			allMissingID, allMissingSerial, 0, 0,
			ioCostCaptureStableLane(4, 40),
		),
	})
	object.setDefaultItems(ioCostOwnerStateMap, []bpf.MapItem{
		ioCostCaptureTestOwnerItem(
			ioCostCaptureTestIOCGPtr,
			ioCostCaptureTestIOCPtr,
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSS,
			ioCostCaptureTestCSSSerial,
		),
		ioCostCaptureTestOwnerItem(
			0x210, 0x110, parentMissingID, 0x310, parentMissingSerial,
		),
	})
	object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{
		ioCostCaptureTestIOCItem(
			ioCostCaptureTestIOCPtr, ioCostCaptureTestIOCID, 8, 16,
		),
		ioCostCaptureTestIOCItem(0x111, ownerMissingID, 8, 17),
	})
	harness := newIOCostCaptureTestSession(t, object, 1)

	snapshot, err := harness.session.captureRawSnapshot()
	require.NoError(t, err)
	require.Len(t, snapshot.Samples, 1)
	require.Contains(t, snapshot.Samples, ioCostWaitKey{
		IOCID: ioCostCaptureTestIOCID, CSSSerial: ioCostCaptureTestCSSSerial,
		Operation: 0,
	})
	require.Equal(t, map[uint64]string{
		ioCostCaptureTestIOCID: "8:16",
		ownerMissingID:         "8:17",
	}, snapshot.LiveIOCs)
	require.Equal(t, map[ioCostOwnerIdentity]uint64{
		{IOCID: ioCostCaptureTestIOCID, CSSSerial: ioCostCaptureTestCSSSerial}: ioCostCaptureTestCSS,
	}, snapshot.LiveOwners)
	require.Zero(t, harness.cancelCount())
}

func TestIOCostSnapshotStructuralFailuresStopSession(t *testing.T) {
	t.Run("missing required maps", func(t *testing.T) {
		stages := ioCostCaptureAllStages()
		for index, missing := range stages {
			index := index
			missing := missing
			t.Run(missing, func(t *testing.T) {
				object := newFakeIOCostCaptureBPF(1)
				object.removeMap(missing)
				harness := newIOCostCaptureTestSession(t, object, 1)

				snapshot, err := harness.session.captureRawSnapshot()
				require.Nil(t, snapshot)
				require.ErrorIs(t, err, errIOCostSessionInvalid)
				require.Equal(t, stages[:index],
					ioCostCaptureTestStages(object.snapshotCalls()))
				require.Equal(t, 1, harness.cancelCount())
			})
		}
	})

	t.Run("sticky unhealthy status", func(t *testing.T) {
		object := newFakeIOCostCaptureBPF(1)
		object.statusValue = encodeIOCostTestStatus(&ioCostStatus{Reason: ioCostFailurePendingDelete})
		harness := newIOCostCaptureTestSession(t, object, 1)

		snapshot, err := harness.session.captureRawSnapshot()
		require.Nil(t, snapshot)
		require.ErrorIs(t, err, errIOCostSessionUnhealthy)
		require.Equal(t, ioCostCaptureAllStages(),
			ioCostCaptureTestStages(object.snapshotCalls()))
		require.Equal(t, 1, harness.cancelCount())
	})
}

func TestIOCostSnapshotTransientFailuresDoNotStopSession(t *testing.T) {
	backendErr := errors.New("temporary backend I/O failure")
	tests := []struct {
		name      string
		configure func(*fakeIOCostCaptureBPF)
		cancel    bool
		wantErr   error
	}{
		{
			name: "aggregate I/O",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueDumps(ioCostWaitAggregateMap,
					ioCostCaptureTestDump{err: backendErr})
			},
			wantErr: backendErr,
		},
		{
			name: "owner I/O",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueDumps(ioCostOwnerStateMap,
					ioCostCaptureTestDump{err: backendErr})
			},
			wantErr: backendErr,
		},
		{
			name: "IOC I/O",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueDumps(ioCostIOCStateMap,
					ioCostCaptureTestDump{err: backendErr})
			},
			wantErr: backendErr,
		},
		{
			name: "status I/O",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueStatus(ioCostCaptureTestRead{err: backendErr})
			},
			wantErr: backendErr,
		},
		{
			name:      "canceled breaker",
			configure: func(*fakeIOCostCaptureBPF) {},
			cancel:    true,
			wantErr:   context.Canceled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeIOCostCaptureBPF(1)
			test.configure(object)
			harness := newIOCostCaptureTestSession(t, object, 1)
			if test.cancel {
				harness.cancel()
			}

			snapshot, err := harness.session.captureRawSnapshot()
			require.Nil(t, snapshot)
			require.ErrorIs(t, err, test.wantErr)
			require.Zero(t, harness.cancelCount())
			if test.cancel {
				require.Empty(t, object.snapshotCalls())
			}
		})
	}
}

func TestIOCostCancelAtEachCaptureStageStopsLaterReads(t *testing.T) {
	stages := ioCostCaptureAllStages()
	for index, stage := range stages {
		index := index
		stage := stage
		t.Run(stage, func(t *testing.T) {
			object := newFakeIOCostCaptureBPF(1)
			entered := make(chan string, 1)
			release := make(chan struct{})
			if stage == ioCostStatusMap {
				object.queueStatus(ioCostCaptureTestRead{
					value:   object.statusValue,
					release: release,
					entered: entered,
				})
			} else {
				object.queueDumps(stage, ioCostCaptureTestDump{
					items:   object.defaultItems[stage],
					release: release,
					entered: entered,
				})
			}
			harness := newIOCostCaptureTestSession(t, object, 1)
			type captureResult struct {
				snapshot *ioCostRawSnapshot
				err      error
			}
			result := make(chan captureResult, 1)
			go func() {
				snapshot, err := harness.session.captureRawSnapshot()
				result <- captureResult{snapshot: snapshot, err: err}
			}()

			select {
			case got := <-entered:
				require.Equal(t, stage, got)
			case <-time.After(time.Second):
				t.Fatalf("capture did not block at %s", stage)
			}
			harness.cancel()
			close(release)

			select {
			case got := <-result:
				require.Nil(t, got.snapshot)
				require.ErrorIs(t, got.err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatalf("capture did not stop after canceling %s", stage)
			}
			require.Equal(t, stages[:index+1],
				ioCostCaptureTestStages(object.snapshotCalls()))
			require.Zero(t, harness.cancelCount())
		})
	}
}

// Hook compatibility.

// Hook resolution and structural profiles.

// This file tests IOCOST hook resolution and supplies kernel structure
// fixtures for relocating the production BPF object.

func TestLoadIOCostKernelProfileUsesOneSymbolSnapshot(t *testing.T) {
	calls := 0
	got, err := loadIOCostKernelProfileWith(func(addresses, ranges []string) (symbol.KsymbolProfile, error) {
		calls++
		require.Equal(t, []string{
			ioCostKickSymbol, ioCostWakeSymbol, ioCostExitSymbol, ioCostPDFreeSymbol,
		}, addresses)
		require.Equal(t, []string{ioCostThrottleCaller, ioCostOverBudgetCaller}, ranges)
		return syntheticIOCostKallsymsProfile(), nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, &ioCostKernelProfile{
		wakeAddress:     0x2000,
		throttleRange:   symbol.KsymbolRange{Start: 0x5000, End: 0x5100},
		overBudgetRange: symbol.KsymbolRange{Start: 0x6000, End: 0x6080},
	}, got)

	failure := errors.New("kallsyms unavailable")
	got, err = loadIOCostKernelProfileWith(func([]string, []string) (symbol.KsymbolProfile, error) {
		return symbol.KsymbolProfile{}, failure
	})
	require.ErrorIs(t, err, failure)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Nil(t, got)

	got, err = loadIOCostKernelProfileWith(func([]string, []string) (symbol.KsymbolProfile, error) {
		profile := syntheticIOCostKallsymsProfile()
		delete(profile.Ranges, ioCostThrottleCaller)
		return profile, nil
	})
	require.ErrorIs(t, err, types.ErrTracingStopped)
	require.ErrorContains(t, err, ioCostThrottleCaller)
	require.Nil(t, got)
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
				delete(profile.Ranges, ioCostOverBudgetCaller)
			} else {
				profile.Ranges[ioCostOverBudgetCaller] = *test.optional
			}

			got, err := resolveIOCostKallsyms(profile)
			require.NoError(t, err)
			require.Equal(t, &ioCostKernelProfile{
				wakeAddress:     0x2000,
				throttleRange:   symbol.KsymbolRange{Start: 0x5000, End: 0x5100},
				overBudgetRange: test.wantBudget,
			}, got)
		})
	}
}

func TestResolveIOCostKallsymsRejectsMissingRequiredSymbols(t *testing.T) {
	for _, name := range []string{
		ioCostKickSymbol,
		ioCostWakeSymbol,
		ioCostExitSymbol,
		ioCostPDFreeSymbol,
	} {
		t.Run(name, func(t *testing.T) {
			profile := syntheticIOCostKallsymsProfile()
			delete(profile.Addresses, name)

			got, err := resolveIOCostKallsyms(profile)
			require.ErrorContains(t, err, name)
			require.Nil(t, got)
		})
	}
}

func TestResolveIOCostKallsymsRequiresEnqueueCaller(t *testing.T) {
	profile := syntheticIOCostKallsymsProfile()
	delete(profile.Ranges, ioCostThrottleCaller)
	got, err := resolveIOCostKallsyms(profile)
	require.ErrorContains(t, err, ioCostThrottleCaller)
	require.Nil(t, got)
}

func syntheticIOCostKallsymsProfile() symbol.KsymbolProfile {
	return symbol.KsymbolProfile{
		Addresses: map[string]uint64{
			ioCostKickSymbol:   0x1000,
			ioCostWakeSymbol:   0x2000,
			ioCostExitSymbol:   0x3000,
			ioCostPDFreeSymbol: 0x4000,
		},
		Ranges: map[string]symbol.KsymbolRange{
			ioCostThrottleCaller:   {Start: 0x5000, End: 0x5100},
			ioCostOverBudgetCaller: {Start: 0x6000, End: 0x6080},
		},
	}
}

type syntheticIOCostDeviceProfile uint8

const (
	syntheticIOCost510Profile syntheticIOCostDeviceProfile = iota
	syntheticIOCostHybridProfile
	syntheticIOCostMainlineProfile
	syntheticIOCostBothProfiles
	syntheticIOCostDiskBioDiskQOSProfile
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
	ioc := &btf.Struct{Name: "ioc", Size: 48}
	rqQOS := &btf.Struct{Name: "rq_qos", Size: 32}
	disk := &btf.Struct{Name: "gendisk", Size: 32}
	queue := &btf.Struct{Name: "request_queue", Size: 8}
	blockDevice := &btf.Struct{Name: "block_device", Size: 8}
	policyData := &btf.Struct{Name: "blkg_policy_data", Size: 32}
	blkg := &btf.Struct{Name: "blkcg_gq", Size: 8}
	blkcg := &btf.Struct{Name: "blkcg", Size: 16}
	css := &btf.Struct{Name: "cgroup_subsys_state", Size: 16}

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
		{Name: "ioc", Type: &btf.Pointer{Target: ioc}, Offset: 512},
	}
	ioc.Members = []btf.Member{
		{Name: "rqos", Type: rqQOS, Offset: 64},
	}
	bio.Members = []btf.Member{
		{Name: "bi_blkg", Type: &btf.Pointer{Target: blkg}, Offset: 0},
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
	}

	if device == syntheticIOCost510Profile || device == syntheticIOCostBothProfiles ||
		device == syntheticIOCostDiskBioDiskQOSProfile {
		bio.Members = append(bio.Members, btf.Member{
			Name: "bi_disk", Type: &btf.Pointer{Target: disk}, Offset: 128,
		})
	}
	if device == syntheticIOCost510Profile ||
		device == syntheticIOCostHybridProfile ||
		device == syntheticIOCostMainlineProfile ||
		device == syntheticIOCostBothProfiles {
		disk.Members = append(disk.Members, btf.Member{
			Name: "queue", Type: &btf.Pointer{Target: queue}, Offset: 64,
		})
	}
	if device == syntheticIOCost510Profile ||
		device == syntheticIOCostHybridProfile ||
		device == syntheticIOCostBothProfiles {
		rqQOS.Members = append(rqQOS.Members, btf.Member{
			Name: "q", Type: &btf.Pointer{Target: queue}, Offset: 0,
		})
	}
	if device == syntheticIOCostHybridProfile ||
		device == syntheticIOCostMainlineProfile ||
		device == syntheticIOCostBothProfiles {
		bio.Members = append(bio.Members, btf.Member{
			Name: "bi_bdev", Type: &btf.Pointer{Target: blockDevice}, Offset: 192,
		})
		blockDevice.Members = append(blockDevice.Members, btf.Member{
			Name: "bd_disk", Type: &btf.Pointer{Target: disk}, Offset: 0,
		})
	}
	if device == syntheticIOCostMainlineProfile || device == syntheticIOCostBothProfiles ||
		device == syntheticIOCostDiskBioDiskQOSProfile {
		rqQOS.Members = append(rqQOS.Members, btf.Member{
			Name: "disk", Type: &btf.Pointer{Target: disk}, Offset: 64,
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
		"ioc":                 ioc,
		"rq_qos":              rqQOS,
		"gendisk":             disk,
		"request_queue":       queue,
		"block_device":        blockDevice,
		"blkg_policy_data":    policyData,
		"blkcg_gq":            blkg,
		"blkcg":               blkcg,
		"cgroup_subsys_state": css,
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
			ioc,
			rqQOS,
			disk,
			queue,
			blockDevice,
			policyData,
			blkg,
			blkcg,
			css,
		},
		structures: structures,
	}
}

// Diagnostic map ABI and wire decoding.

// This file mirrors internal IOCOST map ABI used by object-contract and
// diagnostic tests. The daemon reads the business and health maps defined in
// iocost_tracing.go.

const (
	ioCostIDSequenceMap = "iocost_id_seq_map"
	ioCostPendingMap    = "iocost_pending_map"
	ioCostWakeFrameMap  = "iocost_wake_frame_map"
)

const (
	ioCostIDSequenceMapMaxEntries uint32 = 1
	ioCostPendingMapMaxEntries    uint32 = 10240
	ioCostWakeFrameMapMaxEntries  uint32 = 1
	ioCostStatusMapMaxEntries     uint32 = 1
)

const (
	ioCostPendingSize   = 16
	ioCostWakeFrameSize = 40
)

// ioCostPending is the value of iocost_pending_map.
type ioCostPending struct {
	StartNS   uint64
	Operation uint32
	Reserved  uint32
}

// ioCostWakeFrame is one possible-CPU lane of iocost_wake_frame_map.
type ioCostWakeFrame struct {
	BioPtr  uint64
	IOCGPtr uint64
	EndNS   uint64
	Pending ioCostPending
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
	pending, err := decodeIOCostPending(data[24:40])
	if err != nil {
		return ioCostWakeFrame{}, err
	}
	value := ioCostWakeFrame{
		BioPtr:  binary.LittleEndian.Uint64(data[0:8]),
		IOCGPtr: binary.LittleEndian.Uint64(data[8:16]),
		EndNS:   binary.LittleEndian.Uint64(data[16:24]),
		Pending: pending,
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

// Interval metrics and attribution.

// Interval deltas, scope attribution, and public metric contracts.

// This file freezes IOCOST interval transactions, attribution boundaries and
// the public metric schema. The BPF capture/ABI contract is covered separately
// by iocost_test.go and iocost_test.go.

type ioCostIntervalTestRow struct {
	iocPtr    uint64
	iocID     uint64
	iocgPtr   uint64
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
		iocPtr:    ioCostCaptureTestIOCPtr,
		iocID:     ioCostCaptureTestIOCID,
		iocgPtr:   ioCostCaptureTestIOCGPtr,
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

func setIOCostIntervalTestRows(
	object *fakeIOCostCaptureBPF,
	rows ...ioCostIntervalTestRow,
) {
	aggregates := make([]bpf.MapItem, 0, len(rows))
	owners := make([]bpf.MapItem, 0, len(rows))
	iocs := make([]bpf.MapItem, 0, len(rows))
	seenIOCs := make(map[uint64]struct{}, len(rows))
	for _, row := range rows {
		aggregates = append(aggregates, ioCostCaptureTestAggregateItem(
			row.iocID,
			row.cssSerial,
			row.operation,
			0,
			row.lanes...,
		))
		owners = append(owners, ioCostCaptureTestOwnerItem(
			row.iocgPtr,
			row.iocPtr,
			row.iocID,
			row.css,
			row.cssSerial,
		))
		if _, exists := seenIOCs[row.iocPtr]; exists {
			continue
		}
		seenIOCs[row.iocPtr] = struct{}{}
		iocs = append(iocs, ioCostCaptureTestIOCItem(
			row.iocPtr,
			row.iocID,
			row.major,
			row.minor,
		))
	}
	object.setDefaultItems(ioCostWaitAggregateMap, aggregates)
	object.setDefaultItems(ioCostOwnerStateMap, owners)
	object.setDefaultItems(ioCostIOCStateMap, iocs)
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
	harness.session.previous = newIOCostRawSnapshot()
	return &ioCostIntervalTestFixture{
		collector: &iocostTracing{session: harness.session},
		harness:   harness,
		object:    object,
		source:    source,
	}
}

type inspectedIOCostMetric struct {
	name      string
	fqName    string
	help      string
	valueType int
	value     float64
	labelKeys []string
	labels    map[string]string
}

func inspectIOCostMetric(
	t *testing.T,
	data *metric.Data,
) inspectedIOCostMetric {
	t.Helper()
	require.NotNil(t, data)
	value := reflect.ValueOf(data)
	require.Equal(t, reflect.Pointer, value.Kind())
	value = value.Elem()

	name := value.FieldByName("name").String()
	keys := value.FieldByName("labelKey")
	values := value.FieldByName("labelValue")
	require.Equal(t, keys.Len(), values.Len())
	labels := make(map[string]string, keys.Len())
	labelKeys := make([]string, keys.Len())
	for index := 0; index < keys.Len(); index++ {
		labelKeys[index] = keys.Index(index).String()
		labels[labelKeys[index]] = values.Index(index).String()
	}

	return inspectedIOCostMetric{
		name:      name,
		fqName:    prometheus.BuildFQName(metric.DefaultNamespace, ioCostTracingName, name),
		help:      value.FieldByName("help").String(),
		valueType: int(value.FieldByName("valueType").Int()),
		value:     data.Value,
		labelKeys: labelKeys,
		labels:    labels,
	}
}

func ioCostMetricValues(
	t *testing.T,
	metrics []*metric.Data,
) map[string]float64 {
	t.Helper()
	values := make(map[string]float64, len(metrics))
	for _, data := range metrics {
		got := inspectIOCostMetric(t, data)
		kind := got.labels["scope"]
		if len(got.name) >= len("container_") &&
			got.name[:len("container_")] == "container_" {
			kind = "container"
		}
		values[kind+"/"+got.name] = got.value
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
		"host/waitq_io_count":             count,
		"host/average_wait_milliseconds":  averageMS,
		"other/waitq_io_count":            count,
		"other/average_wait_milliseconds": averageMS,
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
		"host/waitq_io_count":                           count,
		"host/average_wait_milliseconds":                averageMS,
		"container/container_waitq_io_count":            count,
		"container/container_average_wait_milliseconds": averageMS,
	}, ioCostMetricValues(t, metrics))
}

func TestIOCostIntervalSyntheticZeroDeltaAndSessionReset(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	setIOCostIntervalTestRows(
		fixture.object,
		ioCostDefaultIntervalTestRow(10, 2000),
	)

	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 10, 2)
	require.Len(t, fixture.collector.session.previous.Samples, 1,
		"the first successful scrape must commit its complete raw baseline")

	setIOCostIntervalTestRows(
		fixture.object,
		ioCostDefaultIntervalTestRow(13, 2900),
	)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 3, 3)

	// A rebuilt BPF session starts with an empty Go baseline. It must not
	// subtract the previous object's last values or warm up for one interval.
	rebuilt := newIOCostIntervalTestFixture(t, 1, nil)
	setIOCostIntervalTestRows(
		rebuilt.object,
		ioCostDefaultIntervalTestRow(13, 2900),
	)
	metrics, err = rebuilt.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 13, 29.0/13.0)
}

func TestIOCostIntervalIdentityChurnKeepsOnlyLiveBaseline(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	seed := ioCostDefaultIntervalTestRow(97, 19400)
	setIOCostIntervalTestRows(fixture.object, seed)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 97, 2)
	oldKey := ioCostWaitKey{
		IOCID:     seed.iocID,
		CSSSerial: seed.cssSerial,
		Operation: seed.operation,
	}

	for generation := 1; generation <= 3; generation++ {
		row := ioCostDefaultIntervalTestRow(2, 400)
		row.iocID += uint64(generation)
		row.cssSerial += uint64(generation)

		// Reuse the IOC, IOCG and CSS pointers while changing both stable
		// identities. captureRawSnapshot validates these pointer-keyed BPF
		// rows, then intentionally retains only ioc_id and css_serial in the
		// Go baseline.
		setIOCostIntervalTestRows(fixture.object, row)
		previous := fixture.collector.session.previous
		metrics, err = fixture.collector.Update()
		require.NoError(t, err, "generation %d", generation)
		requireIOCostHostAndOtherInterval(t, metrics, 2, 2)
		require.NotSame(t, previous, fixture.collector.session.previous,
			"generation %d did not commit its successful boundary", generation)

		key := ioCostWaitKey{
			IOCID:     row.iocID,
			CSSSerial: row.cssSerial,
			Operation: row.operation,
		}
		identity := ioCostOwnerIdentity{
			IOCID:     row.iocID,
			CSSSerial: row.cssSerial,
		}
		committed := fixture.collector.session.previous
		require.Equal(t, map[ioCostWaitKey]ioCostRawSample{
			key: {
				CSS:       row.css,
				Device:    "8:16",
				Operation: "read",
				Counters:  []ioCostCumulative{{IOCount: 2, Wait10US: 400}},
			},
		}, committed.Samples, "generation %d retained historical samples", generation)
		require.Equal(t, map[uint64]string{
			row.iocID: "8:16",
		}, committed.LiveIOCs,
			"generation %d retained historical IOCs", generation)
		require.Equal(t, map[ioCostOwnerIdentity]uint64{
			identity: row.css,
		}, committed.LiveOwners,
			"generation %d retained historical owners", generation)
		require.NotContains(t, committed.Samples, oldKey,
			"generation %d retained the deleted identity baseline", generation)
		oldKey = key
	}
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostIntervalCommitsAndSubtractsFullPerCPULanes(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 2, nil)
	row := ioCostDefaultIntervalTestRow(0, 0)
	row.lanes = []ioCostCaptureTestLane{
		ioCostCaptureStableLane(10, 10),
		ioCostCaptureStableLane(20, 20),
	}
	setIOCostIntervalTestRows(fixture.object, row)

	_, err := fixture.collector.Update()
	require.NoError(t, err)
	key := ioCostWaitKey{
		IOCID:     row.iocID,
		CSSSerial: row.cssSerial,
		Operation: row.operation,
	}
	require.Equal(t, []ioCostCumulative{
		{IOCount: 10, Wait10US: 10},
		{IOCount: 20, Wait10US: 20},
	}, fixture.collector.session.previous.Samples[key].Counters)

	row.lanes = []ioCostCaptureTestLane{
		ioCostCaptureStableLane(12, 14),
		ioCostCaptureStableLane(23, 29),
	}
	setIOCostIntervalTestRows(fixture.object, row)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 5, 13.0*0.01/5.0)
}

func TestIOCostIntervalUsesIndependentPackedModuloDeltas(t *testing.T) {
	tests := []struct {
		name     string
		previous ioCostCumulative
		current  ioCostCumulative
		want     ioCostCumulative
	}{
		{
			name:     "IO count wraps",
			previous: ioCostCumulative{IOCount: (1 << 26) - 2, Wait10US: 100},
			current:  ioCostCumulative{IOCount: 1, Wait10US: 106},
			want:     ioCostCumulative{IOCount: 3, Wait10US: 6},
		},
		{
			name:     "wait field wraps",
			previous: ioCostCumulative{IOCount: 5, Wait10US: (1 << 38) - 2},
			current:  ioCostCumulative{IOCount: 7, Wait10US: 1},
			want:     ioCostCumulative{IOCount: 2, Wait10US: 3},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deltaIOCostWaitCounters(
				[]ioCostCumulative{test.previous},
				[]ioCostCumulative{test.current},
			)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestIOCostIntervalPerCPUShapeValidation(t *testing.T) {
	t.Run("one lane may have wait with zero count when the raw total has IO", func(t *testing.T) {
		got, err := deltaIOCostWaitCounters(nil, []ioCostCumulative{
			{IOCount: 0, Wait10US: 5},
			{IOCount: 1, Wait10US: 0},
		})
		require.NoError(t, err)
		require.Equal(t, ioCostCumulative{IOCount: 1, Wait10US: 5}, got)
	})

	tests := []struct {
		name     string
		previous []ioCostCumulative
		current  []ioCostCumulative
		contains string
	}{
		{
			name:     "empty lanes",
			current:  nil,
			contains: "empty per-CPU counters",
		},
		{
			name:     "lane count changed",
			previous: []ioCostCumulative{{}, {}},
			current:  []ioCostCumulative{{}},
			contains: "counter count changed",
		},
		{
			name:     "raw interval wait without IO",
			current:  []ioCostCumulative{{Wait10US: 1}},
			contains: "with no IO",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deltaIOCostWaitCounters(test.previous, test.current)
			require.ErrorContains(t, err, test.contains)
			require.Zero(t, got)
		})
	}
}

func TestIOCostIntervalAccumulatesPackedLanesWithoutTruncation(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 2, nil)
	row := ioCostDefaultIntervalTestRow(0, 0)
	row.lanes = []ioCostCaptureTestLane{
		ioCostCaptureStableLane((1<<26)-1, (1<<38)-1),
		ioCostCaptureStableLane(1, 1),
	}
	setIOCostIntervalTestRows(fixture.object, row)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 1<<26, 40.96)
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostIntervalRejectsEveryAggregationOverflow(t *testing.T) {
	containerA := newIOControlAttributionTestContainer(
		"container-a", 0x11, time.Unix(100, 0))
	containerB := newIOControlAttributionTestContainer(
		"container-b", 0x22, time.Unix(200, 0))
	cssContainers := pod.BuildCssContainers(map[string]*pod.Container{
		containerA.ID: containerA,
		containerB.ID: containerB,
	}, subsystem.SubsystemBlkIO)

	tests := []struct {
		name       string
		containers map[uint64]*pod.Container
		css        [2]uint64
		counters   [2]ioCostCumulative
		contains   string
	}{
		{
			name:       "Other",
			containers: nil,
			css:        [2]uint64{0x31, 0x32},
			counters: [2]ioCostCumulative{
				{IOCount: math.MaxUint64},
				{IOCount: 1},
			},
			contains: "other",
		},
		{
			name:       "Container",
			containers: cssContainers,
			css:        [2]uint64{0x11, 0x22},
			counters: [2]ioCostCumulative{
				{IOCount: 1, Wait10US: math.MaxUint64},
				{IOCount: 1, Wait10US: 1},
			},
			contains: "container",
		},
		{
			name:       "Host",
			containers: cssContainers,
			css:        [2]uint64{0x11, 0x33},
			counters: [2]ioCostCumulative{
				{IOCount: math.MaxUint64},
				{IOCount: 1},
			},
			contains: "host",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := make(map[ioCostWaitKey]ioCostInterval, len(test.css))
			for index := range test.css {
				serial := uint64(index + 1)
				raw[ioCostWaitKey{IOCID: serial, CSSSerial: serial}] = ioCostInterval{
					css: test.css[index], device: "8:16", operation: "read",
					counters: test.counters[index],
				}
			}
			got, err := aggregateIOCostAttributedIntervals(
				raw, test.containers)
			require.Nil(t, got)
			require.ErrorIs(t, err, errIOCostSessionInvalid)
			require.ErrorContains(t, err, test.contains)
		})
	}
}

func TestIOCostIntervalDisappearedRawKeyUsesLiveIdentities(t *testing.T) {
	key := ioCostWaitKey{
		IOCID:     ioCostCaptureTestIOCID,
		CSSSerial: ioCostCaptureTestCSSSerial,
		Operation: 0,
	}
	previous := newIOCostRawSnapshot()
	previous.Samples[key] = ioCostRawSample{
		CSS:       ioCostCaptureTestCSS,
		Device:    "8:16",
		Operation: "read",
		Counters:  []ioCostCumulative{{IOCount: 1, Wait10US: 2}},
	}
	identity := ioCostOwnerIdentity{
		IOCID: key.IOCID, CSSSerial: key.CSSSerial,
	}

	tests := []struct {
		name      string
		ownerLive bool
		iocLive   bool
		invalid   bool
	}{
		{name: "both deleted"},
		{name: "owner deleted", iocLive: true},
		{name: "IOC deleted", ownerLive: true},
		{name: "both live", ownerLive: true, iocLive: true, invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := newIOCostRawSnapshot()
			if test.ownerLive {
				current.LiveOwners[identity] = ioCostCaptureTestCSS
			}
			if test.iocLive {
				current.LiveIOCs[key.IOCID] = "8:16"
			}
			got, err := deltaIOCostWaitIntervals(previous, current)
			if test.invalid {
				require.Error(t, err)
				require.ErrorContains(t, err, "live aggregate disappeared")
				require.NotErrorIs(t, err, types.ErrTracingStopped)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}
}

func TestIOCostIntervalLiveDisappearanceRebuildsBaseline(t *testing.T) {
	fixture := newIOCostCommittedIntervalFixture(t)
	previous := fixture.collector.session.previous

	fixture.object.setDefaultItems(ioCostWaitAggregateMap, nil)

	metrics, err := fixture.collector.Update()
	require.Nil(t, metrics)
	require.ErrorContains(t, err, "live aggregate disappeared")
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Zero(t, fixture.harness.cancelCount())
	require.Same(t, previous, fixture.collector.session.previous)
	require.Nil(t, context.Cause(fixture.harness.session.breaker))

	captureErr := errors.New("temporary capture failure")
	fixture.object.queueDumps(ioCostWaitAggregateMap,
		ioCostCaptureTestDump{err: captureErr})
	metrics, err = fixture.collector.Update()
	require.ErrorIs(t, err, captureErr)
	require.Nil(t, metrics)
	require.Zero(t, fixture.harness.cancelCount())
	require.Same(t, previous, fixture.collector.session.previous)

	// A re-created counter establishes its own boundary before publication.
	setIOCostIntervalTestRows(fixture.object,
		ioCostDefaultIntervalTestRow(1, 300))
	metrics, err = fixture.collector.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Nil(t, metrics)
	require.NotSame(t, previous, fixture.collector.session.previous)
	require.Zero(t, fixture.harness.cancelCount())

	setIOCostIntervalTestRows(fixture.object,
		ioCostDefaultIntervalTestRow(3, 1100))
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 2, 4)
	require.Nil(t, context.Cause(fixture.harness.session.breaker))
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostIntervalLifecycleDeletionCommitsEmptyBaseline(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	key := ioCostWaitKey{
		IOCID:     ioCostCaptureTestIOCID,
		CSSSerial: ioCostCaptureTestCSSSerial,
		Operation: 0,
	}
	fixture.collector.session.previous.Samples[key] = ioCostRawSample{
		CSS:       ioCostCaptureTestCSS,
		Device:    "8:16",
		Operation: "read",
		Counters:  []ioCostCumulative{{IOCount: 1, Wait10US: 2}},
	}
	previous := fixture.collector.session.previous
	fixture.object.setDefaultItems(ioCostWaitAggregateMap, nil)
	fixture.object.setDefaultItems(ioCostOwnerStateMap, nil)
	// The IOC remains live while the owner has already been removed. Either
	// missing identity is sufficient to classify the old row as deletion.

	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	require.Empty(t, metrics)
	require.NotSame(t, previous, fixture.collector.session.previous)
	require.Empty(t, fixture.collector.session.previous.Samples)
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostIntervalParentReplacementPreservesOtherIOCs(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	stable := ioCostDefaultIntervalTestRow(10, 1000)
	old := ioCostDefaultIntervalTestRow(40, 8000)
	old.iocPtr++
	old.iocID++
	old.iocgPtr++
	setIOCostIntervalTestRows(fixture.object, stable, old)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 50, 1.8)

	stable.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(13, 1600)}
	old.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(42, 9000)}
	setIOCostIntervalTestRows(fixture.object, stable, old)
	replacement := old
	replacement.iocID++
	// Aggregate and owner dumps precede teardown; the later IOC dump sees
	// the replacement at the same pointer. Only the old IOC's tail is lost.
	oldKey := ioCostWaitKey{IOCID: old.iocID, CSSSerial: old.cssSerial}
	newKey := ioCostWaitKey{IOCID: replacement.iocID, CSSSerial: replacement.cssSerial}
	fixture.object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{
		ioCostCaptureTestIOCItem(stable.iocPtr, stable.iocID,
			stable.major, stable.minor),
		ioCostCaptureTestIOCItem(replacement.iocPtr, replacement.iocID,
			replacement.major, replacement.minor),
	})
	callsBefore := len(fixture.object.snapshotCalls())
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 3, 2)
	require.Equal(t, ioCostCaptureAllStages(), ioCostCaptureTestStages(
		fixture.object.snapshotCalls()[callsBefore:]))
	require.Len(t, fixture.harness.session.previous.Samples, 1)
	require.NotContains(t, fixture.harness.session.previous.Samples, oldKey)
	require.NotContains(t, fixture.harness.session.previous.Samples, newKey)
	require.False(t, fixture.harness.session.needsRebaseline)
	require.Zero(t, fixture.harness.cancelCount())

	// The new raw key starts at zero even though its IOC pointer and device
	// match the deleted IOC. It publishes all seven completions immediately.
	stable.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(15, 2000)}
	replacement.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(7, 2500)}
	setIOCostIntervalTestRows(fixture.object, stable, replacement)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 9, 29.0/9)
	require.Len(t, fixture.harness.session.previous.Samples, 2)
	require.Contains(t, fixture.harness.session.previous.Samples, newKey)
	require.False(t, fixture.harness.session.needsRebaseline)
	require.Zero(t, fixture.harness.cancelCount())

	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 0, 0)
}

func TestIOCostIntervalSuccessfulEmptyTransactionCommits(t *testing.T) {
	container := newIOControlAttributionTestContainer(
		"new-container",
		0x11,
		time.Unix(100, 0),
	)
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	setIOCostIntervalTestRows(fixture.object)
	fixture.source.containers = map[string]*pod.Container{
		container.ID: container,
	}
	previous := fixture.collector.session.previous

	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	require.Empty(t, metrics)
	require.NotSame(t, previous, fixture.collector.session.previous)
}

func newIOCostCommittedIntervalFixture(
	t *testing.T,
) *ioCostIntervalTestFixture {
	t.Helper()
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	setIOCostIntervalTestRows(
		fixture.object,
		ioCostDefaultIntervalTestRow(10, 1000),
	)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 10, 1)
	return fixture
}

func TestIOCostIntervalDeviceBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		minor    uint32
		newIOC   bool
		recovery bool
	}{
		{name: "same device including indistinguishable reuse", minor: 16},
		{name: "changed device", minor: 32},
		{name: "new IOC starts from zero", minor: 32, newIOC: true},
		{name: "recovery precedes device change", minor: 32, recovery: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIOCostCommittedIntervalFixture(t)
			previous := fixture.harness.session.previous

			if test.recovery {
				captureErr := errors.New("temporary capture failure")
				fixture.object.queueDumps(ioCostWaitAggregateMap,
					ioCostCaptureTestDump{err: captureErr})
				_, err := fixture.collector.Update()
				require.ErrorIs(t, err, captureErr)
			}
			row := ioCostDefaultIntervalTestRow(13, 1900)
			row.minor = test.minor
			if test.newIOC {
				row.iocID++
			}
			setIOCostIntervalTestRows(fixture.object, row)
			metrics, err := fixture.collector.Update()
			switch {
			case test.recovery:
				require.ErrorIs(t, err, metric.ErrNoData)
				require.Nil(t, metrics)
			case test.newIOC:
				require.NoError(t, err)
				requireIOCostHostAndOtherInterval(t, metrics, 13, 19.0/13.0)
			case test.minor == 32:
				require.NoError(t, err)
				require.Empty(t, metrics)
			default:
				require.NoError(t, err)
				requireIOCostHostAndOtherInterval(t, metrics, 3, 3)
			}
			require.NotSame(t, previous, fixture.harness.session.previous)

			require.False(t, fixture.harness.session.needsRebaseline)
			row.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(15, 2700)}
			setIOCostIntervalTestRows(fixture.object, row)
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndOtherInterval(t, metrics, 2, 4)
			for _, data := range metrics {
				require.Equal(t, fmt.Sprintf("8:%d", test.minor),
					inspectIOCostMetric(t, data).labels["device"])
			}
			require.Zero(t, fixture.harness.cancelCount())
		})
	}
}

func TestIOCostIntervalDeviceBoundaryConvergesAfterMixedCopy(t *testing.T) {
	fixture := newIOCostCommittedIntervalFixture(t)
	row := ioCostDefaultIntervalTestRow(13, 1900)
	row.major = 259
	// Model a dump with new major/old minor, then the stable new device.
	// This exercises the publication policy, not a kernel copy race.
	for index, minor := range []uint32{16, 32} {
		row.minor = minor
		row.lanes = []ioCostCaptureTestLane{
			ioCostCaptureStableLane(uint64(13+index*3), uint64(1900+index*900)),
		}
		setIOCostIntervalTestRows(fixture.object, row)
		metrics, err := fixture.collector.Update()
		require.NoError(t, err)
		require.Empty(t, metrics)
		require.Equal(t, fmt.Sprintf("259:%d", minor),
			fixture.harness.session.previous.LiveIOCs[row.iocID])
		require.False(t, fixture.harness.session.needsRebaseline)
		require.Zero(t, fixture.harness.cancelCount())
	}
	row.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(18, 3600)}
	setIOCostIntervalTestRows(fixture.object, row)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 2, 4)
	for _, data := range metrics {
		require.Equal(t, "259:32", inspectIOCostMetric(t, data).labels["device"])
	}
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostIntervalDeviceBoundaryIsolatesWholeIOC(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	read := ioCostDefaultIntervalTestRow(10, 1000)
	write := read
	write.iocgPtr++
	write.css++
	write.cssSerial++
	write.operation = 1
	other := read
	other.iocPtr++
	other.iocID++
	other.iocgPtr += 2
	other.minor = 48
	setIOCostIntervalTestRows(fixture.object, read, write, other)
	_, err := fixture.collector.Update()
	require.NoError(t, err)

	read.minor = 32
	write.minor = 32
	// This stale interval would be invalid (wait growth without count growth)
	// if the changed IOC reached delta calculation.
	read.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(10, 2000)}
	newOwner := read
	newOwner.iocgPtr += 3
	newOwner.css += 3
	newOwner.cssSerial += 3
	other.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(13, 1900)}
	setIOCostIntervalTestRows(fixture.object, read, write, newOwner, other)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 3, 3)
	for _, data := range metrics {
		require.Equal(t, "8:48", inspectIOCostMetric(t, data).labels["device"])
	}
	require.Len(t, fixture.harness.session.previous.Samples, 4)
	require.False(t, fixture.harness.session.needsRebaseline)
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostTransactionRecoveryRebaselinesBeforeDelta(t *testing.T) {
	for _, count := range []uint64{13, 10} {
		t.Run(fmt.Sprintf("recovery count %d", count), func(t *testing.T) {
			fixture := newIOCostCommittedIntervalFixture(t)
			previous := fixture.collector.session.previous

			captureErr := errors.New("temporary capture failure")
			for range 2 {
				fixture.object.queueDumps(ioCostWaitAggregateMap,
					ioCostCaptureTestDump{err: captureErr})
				metrics, err := fixture.collector.Update()
				require.ErrorIs(t, err, captureErr)
				require.Nil(t, metrics)
				require.Same(t, previous, fixture.collector.session.previous)

				require.Zero(t, fixture.harness.cancelCount())
			}

			setIOCostIntervalTestRows(fixture.object,
				ioCostDefaultIntervalTestRow(count, 1900))
			// A stale baseline may no longer yield a valid interval: the
			// count=10 case has wait growth without an observed count delta.
			// Recovery must commit the new snapshot without using that delta.
			metrics, err := fixture.collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Nil(t, metrics)
			require.NotSame(t, previous, fixture.collector.session.previous)

			require.Zero(t, fixture.harness.cancelCount())

			setIOCostIntervalTestRows(fixture.object,
				ioCostDefaultIntervalTestRow(count+2, 2700))
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndOtherInterval(t, metrics, 2, 4)
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndOtherInterval(t, metrics, 0, 0)
		})
	}
}

func TestIOCostTransactionFailuresDoNotCommitAndRecoveryRebaselines(t *testing.T) {
	captureErr := errors.New("temporary capture backend failure")
	tests := []struct {
		name      string
		configure func(*testing.T, *ioCostIntervalTestFixture)
		wantErr   error
	}{
		{
			name: "aggregate capture",
			configure: func(_ *testing.T, fixture *ioCostIntervalTestFixture) {
				fixture.object.queueDumps(
					ioCostWaitAggregateMap,
					ioCostCaptureTestDump{err: captureErr},
				)
			},
			wantErr: captureErr,
		},
		{
			name: "owner capture",
			configure: func(_ *testing.T, fixture *ioCostIntervalTestFixture) {
				fixture.object.queueDumps(
					ioCostOwnerStateMap,
					ioCostCaptureTestDump{err: captureErr},
				)
			},
			wantErr: captureErr,
		},
		{
			name: "IOC capture",
			configure: func(_ *testing.T, fixture *ioCostIntervalTestFixture) {
				fixture.object.queueDumps(
					ioCostIOCStateMap,
					ioCostCaptureTestDump{err: captureErr},
				)
			},
			wantErr: captureErr,
		},
		{
			name: "persistent aggregate iteration instability",
			configure: func(_ *testing.T, fixture *ioCostIntervalTestFixture) {
				items := fixture.object.defaultItems[ioCostWaitAggregateMap]
				for range ioCostSnapshotAttempts {
					fixture.object.queueDumps(
						ioCostWaitAggregateMap,
						ioCostCaptureTestDump{
							items: append(cloneIOCostCaptureTestItems(items), items[0]),
						},
					)
				}
			},
			wantErr: errIOCostSnapshotUnstable,
		},
		{
			name: "persistent duplicate IOC ID",
			configure: func(_ *testing.T, fixture *ioCostIntervalTestFixture) {
				items := cloneIOCostCaptureTestItems(fixture.object.defaultItems[ioCostIOCStateMap])
				items = append(items, bpf.MapItem{
					Key:   ioCostCaptureTestUint64(ioCostCaptureTestIOCPtr + 1),
					Value: items[0].Value,
				})
				for range ioCostSnapshotAttempts {
					fixture.object.queueDumps(ioCostIOCStateMap, ioCostCaptureTestDump{items: items})
				}
			},
			wantErr: errIOCostSnapshotUnstable,
		},
		{
			name: "persistent duplicate owner identity",
			configure: func(_ *testing.T, fixture *ioCostIntervalTestFixture) {
				items := cloneIOCostCaptureTestItems(fixture.object.defaultItems[ioCostOwnerStateMap])
				items = append(items, bpf.MapItem{
					Key:   ioCostCaptureTestUint64(ioCostCaptureTestIOCGPtr + 1),
					Value: items[0].Value,
				})
				for range ioCostSnapshotAttempts {
					fixture.object.queueDumps(ioCostOwnerStateMap, ioCostCaptureTestDump{items: items})
				}
			},
			wantErr: errIOCostSnapshotUnstable,
		},
		{
			name: "status capture",
			configure: func(_ *testing.T, fixture *ioCostIntervalTestFixture) {
				fixture.object.queueStatus(
					ioCostCaptureTestRead{err: captureErr},
				)
			},
			wantErr: captureErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIOCostCommittedIntervalFixture(t)
			newContainer := newIOControlAttributionTestContainer(
				"candidate-container",
				ioCostCaptureTestCSS,
				time.Unix(200, 0),
			)
			fixture.source.containers = map[string]*pod.Container{
				newContainer.ID: newContainer,
			}
			setIOCostIntervalTestRows(
				fixture.object,
				ioCostDefaultIntervalTestRow(13, 1900),
			)
			previous := fixture.collector.session.previous
			test.configure(t, fixture)

			metrics, err := fixture.collector.Update()
			require.Error(t, err)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			}
			require.Nil(t, metrics)
			require.Same(t, previous, fixture.collector.session.previous)

			require.Zero(t, fixture.harness.cancelCount(),
				"transient capture failures must not restart")

			// The recovery scrape commits the new raw baseline without
			// publishing the failed interval.
			setIOCostIntervalTestRows(
				fixture.object,
				ioCostDefaultIntervalTestRow(13, 1900),
			)
			metrics, err = fixture.collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Nil(t, metrics)
			require.NotSame(t, previous, fixture.collector.session.previous)

			setIOCostIntervalTestRows(fixture.object,
				ioCostDefaultIntervalTestRow(15, 2700))
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndContainerInterval(t, metrics, 2, 4)
		})
	}
}

func TestIOCostTransactionFinalBreakerPreventsCommit(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovering %t", recovering), func(t *testing.T) {
			fixture := newIOCostCommittedIntervalFixture(t)
			if recovering {
				captureErr := errors.New("temporary capture failure")
				fixture.object.queueDumps(ioCostWaitAggregateMap,
					ioCostCaptureTestDump{err: captureErr})
				_, err := fixture.collector.Update()
				require.ErrorIs(t, err, captureErr)
			}
			previous := fixture.collector.session.previous

			setIOCostIntervalTestRows(fixture.object,
				ioCostDefaultIntervalTestRow(13, 1900))
			fixture.collector.session.containerSource = func() (
				map[string]*pod.Container, error,
			) {
				fixture.harness.cancel()
				return fixture.source.read()
			}

			metrics, err := fixture.collector.Update()
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, metrics)
			require.Same(t, previous, fixture.collector.session.previous)

			require.Zero(t, fixture.harness.cancelCount())
		})
	}
}

func TestIOCostAttributionUsesCurrentCSSMapping(t *testing.T) {
	container := newIOControlAttributionTestContainer(
		"new-container", ioCostCaptureTestCSS, time.Unix(200, 0),
	)
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	fixture.source.containers = map[string]*pod.Container{container.ID: container}
	setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(3, 900))
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 3, 3)

	setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(5, 1700))
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 2, 4)
	require.Zero(t, fixture.harness.cancelCount())
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
	require.NotContains(t, fixture.collector.session.previous.Samples, ioCostWaitKey{
		IOCID: newRow.iocID, CSSSerial: ioCostCaptureTestCSSSerial,
	})
	require.Contains(t, fixture.collector.session.previous.Samples, ioCostWaitKey{
		IOCID: newRow.iocID, CSSSerial: newRow.cssSerial,
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

func TestIOCostMetricsMergeBeforeAverageAndPreserveScopeEquation(t *testing.T) {
	containerA := newIOControlAttributionTestContainer(
		"container-a",
		0x11,
		time.Unix(100, 0),
	)
	containerB := newIOControlAttributionTestContainer(
		"container-b",
		0x22,
		time.Unix(200, 0),
	)
	containers := pod.BuildCssContainers(map[string]*pod.Container{
		containerA.ID: containerA,
		containerB.ID: containerB,
	}, subsystem.SubsystemBlkIO)
	raw := map[ioCostWaitKey]ioCostInterval{
		{IOCID: 1, CSSSerial: 1, Operation: 0}: {
			css: 0x11, device: "4095:1", operation: "read",
			counters: ioCostCumulative{IOCount: 2, Wait10US: 400},
		},
		{IOCID: 2, CSSSerial: 2, Operation: 0}: {
			css: 0x22, device: "4095:1", operation: "read",
			counters: ioCostCumulative{IOCount: 3, Wait10US: 900},
		},
		{IOCID: 3, CSSSerial: 3, Operation: 0}: {
			css: 0x33, device: "4095:1", operation: "read",
			counters: ioCostCumulative{IOCount: 7, Wait10US: 2800},
		},
	}

	intervals, err := aggregateIOCostAttributedIntervals(raw, containers)
	require.NoError(t, err)
	require.Len(t, intervals.host, 1)
	require.Len(t, intervals.other, 1)
	require.Len(t, intervals.containers, 1,
		"containers with identical public labels must be one series")
	labels, err := ioControlPublicContainerLabels(containerA)
	require.NoError(t, err)
	for key := range intervals.containers {
		require.Equal(t, labels, key.labels,
			"public labels must remain part of the container grouping key")
	}
	metrics := appendIOCostAttributedMetrics(nil, intervals)
	require.Equal(t, map[string]float64{
		"host/waitq_io_count":                           12,
		"host/average_wait_milliseconds":                41.0 / 12.0,
		"other/waitq_io_count":                          7,
		"other/average_wait_milliseconds":               4,
		"container/container_waitq_io_count":            5,
		"container/container_average_wait_milliseconds": 2.6,
	}, ioCostMetricValues(t, metrics))

	values := ioCostMetricValues(t, metrics)
	hostWaitMS := values["host/waitq_io_count"] *
		values["host/average_wait_milliseconds"]
	childWaitMS := values["other/waitq_io_count"]*
		values["other/average_wait_milliseconds"] +
		values["container/container_waitq_io_count"]*
			values["container/container_average_wait_milliseconds"]
	require.InDelta(t, childWaitMS, hostWaitMS, 1e-12)
	require.Equal(t,
		values["other/waitq_io_count"]+
			values["container/container_waitq_io_count"],
		values["host/waitq_io_count"],
	)
}

func TestIOCostMetricsFreezeFQNamesHelpTypesAndLabels(t *testing.T) {
	const (
		wantCountHelp = "Number of READ/WRITE I/O requests whose departure " +
			"from the IOCOST wait queue was observed by the paired probes " +
			"between consecutive successful iocost collections."
		wantAverageHelp = "Average IOCOST wait-queue residence time in " +
			"milliseconds for the READ/WRITE I/O requests counted by " +
			"waitq_io_count."
	)
	require.Equal(t, wantCountHelp, ioCostWaitCountHelp)
	require.Equal(t, wantAverageHelp, ioCostAverageWaitHelp)

	container := newIOControlAttributionTestContainer(
		"container-a",
		0x11,
		time.Unix(100, 0),
	)
	labels, err := ioControlPublicContainerLabels(container)
	require.NoError(t, err)
	intervals := &ioCostAttributedIntervals{
		host: map[ioCostHostKey]ioCostCumulative{
			{device: "9:0", operation: "write"}: {IOCount: 4, Wait10US: 800},
			{device: "8:16", operation: "read"}: {IOCount: 2, Wait10US: 200},
		},
		other: map[ioCostHostKey]ioCostCumulative{
			{device: "9:0", operation: "write"}: {IOCount: 1, Wait10US: 300},
			{device: "8:16", operation: "read"}: {},
		},
		containers: map[ioCostContainerKey]ioCostContainerInterval{
			{labels: labels, device: "9:0", operation: "write"}: {
				container: container,
				counters:  ioCostCumulative{IOCount: 3, Wait10US: 500},
			},
			{labels: labels, device: "8:16", operation: "read"}: {
				container: container,
				counters:  ioCostCumulative{IOCount: 2, Wait10US: 200},
			},
		},
	}

	metrics := appendIOCostAttributedMetrics(nil, intervals)
	require.Len(t, metrics, 12)

	wantFQNames := map[string]struct{}{
		"huatuo_bamai_iocost_waitq_io_count":                      {},
		"huatuo_bamai_iocost_average_wait_milliseconds":           {},
		"huatuo_bamai_iocost_container_waitq_io_count":            {},
		"huatuo_bamai_iocost_container_average_wait_milliseconds": {},
	}
	gotFQNames := make(map[string]struct{})
	for _, data := range metrics {
		got := inspectIOCostMetric(t, data)
		gotFQNames[got.fqName] = struct{}{}
		require.Equal(t, metric.MetricTypeGauge, got.valueType)
		if got.name == ioCostWaitCountName ||
			got.name == "container_"+ioCostWaitCountName {
			require.Equal(t, wantCountHelp, got.help)
		} else {
			require.Contains(t, []string{
				ioCostAverageWaitName,
				"container_" + ioCostAverageWaitName,
			}, got.name)
			require.Equal(t, wantAverageHelp, got.help)
		}
		if got.labels["scope"] != "" {
			require.Equal(t, []string{
				metric.LabelRegion,
				metric.LabelHost,
				"device",
				"operation",
				"scope",
			}, got.labelKeys)
			continue
		}
		require.Equal(t, []string{
			metric.LabelRegion,
			metric.LabelContainerHost,
			metric.LabelContainerName,
			metric.LabelContainerType,
			metric.LabelContainerLevel,
			metric.LabelContainerHostNamespace,
			metric.LabelHost,
			"device",
			"operation",
		}, got.labelKeys)
	}
	require.Equal(t, wantFQNames, gotFQNames)

	// Verify every public series independently of the collector's row order.
	signatures := make([]string, 0, len(metrics))
	for _, data := range metrics {
		got := inspectIOCostMetric(t, data)
		scope := got.labels["scope"]
		if scope == "" {
			scope = "container"
		}
		signatures = append(signatures, fmt.Sprintf(
			"%s/%s/%s/%s",
			scope,
			got.labels["device"],
			got.labels["operation"],
			got.name,
		))
	}
	require.ElementsMatch(t, []string{
		"host/8:16/read/waitq_io_count",
		"host/8:16/read/average_wait_milliseconds",
		"host/9:0/write/waitq_io_count",
		"host/9:0/write/average_wait_milliseconds",
		"other/8:16/read/waitq_io_count",
		"other/8:16/read/average_wait_milliseconds",
		"other/9:0/write/waitq_io_count",
		"other/9:0/write/average_wait_milliseconds",
		"container/8:16/read/container_waitq_io_count",
		"container/8:16/read/container_average_wait_milliseconds",
		"container/9:0/write/container_waitq_io_count",
		"container/9:0/write/container_average_wait_milliseconds",
	}, signatures)
}

func TestIOCostMetricsPublishZeroForAnExistingRawSeries(t *testing.T) {
	key := ioCostWaitKey{
		IOCID: 1, CSSSerial: 2, Operation: 0,
	}
	current := newIOCostRawSnapshot()
	current.Samples[key] = ioCostRawSample{
		CSS:       3,
		Device:    "8:16",
		Operation: "read",
		Counters:  []ioCostCumulative{{}},
	}

	metrics, err := buildIOCostWaitMetrics(
		newIOCostRawSnapshot(),
		current,
		nil,
	)
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, metrics, 0, 0)

	metrics, err = buildIOCostWaitMetrics(
		newIOCostRawSnapshot(),
		newIOCostRawSnapshot(),
		nil,
	)
	require.NoError(t, err)
	require.Empty(t, metrics,
		"a series that never existed must not be synthesized")
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
				require.False(t, fixture.collector.session.needsRebaseline)
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
			require.False(t, fixture.collector.session.needsRebaseline)
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

func TestIOCostTransactionUpdateMutexCancelWithdrawClose(t *testing.T) {
	for _, stage := range ioCostCaptureAllStages() {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			fixture := newIOCostIntervalTestFixture(t, 1, nil)
			object := &closeAwareIOCostCaptureBPF{
				fakeIOCostCaptureBPF: fixture.object,
			}
			fixture.collector.session.object = object
			previous := fixture.collector.session.previous

			entered := make(chan string, 1)
			release := make(chan struct{})
			if stage == ioCostStatusMap {
				fixture.object.queueStatus(ioCostCaptureTestRead{
					value:   fixture.object.statusValue,
					release: release,
					entered: entered,
				})
			} else {
				fixture.object.queueDumps(stage, ioCostCaptureTestDump{
					items:   fixture.object.defaultItems[stage],
					release: release,
					entered: entered,
				})
			}

			type updateResult struct {
				metrics []*metric.Data
				err     error
			}
			updated := make(chan updateResult, 1)
			go func() {
				metrics, err := fixture.collector.Update()
				updated <- updateResult{metrics: metrics, err: err}
			}()
			select {
			case got := <-entered:
				require.Equal(t, stage, got)
			case <-time.After(time.Second):
				t.Fatalf("Update did not enter %s", stage)
			}

			session := fixture.harness.session
			withdrawn := make(chan struct{})
			fixture.harness.cancel()
			go func() {
				fixture.collector.withdrawSession()
				_ = object.Close()
				close(withdrawn)
			}()
			select {
			case <-withdrawn:
				t.Fatal("object closed during an in-flight map read")
			case <-time.After(50 * time.Millisecond):
			}
			close(release)

			select {
			case result := <-updated:
				require.Nil(t, result.metrics)
				require.ErrorIs(t, result.err, context.Canceled)
				require.NotErrorIs(t, result.err, metric.ErrNoData)
			case <-time.After(time.Second):
				t.Fatalf("Update did not leave canceled %s", stage)
			}
			select {
			case <-withdrawn:
			case <-time.After(time.Second):
				t.Fatalf("withdraw did not complete after %s", stage)
			}

			closeCalls, readAfterClose, closeDuringRead := object.closeState()
			require.Equal(t, 1, closeCalls)
			require.False(t, readAfterClose)
			require.False(t, closeDuringRead)
			require.Same(t, previous, session.previous)

			metrics, err := fixture.collector.Update()
			require.Nil(t, metrics)
			require.ErrorIs(t, err, metric.ErrNoData)
		})
	}
}

// Registration, terminal errors, and collector isolation.

// This file freezes IOCOST factory, registration, no-data and failure-domain
// contracts without touching tracing's process-global once registry.

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

func TestIOCostFatalUpdateStopsStart(t *testing.T) {
	for _, test := range []struct {
		name   string
		status ioCostStatus
		reason string
	}{
		{
			name:   "map capacity",
			status: ioCostStatus{Reason: ioCostFailurePendingInsert, Errno: -int32(unix.E2BIG)},
			reason: "capacity exhausted (10240 entries)",
		},
		{
			name:   "duplicate pending",
			status: ioCostStatus{Reason: ioCostFailurePendingCollision},
			reason: "restoring an uncommitted waiter",
		},
		{
			name:   "required pending missing",
			status: ioCostStatus{Reason: ioCostFailurePendingDelete, Errno: -int32(unix.ENOENT)},
			reason: "helper returned -2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &ioCostSessionCaptureBPF{
				fakeIOCostBPF: newFakeIOCostBPF(nil),
				capture:       newFakeIOCostCaptureBPF(1),
			}
			object.capture.statusValue = encodeIOCostTestStatus(&test.status)
			collector := &iocostTracing{}
			startErrors := make(chan error, 1)
			ctx, cancelRun := context.WithCancel(t.Context())
			t.Cleanup(cancelRun)
			go func() {
				startErrors <- collector.startWithProfile(ctx,
					func(string, map[string]any) (bpf.BPF, error) { return object, nil },
					ioCostSessionTestProfile(), 1, emptyIOCostContainerSource)
			}()
			select {
			case <-object.waitStarted:
			case <-time.After(time.Second):
				t.Fatal("IOCOST session was not published")
			}
			metrics, err := collector.Update()
			require.Nil(t, metrics)
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorContains(t, err, test.reason)
			select {
			case err := <-startErrors:
				require.ErrorIs(t, err, types.ErrTracingStopped)
				require.ErrorContains(t, err, test.reason)
			case <-time.After(time.Second):
				t.Fatal("Update did not stop IOCOST Start")
			}
			require.Equal(t, 1, object.state().closeCalls)
			metrics, err = collector.Update()
			require.Nil(t, metrics)
			require.ErrorIs(t, err, metric.ErrNoData)
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

func TestIOCostNoDataForMissingOrAlreadyCanceledSession(t *testing.T) {
	t.Run("missing session", func(t *testing.T) {
		collector := &iocostTracing{}
		metrics, err := collector.Update()
		require.Nil(t, metrics)
		require.ErrorIs(t, err, metric.ErrNoData)
	})

	t.Run("already canceled published session", func(t *testing.T) {
		fixture := newIOCostIntervalTestFixture(t, 1, nil)
		key := ioCostWaitKey{
			IOCID:     ioCostCaptureTestIOCID,
			CSSSerial: ioCostCaptureTestCSSSerial,
			Operation: 0,
		}
		fixture.collector.session.previous.Samples[key] = ioCostRawSample{
			CSS:       ioCostCaptureTestCSS,
			Device:    "8:16",
			Operation: "read",
			Counters:  []ioCostCumulative{{IOCount: 7, Wait10US: 11}},
		}

		previous := fixture.collector.session.previous
		previousValue := cloneIOCostRawSnapshotForTest(previous)
		fixture.harness.cancel()

		metrics, err := fixture.collector.Update()
		require.Nil(t, metrics)
		require.ErrorIs(t, err, metric.ErrNoData)
		require.Empty(t, fixture.object.snapshotCalls(),
			"an already canceled session must not touch any BPF map")
		require.Same(t, previous, fixture.collector.session.previous)
		require.Equal(t, previousValue, fixture.collector.session.previous)

		require.Zero(t, fixture.harness.cancelCount())
	})
}

func cloneIOCostRawSnapshotForTest(
	snapshot *ioCostRawSnapshot,
) *ioCostRawSnapshot {
	clone := newIOCostRawSnapshot()
	for key, sample := range snapshot.Samples {
		copied := sample
		copied.Counters = append([]ioCostCumulative(nil), sample.Counters...)
		clone.Samples[key] = copied
	}
	for id, device := range snapshot.LiveIOCs {
		clone.LiveIOCs[id] = device
	}
	for identity, css := range snapshot.LiveOwners {
		clone.LiveOwners[identity] = css
	}
	return clone
}

func cloneThrotlWaitSnapshotForIOCostTest(
	snapshot throtlWaitSnapshot,
) throtlWaitSnapshot {
	clone := make(throtlWaitSnapshot, len(snapshot))
	for key, sample := range snapshot {
		copied := sample
		copied.counters = append(
			[]throtlWaitCounters(nil),
			sample.counters...,
		)
		clone[key] = copied
	}
	return clone
}

// Both collectors resolve the current CSS catalog immediately while retaining
// independent raw counters, including the kernel CSS serial in each baseline.
func TestIOWaitCollectorsUseCurrentCSSMappings(t *testing.T) {
	source := &ioControlAttributionTestSource{}
	throtlObject := newFakeThrotlCollectorBPF()
	throtl := newThrotlAttributedTestCollector(t, throtlObject, source)
	ioCost := newIOCostIntervalTestFixture(t, 1, nil)
	ioCost.collector.session.containerSource = source.read
	container := newIOControlAttributionTestContainer(
		"new-container", 0x11, time.Unix(100, 0),
	)
	source.containers = map[string]*pod.Container{container.ID: container}
	throtlKey := throtlTestKey{
		css: 0x11, blkg: 0x33, serial: 7, major: 4095, minor: 1,
	}
	ioCostRow := ioCostDefaultIntervalTestRow(5, 1500)
	ioCostRow.css = 0x11
	ioCostRow.cssSerial = 7
	throtlObject.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(throtlKey, stableThrotlTestLane(3, 900)),
	}})
	setIOCostIntervalTestRows(ioCost.object, ioCostRow)

	t.Run("throttle", func(t *testing.T) {
		metrics, err := throtl.Update()
		require.NoError(t, err)
		requireThrotlHostAndContainerInterval(t, metrics, 3, 3)
	})
	t.Run("iocost", func(t *testing.T) {
		metrics, err := ioCost.collector.Update()
		require.NoError(t, err)
		requireIOCostHostAndContainerInterval(t, metrics, 5, 3)
	})
	// A shared container source does not couple the collectors' baselines.
	previous := cloneThrotlWaitSnapshotForIOCostTest(throtl.session.previous)
	dumpErr := errors.New("temporary throttle snapshot failure")
	throtlObject.setDumps(throtlTestDump{err: dumpErr})
	metrics, err := throtl.Update()
	require.ErrorIs(t, err, dumpErr)
	require.Nil(t, metrics)
	require.Equal(t, previous, throtl.session.previous)

	ioCostRow.lanes = []ioCostCaptureTestLane{ioCostCaptureStableLane(7, 2300)}
	setIOCostIntervalTestRows(ioCost.object, ioCostRow)
	metrics, err = ioCost.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 2, 4)
	require.Equal(t, previous, throtl.session.previous)

	throtlObject.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(throtlKey, stableThrotlTestLane(5, 1700)),
	}})
	metrics, err = throtl.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Nil(t, metrics)

	throtlObject.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(throtlKey, stableThrotlTestLane(7, 2500)),
	}})
	metrics, err = throtl.Update()
	require.NoError(t, err)
	requireThrotlHostAndContainerInterval(t, metrics, 2, 4)
}

func TestIOCostIsolationFromThrotlFailureDomain(t *testing.T) {
	throtlBreaker := context.Background()
	for _, failure := range []string{"sticky unhealthy", "live disappearance"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			throtl, throtlObject, _, throtlKey := newThrotlAttributionFailureFixture(t)
			//nolint:fatcontext // Pin the independent collector to its test breaker.
			throtl.session.breaker = throtlBreaker
			throtlSession := throtl.session
			throtlPreviousValue := cloneThrotlWaitSnapshotForIOCostTest(
				throtlSession.previous,
			)
			throtlDumpCalls := throtlObject.dumpCalls
			throtlStatusCalls := throtlObject.statusCalls

			ioCost := newIOCostCommittedIntervalFixture(t)
			ioCostPrevious := ioCost.collector.session.previous
			switch failure {
			case "sticky unhealthy":
				ioCost.object.statusValue = encodeIOCostTestStatus(
					&ioCostStatus{Reason: ioCostFailurePendingCollision},
				)
			case "live disappearance":
				ioCost.object.setDefaultItems(ioCostWaitAggregateMap, nil)
			default:
				t.Fatalf("unknown failure fixture %q", failure)
			}

			metrics, err := ioCost.collector.Update()
			require.Nil(t, metrics)
			require.Error(t, err)
			require.Same(t, ioCostPrevious, ioCost.collector.session.previous)
			if failure == "sticky unhealthy" {
				require.ErrorIs(t, err, types.ErrTracingStopped)
				require.ErrorIs(t, context.Cause(ioCost.collector.session.breaker),
					types.ErrTracingStopped)
				require.Equal(t, 1, ioCost.harness.cancelCount())
				require.ErrorIs(t,
					ioCost.collector.session.breaker.Err(),
					context.Canceled,
				)
			} else {
				require.ErrorContains(t, err, "live aggregate disappeared")
				require.NotErrorIs(t, err, types.ErrTracingStopped)
				require.Nil(t, context.Cause(ioCost.collector.session.breaker))
				require.Zero(t, ioCost.harness.cancelCount())
				require.NoError(t, ioCost.collector.session.breaker.Err())
			}

			require.Same(t, throtlSession, throtl.session)
			require.True(t, throtlSession.object == throtlObject)
			require.True(t, throtlSession.breaker == throtlBreaker)
			require.NoError(t, throtlBreaker.Err())
			require.Equal(t, throtlPreviousValue, throtlSession.previous)

			require.Equal(t, throtlDumpCalls, throtlObject.dumpCalls)
			require.Equal(t, throtlStatusCalls, throtlObject.statusCalls)

			throtlObject.setDumps(throtlTestDump{items: []bpf.MapItem{
				throtlTestItem(
					throtlKey,
					stableThrotlTestLane(13, 1_900),
				),
			}})
			metrics, err = throtl.Update()
			require.NoError(t, err)
			requireThrotlHostAndContainerInterval(t, metrics, 3, 3)
		})
	}
}

// Session lifecycle and object contracts.

const (
	ioCostIOCStateMapMaxEntries      uint32 = 4096
	ioCostOwnerStateMapMaxEntries    uint32 = 4096
	ioCostWaitAggregateMapMaxEntries        = 2 * ioCostOwnerStateMapMaxEntries

	ioCostDiagnosticObjectName = "iocost_tracing_test.o"

	ioCostDiagnosticStatusMap  = "iocost_diag_stat_map"
	ioCostDiagnosticScratchMap = "iocost_diag_fault_scratch_map"

	ioCostDiagnosticFaultMaskConstant  = "iocost_diag_fault_mask"
	ioCostDiagnosticMajorConstant      = "iocost_diag_major"
	ioCostDiagnosticFirstMinorConstant = "iocost_diag_first_minor"
	ioCostDiagnosticCSSSerialConstant  = "iocost_diag_css_serial"
	ioCostDiagnosticIOCGPtrConstant    = "iocost_diag_iocg_ptr"

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

type ioCostTestEvents struct {
	mu     sync.Mutex
	values []string
}

func (events *ioCostTestEvents) add(value string) {
	if events == nil {
		return
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	events.values = append(events.values, value)
}

func (events *ioCostTestEvents) snapshot() []string {
	if events == nil {
		return nil
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	return append([]string(nil), events.values...)
}

type fakeIOCostBPF struct {
	bpf.BPF

	mu              sync.Mutex
	events          *ioCostTestEvents
	options         []bpf.AttachOption
	attachCalls     int
	attachFailAt    int
	attachErr       error
	statusMapID     uint32
	statusValue     []byte
	statusErrors    []error
	readCalls       int
	readStarted     chan struct{}
	releaseRead     chan struct{}
	readStartOnce   sync.Once
	waitStarted     chan struct{}
	waitStartOnce   sync.Once
	breakOnWait     bool
	closeCalls      int
	closeErr        error
	closed          bool
	readAfterClose  bool
	sessionVisible  func() bool
	visibleAtAttach bool
	visibleAtWait   bool
	visibleAtClose  bool
}

func newFakeIOCostBPF(events *ioCostTestEvents) *fakeIOCostBPF {
	return &fakeIOCostBPF{
		events:      events,
		statusMapID: 1,
		statusValue: encodeIOCostTestStatus(&ioCostStatus{}),
		waitStarted: make(chan struct{}),
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
	for index, option := range options {
		object.options = append(object.options, option)
		object.events.add("attach:" + option.ProgramName)
		if object.attachFailAt == index+1 {
			return object.attachErr
		}
	}
	return nil
}

func (object *fakeIOCostBPF) MapIDByName(name string) uint32 {
	object.mu.Lock()
	defer object.mu.Unlock()
	if name == ioCostStatusMap {
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
		object.readAfterClose = true
	}
	var readErr error
	if call <= len(object.statusErrors) {
		readErr = object.statusErrors[call-1]
	}
	value := append([]byte(nil), object.statusValue...)
	started := object.readStarted
	release := object.releaseRead
	object.mu.Unlock()

	if started != nil {
		object.readStartOnce.Do(func() { close(started) })
	}
	if release != nil {
		<-release
	}
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

func (object *fakeIOCostBPF) DetachOnContextDone(
	_ context.Context,
	cancel context.CancelFunc,
) {
	visible := false
	if object.sessionVisible != nil {
		visible = object.sessionVisible()
	}
	object.mu.Lock()
	object.visibleAtWait = object.visibleAtWait || visible
	object.mu.Unlock()
	object.events.add("wait-detach")
	object.waitStartOnce.Do(func() { close(object.waitStarted) })
	if object.breakOnWait {
		cancel()
	}
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
	object.events.add("object-close")
	return object.closeErr
}

type fakeIOCostBPFState struct {
	options        []bpf.AttachOption
	attachCalls    int
	closeCalls     int
	readAfterClose bool
	visibleAtClose bool
}

func (object *fakeIOCostBPF) state() fakeIOCostBPFState {
	object.mu.Lock()
	defer object.mu.Unlock()
	return fakeIOCostBPFState{
		options:        append([]bpf.AttachOption(nil), object.options...),
		attachCalls:    object.attachCalls,
		closeCalls:     object.closeCalls,
		readAfterClose: object.readAfterClose,
		visibleAtClose: object.visibleAtClose,
	}
}

func (object *fakeIOCostBPF) publicationState() (
	visibleAtAttach bool,
	visibleAtWait bool,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	return object.visibleAtAttach, object.visibleAtWait
}

func ioCostSessionTestProfile() *ioCostKernelProfile {
	return ioCostTestKernelProfile(true)
}

func ioCostSessionTestAttachOptions() []bpf.AttachOption {
	options := []bpf.AttachOption{
		{ProgramName: ioCostWakeReturnProgram, Symbol: ioCostWakeSymbol},
		{ProgramName: ioCostWakeEntryProgram, Symbol: ioCostWakeSymbol},
		{ProgramName: ioCostPDFreeProgram, Symbol: ioCostPDFreeSymbol},
		{ProgramName: ioCostExitProgram, Symbol: ioCostExitSymbol},
	}
	return append(options, bpf.AttachOption{
		ProgramName: ioCostKickProgram,
		Symbol:      ioCostKickSymbol,
	})
}

func emptyIOCostContainerSource() (map[string]*pod.Container, error) {
	return nil, nil
}

func encodeIOCostTestStatus(status *ioCostStatus) []byte {
	data := make([]byte, ioCostStatusSize)
	binary.LittleEndian.PutUint64(data, uint64(status.Reason)<<32|uint64(uint32(status.Errno)))
	return data
}

func TestIOCostObjectContract(t *testing.T) {
	spec := loadIOCostObjectSpec(t)
	requireIOCostPrograms(t, spec)
	wantMaps := ioCostBusinessMapContracts()
	require.Len(t, spec.Maps, len(wantMaps)+1,
		"compiled object must contain only the seven IOCOST maps and .rodata")
	_, hasRodata := spec.Maps[".rodata"]
	require.True(t, hasRodata, "compiled object has no .rodata map")
	requireIOCostBusinessMaps(t, spec)
	require.NotContains(t, spec.Maps, ioCostDiagnosticStatusMap)
	require.NotContains(t, spec.Maps, ioCostDiagnosticScratchMap)
	requireIOCostProductionObjectExcludesDiagnostics(t, spec)
}

// Relocate the compiled probes, including the serial read, against a kernel
// containing an unrelated same-named CSS with a different field offset.
func TestIOCostObjectRepeatedCSS(t *testing.T) {
	for _, object := range []*ebpf.CollectionSpec{loadIOCostObjectSpec(t), loadIOCostDiagnosticObjectSpec(t)} {
		for _, first := range []bool{false, true} {
			target := newSyntheticIOCostBTFSpec(t, syntheticIOCost510Profile, func(fixture *syntheticIOCostBTFFixture) {
				duplicate := btf.Copy(fixture.structures["cgroup_subsys_state"]).(*btf.Struct)
				duplicate.Members[0].Offset = 64
				if first {
					fixture.types = append([]btf.Type{duplicate}, fixture.types...)
				} else {
					fixture.types = append(fixture.types, duplicate)
				}
			})
			var relocations []*btf.CORERelocation
			for _, program := range object.Programs {
				for index := range program.Instructions {
					if relocation := btf.CORERelocationMetadata(&program.Instructions[index]); relocation != nil {
						relocations = append(relocations, relocation)
					}
				}
			}
			require.NotEmpty(t, relocations)
			_, err := btf.CORERelocate(relocations, []*btf.Spec{target}, object.ByteOrder, object.Types.TypeID)
			require.NoError(t, err)
		}
	}
}

// Relocate the actual object for old, transitional, current and backported
// field combinations. The fixtures contain structures without BTF FUNCs.
func TestIOCostObjectDeviceFieldSelection(t *testing.T) {
	for _, object := range []*ebpf.CollectionSpec{loadIOCostObjectSpec(t), loadIOCostDiagnosticObjectSpec(t)} {
		var relocations []*btf.CORERelocation
		for _, program := range object.Programs {
			for index := range program.Instructions {
				if relocation := btf.CORERelocationMetadata(&program.Instructions[index]); relocation != nil {
					relocations = append(relocations, relocation)
				}
			}
		}
		require.NotEmpty(t, relocations)
		for _, device := range []syntheticIOCostDeviceProfile{
			syntheticIOCost510Profile,
			syntheticIOCostHybridProfile,
			syntheticIOCostMainlineProfile,
			syntheticIOCostBothProfiles,
			syntheticIOCostDiskBioDiskQOSProfile,
		} {
			target := newSyntheticIOCostBTFSpec(t, device, nil)
			_, err := btf.CORERelocate(relocations, []*btf.Spec{target}, object.ByteOrder, object.Types.TypeID)
			require.NoError(t, err, "field layout %d", device)
		}
	}
}

func TestIOCostObjectRodataContract(t *testing.T) {
	spec := loadIOCostObjectSpec(t)
	want := ioCostProductionRodataContract()
	require.Equal(t, want, ioCostRodataContract(t, spec))

	require.NoError(t, spec.RewriteConstants(ioCostTestKernelProfile(true).constants()))
}

func TestIOCostDiagnosticObjectContract(t *testing.T) {
	spec := loadIOCostDiagnosticObjectSpec(t)
	requireIOCostPrograms(t, spec)
	requireIOCostBusinessMaps(t, spec)

	wantMaps := ioCostBusinessMapContracts()
	require.Len(t, spec.Maps, len(wantMaps)+3,
		"diagnostic object must contain seven business maps, two diagnostic maps, and .rodata")
	requireIOCostMapContract(t, spec, ioCostMapContract{
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
	requireIOCostMapContract(t, spec, ioCostMapContract{
		name: ioCostDiagnosticScratchMap, typeID: ebpf.Hash,
		keySize: ioCostUint32Size, valueSize: ioCostUint64Size,
		maxEntries: 1,
	})
	_, hasRodata := spec.Maps[".rodata"]
	require.True(t, hasRodata, "diagnostic object has no .rodata map")
}

func TestIOCostDiagnosticObjectRodataContract(t *testing.T) {
	spec := loadIOCostDiagnosticObjectSpec(t)
	want := ioCostProductionRodataContract()
	want[ioCostDiagnosticFaultMaskConstant] = 4
	want[ioCostDiagnosticMajorConstant] = 4
	want[ioCostDiagnosticFirstMinorConstant] = 4
	want[ioCostDiagnosticCSSSerialConstant] = 8
	want[ioCostDiagnosticIOCGPtrConstant] = 8
	require.Equal(t, want, ioCostRodataContract(t, spec))

	constants := ioCostTestKernelProfile(true).constants()
	constants[ioCostDiagnosticFaultMaskConstant] = ioCostDiagnosticFaultDeleteFailure
	constants[ioCostDiagnosticMajorConstant] = uint32(8)
	constants[ioCostDiagnosticFirstMinorConstant] = uint32(16)
	constants[ioCostDiagnosticCSSSerialConstant] = uint64(0x1020304050607080)
	constants[ioCostDiagnosticIOCGPtrConstant] = uint64(0x8070605040302010)
	require.NoError(t, spec.RewriteConstants(constants))
}

func TestIOCostStartSequenceAndCleanup(t *testing.T) {
	profile := ioCostSessionTestProfile()
	wantOptions := ioCostSessionTestAttachOptions()
	events := &ioCostTestEvents{}
	object := newFakeIOCostBPF(events)
	tracing := &iocostTracing{}
	object.sessionVisible = func() bool {
		tracing.mu.Lock()
		defer tracing.mu.Unlock()
		return tracing.session != nil
	}

	var loadedName string
	var loadedConstants map[string]any
	load := func(name string, constants map[string]any) (bpf.BPF, error) {
		events.add("load")
		loadedName = name
		loadedConstants = constants
		return object, nil
	}
	containerSource := func() (map[string]*pod.Container, error) {
		events.add("containers")
		return nil, errors.New("kubelet not running")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startDone := make(chan error, 1)
	go func() {
		startDone <- tracing.startWithProfile(
			ctx,
			load,
			profile,
			4,
			containerSource,
		)
	}()

	select {
	case <-object.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("Start did not publish the IOCOST session")
	}
	tracing.mu.Lock()
	session := tracing.session
	tracing.mu.Unlock()
	require.NotNil(t, session)
	require.Same(t, object, session.object)
	require.Equal(t, 4, session.possibleCPUs)
	require.Equal(t, ioCostObjectName, loadedName)
	require.Equal(t, profile.constants(), loadedConstants)
	state := object.state()
	require.Equal(t, wantOptions, state.options)
	require.Equal(t, 1, state.attachCalls)
	require.Zero(t, state.closeCalls)
	visibleAtAttach, visibleAtWait := object.publicationState()
	require.False(t, visibleAtAttach)
	require.True(t, visibleAtWait)
	require.Equal(t, []string{
		"load",
		"attach:" + ioCostWakeReturnProgram,
		"attach:" + ioCostWakeEntryProgram,
		"attach:" + ioCostPDFreeProgram,
		"attach:" + ioCostExitProgram,
		"attach:" + ioCostKickProgram,
		"wait-detach",
	}, events.snapshot())

	cancel()
	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start did not clean up the canceled IOCOST session")
	}
	tracing.mu.Lock()
	require.Nil(t, tracing.session)
	tracing.mu.Unlock()
	state = object.state()
	require.Equal(t, 1, state.closeCalls)
	require.False(t, state.readAfterClose)
	require.False(t, state.visibleAtClose)
	require.Equal(t, []string{
		"load",
		"attach:" + ioCostWakeReturnProgram,
		"attach:" + ioCostWakeEntryProgram,
		"attach:" + ioCostPDFreeProgram,
		"attach:" + ioCostExitProgram,
		"attach:" + ioCostKickProgram,
		"wait-detach",
		"object-close",
	}, events.snapshot())
}

func TestIOCostAttachFailureClosesObject(t *testing.T) {
	profile := ioCostSessionTestProfile()
	wantOptions := ioCostSessionTestAttachOptions()
	for failureAt := 1; failureAt <= len(wantOptions); failureAt++ {
		failureAt := failureAt
		t.Run(wantOptions[failureAt-1].ProgramName, func(t *testing.T) {
			attachErr := errors.New("attach failure")
			closeErr := errors.New("close failure")
			events := &ioCostTestEvents{}
			object := newFakeIOCostBPF(events)
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
				events.add("load")
				return object, nil
			}
			containerSource := func() (map[string]*pod.Container, error) {
				events.add("containers")
				return nil, nil
			}

			err := tracing.startWithProfile(
				t.Context(),
				load,
				profile,
				1,
				containerSource,
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
			require.Equal(t, 1, state.attachCalls)
			require.Equal(t, 1, state.closeCalls)
			require.False(t, state.visibleAtClose)
			visibleAtAttach, visibleAtWait := object.publicationState()
			require.False(t, visibleAtAttach)
			require.False(t, visibleAtWait)
			require.NotContains(t, events.snapshot(), "wait-detach")
		})
	}
}

func TestIOCostStartPrePublishFailures(t *testing.T) {
	loadErr := errors.New("load failure")
	tests := []struct {
		name            string
		containerSource ioControlContainerSource
		load            ioCostBPFLoader
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
			object := newFakeIOCostBPF(nil)
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

func TestIOCostBackendBreakerWithdrawsSession(t *testing.T) {
	events := &ioCostTestEvents{}
	object := newFakeIOCostBPF(events)
	object.breakOnWait = true
	tracing := &iocostTracing{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := tracing.startWithProfile(
		ctx,
		func(string, map[string]any) (bpf.BPF, error) {
			return object, nil
		},
		ioCostSessionTestProfile(),
		1,
		emptyIOCostContainerSource,
	)
	require.NoError(t, err)
	require.NoError(t, ctx.Err(), "backend breaker must stop before parent deadline")
	require.Contains(t, events.snapshot(), "wait-detach")
	require.Nil(t, tracing.session)
	state := object.state()
	require.Equal(t, 1, state.closeCalls)
	require.False(t, state.readAfterClose)
	require.False(t, state.visibleAtClose)
}

func TestIOCostSessionReadStatus(t *testing.T) {
	want := ioCostStatus{Reason: ioCostFailureWakeFrame}
	transientErr := errors.New("transient status read failure")
	tests := []struct {
		name        string
		mapID       uint32
		value       []byte
		readErr     error
		want        ioCostStatus
		wantErr     error
		wantInvalid bool
	}{
		{
			name: "valid", mapID: 1, value: encodeIOCostTestStatus(&want),
			want: want,
		},
		{name: "missing map", wantInvalid: true},
		{
			name: "short ABI", mapID: 1,
			value: make([]byte, ioCostStatusSize-1), wantInvalid: true,
		},
		{
			name: "long ABI", mapID: 1,
			value: make([]byte, ioCostStatusSize+1), wantInvalid: true,
		},
		{
			name: "ordinary I/O error", mapID: 1,
			readErr: transientErr, wantErr: transientErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeIOCostBPF(nil)
			object.statusMapID = test.mapID
			object.statusValue = test.value
			if test.readErr != nil {
				object.statusErrors = []error{test.readErr}
			}
			session := &ioCostSession{object: object}
			got, err := session.readStatus()
			switch {
			case test.wantInvalid:
				require.ErrorIs(t, err, errIOCostSessionInvalid)
			case test.wantErr != nil:
				require.ErrorIs(t, err, test.wantErr)
				require.NotErrorIs(t, err, errIOCostSessionInvalid)
			default:
				require.NoError(t, err)
				require.Equal(t, test.want, got)
			}
		})
	}
}

func TestIOCostStatusFailureReasons(t *testing.T) {
	// Map errno distinguishes capacity exhaustion from other helper failures;
	// each reported contract failure must stop tracing rather than retry it.
	tests := []struct {
		name   string
		status ioCostStatus
		reason string
	}{
		{name: "healthy"},
		{
			name:   "pending capacity exhausted",
			status: ioCostStatus{Reason: ioCostFailurePendingInsert, Errno: -int32(unix.E2BIG)},
			reason: "iocost_pending_map capacity exhausted (10240 entries)",
		},
		{
			name:   "pending allocation failure",
			status: ioCostStatus{Reason: ioCostFailurePendingInsert, Errno: -int32(unix.ENOMEM)},
			reason: "insert pending: helper returned -12",
		},
		{
			name:   "duplicate pending restoration",
			status: ioCostStatus{Reason: ioCostFailurePendingCollision},
			reason: "pending already exists while restoring an uncommitted waiter",
		},
		{
			name:   "required pending disappeared",
			status: ioCostStatus{Reason: ioCostFailurePendingDelete, Errno: -int32(unix.ENOENT)},
			reason: "pending missing or deletion failed: helper returned -2",
		},
		{
			name:   "aggregate allocation failure",
			status: ioCostStatus{Reason: ioCostFailureAggregateInsert, Errno: -int32(unix.ENOMEM)},
			reason: "aggregate creation or lookup failed: helper returned -12",
		},
		{
			name:   "identity mismatch",
			status: ioCostStatus{Reason: ioCostFailureIdentity},
			reason: "kernel identity could not be read, created or validated",
		},
		{
			name:   "wake contract",
			status: ioCostStatus{Reason: ioCostFailureWakeFrame},
			reason: "wake entry/return state or return value violates the hook contract",
		},
		{
			name:   "time rollback",
			status: ioCostStatus{Reason: ioCostFailureTimeRollback},
			reason: "wait end precedes its recorded start",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.status.failure()
			if test.reason == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorContains(t, err, test.reason)
			if test.status.Errno == -int32(unix.ENOMEM) {
				require.NotContains(t, err.Error(), "capacity exhausted")
			}
		})
	}
}

func TestIOCostCancelWaitsForInFlightStatusRead(t *testing.T) {
	object := newFakeIOCostBPF(nil)
	object.readStarted = make(chan struct{})
	releaseRead := make(chan struct{})
	object.releaseRead = releaseRead
	t.Cleanup(func() {
		select {
		case <-releaseRead:
		default:
			close(releaseRead)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tracing := &iocostTracing{}
	object.sessionVisible = func() bool {
		tracing.mu.Lock()
		defer tracing.mu.Unlock()
		return tracing.session != nil
	}
	startDone := make(chan error, 1)
	go func() {
		startDone <- tracing.startWithProfile(
			ctx,
			func(string, map[string]any) (bpf.BPF, error) {
				return object, nil
			},
			ioCostSessionTestProfile(),
			1,
			emptyIOCostContainerSource,
		)
	}()
	select {
	case <-object.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("Start did not publish the IOCOST session")
	}
	tracing.mu.Lock()
	session := tracing.session
	tracing.mu.Unlock()
	require.NotNil(t, session)

	readDone := make(chan error, 1)
	go func() {
		tracing.mu.Lock()
		_, err := session.readStatus()
		tracing.mu.Unlock()
		readDone <- err
	}()
	select {
	case <-object.readStarted:
	case <-time.After(time.Second):
		t.Fatal("status read did not enter the backend")
	}

	cancel()
	select {
	case <-session.breaker.Done():
	case <-time.After(time.Second):
		t.Fatal("session breaker did not observe cancellation")
	}
	select {
	case err := <-startDone:
		t.Fatalf("Start closed the object during an in-flight read: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	state := object.state()
	require.Zero(t, state.closeCalls)

	close(releaseRead)
	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("status read did not leave the released backend")
	}
	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start did not withdraw and close the IOCOST session")
	}
	tracing.mu.Lock()
	require.Nil(t, tracing.session)
	tracing.mu.Unlock()
	state = object.state()
	require.Equal(t, 1, state.closeCalls)
	require.False(t, state.readAfterClose)
	require.False(t, state.visibleAtClose)
}

func TestIOCostABIPackedWaitWord(t *testing.T) {
	for _, packed := range []uint64{0, 1, uint64(1) << 37, uint64(1) << 38, ^uint64(0)} {
		data := ioCostCaptureTestUint64(packed)
		counters, err := decodeIOCostWaitCounters(data, 1)
		require.NoError(t, err)
		counter := counters[0]
		require.Equal(t, packed>>38, counter.IOCount)
		require.Equal(t, packed&((1<<38)-1), counter.Wait10US)
	}
}

func TestIOCostABIGoldenBytes(t *testing.T) {
	t.Run("u64 key", func(t *testing.T) {
		data := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}
		got, err := decodeIOCostUint64(data)
		require.NoError(t, err)
		require.Equal(t, uint64(0x0102030405060708), got)
	})

	t.Run("ioc state", func(t *testing.T) {
		data := []byte{
			0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
			0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
		}
		got, err := decodeIOCostIOCState(data)
		require.NoError(t, err)
		require.Equal(t, ioCostIOCState{
			IOCID: 0x0102030405060708, Device: 0x1122334455667788,
		}, got)
	})

	t.Run("owner state", func(t *testing.T) {
		data := []byte{
			0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
			0x18, 0x17, 0x16, 0x15, 0x14, 0x13, 0x12, 0x11,
			0x28, 0x27, 0x26, 0x25, 0x24, 0x23, 0x22, 0x21,
			0x38, 0x37, 0x36, 0x35, 0x34, 0x33, 0x32, 0x31,
		}
		got, err := decodeIOCostOwnerState(data)
		require.NoError(t, err)
		require.Equal(t, ioCostOwnerState{
			IOCPtr: 0x0102030405060708, IOCID: 0x1112131415161718,
			CSS: 0x2122232425262728, CSSSerial: 0x3132333435363738,
		}, got)
	})

	t.Run("pending", func(t *testing.T) {
		data := []byte{
			0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
			0x44, 0x33, 0x22, 0x11,
			0x00, 0x00, 0x00, 0x00,
		}
		got, err := decodeIOCostPending(data)
		require.NoError(t, err)
		require.Equal(t, ioCostPending{
			StartNS:   0x0102030405060708,
			Operation: 0x11223344,
		}, got)
	})

	t.Run("wait key", func(t *testing.T) {
		data := []byte{
			0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
			0x18, 0x17, 0x16, 0x15, 0x14, 0x13, 0x12, 0x11,
			0x44, 0x33, 0x22, 0x11,
			0x00, 0x00, 0x00, 0x00,
		}
		got, err := decodeIOCostWaitKey(data)
		require.NoError(t, err)
		require.Equal(t, ioCostWaitKey{
			IOCID: 0x0102030405060708, CSSSerial: 0x1112131415161718,
			Operation: 0x11223344,
		}, got)
	})

	t.Run("wait counter", func(t *testing.T) {
		data := []byte{
			0x03, 0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00,
		}
		got, err := decodeIOCostWaitCounters(data, 1)
		require.NoError(t, err)
		require.Equal(t, []ioCostCumulative{{
			IOCount: 2, Wait10US: 3,
		}}, got)
	})

	t.Run("wake frame", func(t *testing.T) {
		data := []byte{
			0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		}
		got, err := decodeIOCostWakeFrame(data)
		require.NoError(t, err)
		require.Equal(t, ioCostWakeFrame{
			BioPtr: 1, IOCGPtr: 2, EndNS: 3,
			Pending: ioCostPending{StartNS: 4, Operation: 1},
		}, got)
	})

	t.Run("status", func(t *testing.T) {
		data := []byte{
			0xf9, 0xff, 0xff, 0xff, 0x03, 0x00, 0x00, 0x00,
		}
		got, err := decodeIOCostStatus(data)
		require.NoError(t, err)
		require.Equal(t, ioCostStatus{
			Reason: ioCostFailurePendingInsert, Errno: -7,
		}, got)
	})
}

func TestIOCostABIRejectsNonExactSizes(t *testing.T) {
	tests := []struct {
		name   string
		size   int
		decode func([]byte) error
	}{
		{name: "u64", size: ioCostUint64Size, decode: func(data []byte) error {
			_, err := decodeIOCostUint64(data)
			return err
		}},
		{name: "ioc state", size: ioCostIOCStateSize, decode: func(data []byte) error {
			_, err := decodeIOCostIOCState(data)
			return err
		}},
		{name: "owner state", size: ioCostOwnerStateSize, decode: func(data []byte) error {
			_, err := decodeIOCostOwnerState(data)
			return err
		}},
		{name: "pending", size: ioCostPendingSize, decode: func(data []byte) error {
			_, err := decodeIOCostPending(data)
			return err
		}},
		{name: "wait key", size: ioCostWaitKeySize, decode: func(data []byte) error {
			_, err := decodeIOCostWaitKey(data)
			return err
		}},
		{name: "wait counter", size: ioCostWaitCounterSize, decode: func(data []byte) error {
			_, err := decodeIOCostWaitCounters(data, 1)
			return err
		}},
		{name: "wake frame", size: ioCostWakeFrameSize, decode: func(data []byte) error {
			_, err := decodeIOCostWakeFrame(data)
			return err
		}},
		{name: "status", size: ioCostStatusSize, decode: func(data []byte) error {
			_, err := decodeIOCostStatus(data)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, test.decode(make([]byte, test.size-1)), "short data")
			require.Error(t, test.decode(make([]byte, test.size+1)), "long data")
		})
	}
}

func TestIOCostABIRejectsNonzeroReservedFields(t *testing.T) {
	pending := make([]byte, ioCostPendingSize)
	pending[12] = 1
	_, err := decodeIOCostPending(pending)
	require.ErrorContains(t, err, "reserved")

	waitKey := make([]byte, ioCostWaitKeySize)
	waitKey[20] = 1
	_, err = decodeIOCostWaitKey(waitKey)
	require.ErrorContains(t, err, "reserved")

	frames := make([]byte, 2*ioCostWakeFrameSize)
	frames[ioCostWakeFrameSize+36] = 1
	_, err = decodeIOCostWakeFrames(frames, 2)
	require.ErrorContains(t, err, "CPU 1")
	require.ErrorContains(t, err, "reserved")
}

func TestIOCostABIPerCPUDecoders(t *testing.T) {
	waitData := []byte{
		0x03, 0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00,
		0x07, 0x00, 0x00, 0x00, 0x80, 0x01, 0x00, 0x00,
	}
	waits, err := decodeIOCostWaitCounters(waitData, 2)
	require.NoError(t, err)
	require.Equal(t, []ioCostCumulative{
		{IOCount: 2, Wait10US: 3},
		{IOCount: 6, Wait10US: 7},
	}, waits)

	frameData := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	frames, err := decodeIOCostWakeFrames(frameData, 2)
	require.NoError(t, err)
	require.Equal(t, []ioCostWakeFrame{
		{},
		{BioPtr: 1, IOCGPtr: 2, EndNS: 3, Pending: ioCostPending{StartNS: 4, Operation: 1}},
	}, frames)
}

func TestIOCostABIPerCPURejectsLengthAndOverflow(t *testing.T) {
	tests := []struct {
		name   string
		size   int
		decode func([]byte) error
	}{
		{name: "wait counters", size: 2 * ioCostWaitCounterSize, decode: func(data []byte) error {
			_, err := decodeIOCostWaitCounters(data, 2)
			return err
		}},
		{name: "wake frames", size: 2 * ioCostWakeFrameSize, decode: func(data []byte) error {
			_, err := decodeIOCostWakeFrames(data, 2)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, test.decode(make([]byte, test.size-1)), "short data")
			require.Error(t, test.decode(make([]byte, test.size+1)), "long data")
		})
	}

	_, err := decodeIOCostWaitCounters(nil, 0)
	require.ErrorContains(t, err, "invalid possible CPU count")
	_, err = decodeIOCostWaitCounters(nil, -1)
	require.ErrorContains(t, err, "invalid possible CPU count")

	maxInt := int(^uint(0) >> 1)
	_, err = ioCostPerCPUDataSize(2, maxInt)
	require.ErrorContains(t, err, "overflows int")

	if uint64(^uint(0)) > ioCostMaxPossibleCPUs {
		limit := ioCostMaxPossibleCPUs
		_, err = ioCostPerCPUDataSize(int(limit+1), ioCostUint64Size)
		require.ErrorContains(t, err, "cannot be encoded")
	}
}

func TestIOCostABIProfileConstants(t *testing.T) {
	for _, optional := range []bool{false, true} {
		profile := ioCostTestKernelProfile(optional)
		budget := symbol.KsymbolRange{}
		if optional {
			budget = symbol.KsymbolRange{Start: 0x3000, End: 0x3100}
		}
		require.Equal(t, map[string]any{
			ioCostWakeAddressConstant:           uint64(0x1000),
			ioCostThrottleCallerStartConstant:   uint64(0x2000),
			ioCostThrottleCallerEndConstant:     uint64(0x2100),
			ioCostOverBudgetCallerStartConstant: budget.Start,
			ioCostOverBudgetCallerEndConstant:   budget.End,
		}, profile.constants())
	}
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

func TestIOCostDiagnosticStatusRejectsNonExactLength(t *testing.T) {
	_, err := decodeIOCostDiagnosticStatus(
		make([]byte, ioCostDiagnosticStatusSize-1))
	require.Error(t, err, "short diagnostic status")
	_, err = decodeIOCostDiagnosticStatus(
		make([]byte, ioCostDiagnosticStatusSize+1))
	require.Error(t, err, "long diagnostic status")
}

// The source checks below are review guards for the C transaction shape. They
// cannot prove that a target kernel preserves the calls, that the verifier
// accepts the object, or that every probe executes at runtime.

type ioCostMapContract struct {
	name       string
	typeID     ebpf.MapType
	keySize    uint32
	valueSize  uint32
	maxEntries uint32
	flags      uint32
}

func ioCostBusinessMapContracts() []ioCostMapContract {
	return []ioCostMapContract{
		{
			name: ioCostIDSequenceMap, typeID: ebpf.PerCPUArray,
			keySize: ioCostUint32Size, valueSize: ioCostUint64Size,
			maxEntries: ioCostIDSequenceMapMaxEntries,
		},
		{
			name: ioCostIOCStateMap, typeID: ebpf.Hash,
			keySize: ioCostUint64Size, valueSize: ioCostIOCStateSize,
			maxEntries: ioCostIOCStateMapMaxEntries,
			flags:      unix.BPF_F_NO_PREALLOC,
		},
		{
			name: ioCostOwnerStateMap, typeID: ebpf.Hash,
			keySize: ioCostUint64Size, valueSize: ioCostOwnerStateSize,
			maxEntries: ioCostOwnerStateMapMaxEntries,
			flags:      unix.BPF_F_NO_PREALLOC,
		},
		{
			name: ioCostPendingMap, typeID: ebpf.Hash,
			keySize: ioCostUint64Size, valueSize: ioCostPendingSize,
			maxEntries: ioCostPendingMapMaxEntries,
		},
		{
			name: ioCostWaitAggregateMap, typeID: ebpf.PerCPUHash,
			keySize: ioCostWaitKeySize, valueSize: ioCostWaitCounterSize,
			maxEntries: ioCostWaitAggregateMapMaxEntries,
			flags:      unix.BPF_F_NO_PREALLOC,
		},
		{
			name: ioCostWakeFrameMap, typeID: ebpf.PerCPUArray,
			keySize: ioCostUint32Size, valueSize: ioCostWakeFrameSize,
			maxEntries: ioCostWakeFrameMapMaxEntries,
		},
		{
			name: ioCostStatusMap, typeID: ebpf.Array,
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
		ioCostExitProgram,
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
		requireIOCostMapContract(t, spec, contract)
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

func requireIOCostMapContract(
	t *testing.T,
	spec *ebpf.CollectionSpec,
	want ioCostMapContract,
) {
	t.Helper()
	actual, ok := spec.Maps[want.name]
	require.True(t, ok, "map %s", want.name)
	require.Equal(t, want.typeID, actual.Type, "map %s type", want.name)
	require.Equal(t, want.keySize, actual.KeySize, "map %s key size", want.name)
	require.Equal(t, want.valueSize, actual.ValueSize, "map %s value size", want.name)
	require.Equal(t, want.maxEntries, actual.MaxEntries,
		"map %s capacity", want.name)
	require.Equal(t, want.flags, actual.Flags, "map %s flags", want.name)
}

func ioCostProductionRodataContract() map[string]uint32 {
	return map[string]uint32{
		ioCostWakeAddressConstant:           8,
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
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	object := filepath.Join(filepath.Dir(file), "..", "..", "bpf", ioCostObjectName)
	spec, err := ebpf.LoadCollectionSpec(object)
	require.NoError(t, err)
	return spec
}

func loadIOCostDiagnosticObjectSpec(t *testing.T) *ebpf.CollectionSpec {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	object := filepath.Join(filepath.Dir(file), "..", "..", "_output",
		"test-bpf", ioCostDiagnosticObjectName)
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
		wakeAddress:     0x1000,
		throttleRange:   symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
		overBudgetRange: overBudget,
	}
	return profile
}

func ioCostCSource(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(file), "..", "..", "bpf", "iocost_tracing.c")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func ioCostDiagnosticCSource(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(file), "..", "..", "bpf", "iocost_tracing_test.c")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func ioCostSourceSection(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	require.GreaterOrEqual(t, start, 0, "missing source marker %q", startMarker)
	endRelative := strings.Index(source[start+len(startMarker):], endMarker)
	require.GreaterOrEqual(t, endRelative, 0, "missing source marker %q", endMarker)
	end := start + len(startMarker) + endRelative
	require.Greater(t, end, start)
	return source[start:end]
}
