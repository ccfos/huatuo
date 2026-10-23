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

// IOLatency BPF accounting and compatibility tests share native and BTF fixtures.
package collector

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/netutil"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/btf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const ioLatencyGroupMap = "io_latency_group_map"

// Compatibility: raw tracepoints and required hooks.

// Tracepoint and state-map ABI fixtures.

func TestIOLatencyStateMapABI(t *testing.T) {
	object := filepath.Join(
		"..", "..", "bpf", "iolatency_tracing.o",
	)
	spec, err := ebpf.LoadCollectionSpec(object)
	if err != nil {
		t.Fatalf("load iolatency BPF object: %v", err)
	}
	if err := spec.RewriteConstants(map[string]any{
		ioLatencyRootBlkcg:    uint64(0xffffffff82003000),
		ioLatencyBioTraceFlag: uint32(6),
	}); err != nil {
		t.Fatalf("rewrite root blkcg address: %v", err)
	}
	for _, want := range []ioControlTestMapContract{
		{
			name: ioControlStatusMap, typeID: ebpf.Array,
			keySize: 4, valueSize: 16, maxEntries: 1,
		},
		{
			name: ioLatencyGroupMap, typeID: ebpf.Hash,
			keySize: 8, valueSize: 8, maxEntries: 2048,
		},
		{
			name: "blkdisk_lat_map", typeID: ebpf.Hash,
			keySize: 8, valueSize: 1024, maxEntries: 128,
			flags: unix.BPF_F_NO_PREALLOC,
		},
		{
			name: ioLatencyProbeResultMap, typeID: ebpf.Hash,
			keySize: 32, valueSize: 24, maxEntries: 1,
		},
		{
			name: "blkcg_lat_map", typeID: ebpf.Hash,
			keySize: 32, valueSize: 504, maxEntries: 2048*128*2 + 2048,
			flags: unix.BPF_F_NO_PREALLOC,
		},
	} {
		requireIOControlTestMap(t, spec, want)
	}
	for _, program := range []string{
		"trace_bio_queue",
		"trace_bio_remap",
		"trace_request_remap",
		"trace_request_complete",
		"io_latency_css_released",
		"io_control_device_removed",
		"io_control_device_changed",
		"io_control_disk_added",
	} {
		if _, exists := spec.Programs[program]; !exists {
			t.Fatalf("required lifecycle program %s is missing", program)
		}
	}
}

type fakeIOLatencyAttacher struct {
	bpf.BPF
	attached  [][]string
	options   []bpf.AttachOption
	fail      map[string]error
	detached  []string
	detachErr error
}

// Full layout bounds belong to kernel qualification, not startup selection.
var ioLatencyTestLayouts = []struct {
	symbol          string
	constant        string
	arguments       uint32
	legacyArguments uint32
}{
	{"block_rq_complete", "", 3, 3},
	{"block_bio_queue", ioLatencyQueueBioArgument, 1, 2},
	{"block_bio_remap", ioLatencyRemapBioArgument, 3, 4},
	{"block_rq_remap", ioLatencyRemapRequestArgument, 3, 4},
}

func (b *fakeIOLatencyAttacher) AttachWithOptions(
	options []bpf.AttachOption,
) error {
	b.options = append(b.options, options...)
	programs := make([]string, 0, len(options))
	for _, option := range options {
		programs = append(programs, option.ProgramName)
	}
	b.attached = append(b.attached, programs)
	for _, program := range programs {
		if err := b.fail[program]; err != nil {
			return err
		}
	}
	return nil
}

func (b *fakeIOLatencyAttacher) Detach() error {
	b.detached = append(b.detached, "all")
	return b.detachErr
}

// Argument probes distinguish compatibility boundaries from errors.

func TestIOLatencyArgumentProbeObject(t *testing.T) {
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(
		"..", "..", "bpf", "iolatency_probe.o"))
	if err != nil {
		t.Fatal(err)
	}
	var lastSlot uint32
	for _, definition := range ioLatencyTestLayouts {
		lastSlot = max(lastSlot, definition.legacyArguments)
	}
	if len(spec.Maps) != 2 || len(spec.Programs) != int(lastSlot)+2 {
		t.Fatalf("probe object has %d maps and %d programs", len(spec.Maps), len(spec.Programs))
	}
	// The kernel must see one fixed context load. The remaining instructions
	// return zero, so running a probe has no event-side effects.
	for slot := uint32(0); slot <= lastSlot; slot++ {
		program := spec.Programs[fmt.Sprintf("probe_arg%d", slot)]
		if program == nil || program.Type != ebpf.RawTracepoint ||
			program.SectionName != "raw_tracepoint/block_bio_queue" {
			t.Fatalf("missing raw argument probe %d", slot)
		}
		insns := program.Instructions
		if len(insns) != 3 ||
			insns[0].OpCode != asm.LoadMem(asm.R0, asm.R1, 0, asm.DWord).OpCode ||
			insns[0].Src != asm.R1 || insns[0].Offset != int16(slot*8) ||
			insns[1].OpCode != asm.Mov.Imm(asm.R0, 0).OpCode ||
			insns[1].Dst != asm.R0 || insns[1].Constant != 0 ||
			insns[2].OpCode != asm.Return().OpCode {
			t.Fatalf("argument %d probe instructions: %v", slot, insns)
		}
	}
}

func TestIOLatencyArgumentProbeErrorsAndCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		attachErr error
		detachErr error
		available bool
		wantErr   error
	}{
		{name: "valid slot", available: true},
		{name: "out of bounds", attachErr: fmt.Errorf("raw attach: %w", unix.EINVAL)},
		{name: "permission", attachErr: unix.EPERM, wantErr: unix.EPERM},
		{name: "resources", attachErr: unix.ENOMEM, wantErr: unix.ENOMEM},
		{name: "missing event", attachErr: unix.ENOENT, wantErr: unix.ENOENT},
		{name: "detach failure", detachErr: unix.EINVAL, wantErr: unix.EINVAL},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &fakeIOLatencyAttacher{
				fail:      map[string]error{"probe_arg1": test.attachErr},
				detachErr: test.detachErr,
			}
			available, err := probeIOLatencyRawArgument(object, "block_bio_queue", 1)
			if available != test.available || !errors.Is(err, test.wantErr) {
				t.Fatalf("probe = (%t, %v), want (%t, %v)", available, err, test.available, test.wantErr)
			}
			if test.attachErr == nil && !slices.Equal(object.detached, []string{"all"}) {
				t.Fatalf("attached probe was not released: %v", object.detached)
			}
		})
	}
}

// Known raw layouts and required hook coverage.

func TestIOLatencyTracepointABI(t *testing.T) {
	// A backport may change any single event independently of its neighbors.
	for _, changed := range ioLatencyTestLayouts {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy-%t", changed.symbol, legacy), func(t *testing.T) {
				counts := make(map[string]uint32)
				for _, definition := range ioLatencyTestLayouts {
					count := definition.arguments
					if legacy != (definition.symbol == changed.symbol) {
						count = definition.legacyArguments
					}
					counts[definition.symbol] = count
				}
				constants, err := ioLatencyTracepointArguments(
					func(symbol string, argument uint32) (bool, error) {
						return argument < counts[symbol], nil
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				for _, definition := range ioLatencyTestLayouts {
					var want uint32
					if counts[definition.symbol] != definition.arguments {
						want = 1
					}
					assertIOLatencyConstant(t, constants, definition.constant, want)
				}
			})
		}
	}
}

// Operational failures preserve their error and abort the whole profile;
// none of them may be mistaken for an argument-count boundary.
func TestIOLatencyTracepointProbeErrorsDoNotSelectArguments(t *testing.T) {
	for _, definition := range ioLatencyTracepoints {
		if definition.constant == "" {
			continue
		}
		for _, failure := range []error{
			unix.EPERM, unix.ENOMEM, unix.ENOENT,
		} {
			t.Run(fmt.Sprintf("%s/%v", definition.symbol, failure), func(t *testing.T) {
				constants, err := ioLatencyTracepointArguments(
					func(symbol string, argument uint32) (bool, error) {
						if symbol == definition.symbol {
							return false, failure
						}
						return false, nil
					},
				)
				if !errors.Is(err, failure) || errors.Is(err, types.ErrTracingStopped) ||
					constants != nil || !strings.Contains(err.Error(), definition.symbol) {
					t.Fatalf("failed probe: constants=%v, err=%v", constants, err)
				}
			})
		}
	}
}

func TestAttachIOLatencyHooksRequiresEveryPath(t *testing.T) {
	var programs []string
	hooks, err := ioControlDeviceHooks()
	require.NoError(t, err)
	for _, hook := range hooks {
		programs = append(programs, hook.ProgramName)
	}
	programs = append(programs,
		"probe_queue_stats", "io_latency_css_released",
	)
	for _, definition := range ioLatencyTracepoints {
		programs = append(programs, definition.program)
	}
	object := &fakeIOLatencyAttacher{}
	require.NoError(t, attachIOLatencyHooks(object))
	require.Equal(t, [][]string{programs}, object.attached)
	for _, program := range programs {
		t.Run(program, func(t *testing.T) {
			failure := errors.New("attach failed")
			object := &fakeIOLatencyAttacher{fail: map[string]error{program: failure}}
			require.ErrorIs(t, attachIOLatencyHooks(object), failure)
		})
	}
}

func assertIOLatencyConstant(
	t *testing.T,
	constants map[string]any,
	name string,
	want uint32,
) {
	t.Helper()
	if name == "" {
		return
	}
	got, ok := constants[name].(uint32)
	if !ok || got != want {
		t.Fatalf("constant %s = %#v, want %d", name, constants[name], want)
	}
}

// Request timestamps, remaps, completion ranges, and container lifetime.

func TestIOLatencyRemapAccounting(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "bpf", "iolatency_tracing.c"))
	require.NoError(t, err)
	section := func(source []byte, start, end string) string {
		return ioControlTestSourceSection(t, string(source), start, end)
	}
	program := `
#include <assert.h>
#include <errno.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
typedef uint64_t u64;
typedef uint64_t __u64;
typedef uint32_t u32;
typedef uint16_t u16;
typedef uint8_t u8;
typedef int64_t s64;
#include "abi/iolatency_types.h"
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define __noinline __attribute__((noinline))
#define SEC(name)
#define CORE_READ1(object, field) ({ \
    __auto_type source = (object); \
    source ? source->field : 0; \
})
#define CORE_READ2(object, first, second) CORE_READ1(CORE_READ1(object, first), second)
#define CORE_SELECT(_1, _2, _3, name, ...) name
#define BPF_CORE_READ(...) CORE_SELECT(__VA_ARGS__, CORE_READ2, CORE_READ1)(__VA_ARGS__)
#define bpf_core_field_exists(field) _Generic(&(field), \
    struct bio_issue *: bio_has_bi_issue, u64 *: bio_has_issue_time_ns, \
    u8 *: bio_has_partno, u16 *: request_has_stats_sectors, default: request_has_disk)
#define __builtin_preserve_type_info(value, kind) sizeof(value)
#define bio___5_12 bio
#define bio___7_0 bio
#define block_device___5_12 block_device
#define request___iolatency request
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define COMPAT_BPF_ANY 0
#define COMPAT_BPF_NOEXIST 1
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
#define REQ_OP_MASK 255
struct hd_struct { struct { u32 devt; } __dev; };
struct disk_part_tbl { struct hd_struct *part[4]; };
struct gendisk {
    u32 major, first_minor;
    struct disk_part_tbl *part_tbl;
};
struct block_device { struct gendisk *bd_disk; u32 bd_dev; };
struct cgroup_subsys_state { u64 serial_nr; };
struct blkcg { struct cgroup_subsys_state css; };
struct blkcg_gq { struct blkcg *blkcg; };
static struct blkcg test_groups[4] = {
    { .css.serial_nr = 0 }, { .css.serial_nr = 1 },
    { .css.serial_nr = 2 }, { .css.serial_nr = 3 },
};
struct bio_issue { u64 value; };
struct bio {
    struct bio *bi_next;
    struct gendisk *bi_disk;
#ifdef IO_LATENCY_TEST_BIO_DEVICE_GAP
    u64 device_padding;
#endif
    u32 bi_opf;
    u16 bi_flags;
    struct bio_issue bi_issue;
    u64 issue_time_ns;
    struct { u64 bi_size; } bi_iter;
    u8 bi_partno;
    struct block_device *bi_bdev;
    struct blkcg_gq *bi_blkg;
};
struct request_queue { struct gendisk *disk; };
struct request {
    struct gendisk *rq_disk;
    struct request_queue *q;
    u32 cmd_flags, rq_flags;
    int tag, internal_tag;
    u32 __data_len;
    u64 __sector;
    struct bio *bio;
#ifdef IO_LATENCY_TEST_BIO_TAIL_GAP
    u64 tail_padding;
#endif
    struct bio *biotail;
    u16 stats_sectors;
    u64 start_time_ns;
#ifdef IO_LATENCY_TEST_REQUEST_TIME_GAP
    u64 time_padding;
#endif
    u64 io_start_time_ns;
};
struct request_aux { u64 alloc_time_ns; u16 stats_sectors; };
struct pt_regs { void *arg1; };
struct bpf_raw_tracepoint_args { u64 args[4]; };
static u32 block_bio_queue_bio_arg, block_bio_remap_bio_arg;
static u32 block_rq_remap_request_arg;
static u32 io_latency_bio_trace_completion = 10;
static u64 io_latency_root_blkcg;
static bool request_has_disk;
static bool request_has_stats_sectors = true;
static bool bio_has_partno = true;
#ifdef IO_LATENCY_TEST_FULL_BIO_TIME
static bool bio_has_bi_issue;
static bool bio_has_issue_time_ns = true;
#else
static bool bio_has_bi_issue = true;
static bool bio_has_issue_time_ns;
#endif
struct io_control_status { u64 failure, devices_changed; };
static struct io_control_status status_value;
static struct io_control_status *io_control_status_value(void) { return &status_value; }
static u64 clock_ns;
static long delete_error, update_error;
static bool zero_extend_errno;
static void *update_error_map;
static int blkdisk_lat_map, blkcg_lat_map, io_latency_group_map;
static const void *read_error_source;
static const void *queue_snapshot_source;
static unsigned queue_snapshots;
static int bpf_core_read(void *destination, size_t size, const void *source)
{
    if (source == read_error_source)
        return -EFAULT;
    if (source == queue_snapshot_source)
        queue_snapshots++;
    memcpy(destination, source, size);
    return 0;
}
static u64 bpf_ktime_get_ns(void) { return clock_ns; }
static struct gendisk *bio_disk(struct bio *bio) { return bio->bi_disk; }
` + section(data, "#define IO_SIZE_KIB", "#ifndef REQ_OP_MASK") +
		section(data, "struct io_latency_group_state {", "\nstruct {\n") +
		section(
			data,
			"static const struct iolatency_counters zero_latency_counters",
			"static __noinline void io_latency_fail",
		) + `
static struct iolatency_host_counters host[256];
static u64 host_keys[256];
static bool host_available[256];
static struct io_latency_group_state groups[4];
static bool group_present[4];
static struct counter_entry {
    bool present;
    struct iolatency_container_key key;
    struct iolatency_counters counters;
} counter_rows[128];
static struct iolatency_counters absent_counters;

static unsigned group_slot(u64 address)
{
    for (unsigned i = 1; i < 4; i++)
        if ((u64)&test_groups[i].css == address)
            return i;
    abort();
}

static struct counter_entry *counter_entry(const struct iolatency_container_key *key)
{
    for (unsigned i = 0; i < 128; i++)
        if (counter_rows[i].present &&
            !memcmp(&counter_rows[i].key, key, sizeof(*key)))
            return &counter_rows[i];
    return NULL;
}

static struct iolatency_counters *container_sample(unsigned group, unsigned operation, unsigned major)
{
    struct iolatency_container_key key = {
        .blkcg = (u64)&test_groups[group].css,
        .serial = test_groups[group].css.serial_nr,
        .major = major, .minor = host[major].minor, .operation = operation,
    };
    struct counter_entry *entry = counter_entry(&key);
    return entry ? &entry->counters : &absent_counters;
}

static void register_host(struct gendisk *disk)
{
    assert(disk->major < 256);
    host_keys[disk->major] = (u64)disk;
    host[disk->major].major = disk->major;
    host[disk->major].minor = disk->first_minor;
    host_available[disk->major] = true;
}

static void *bpf_map_lookup_elem(void *map, const void *key)
{
    if (map == update_error_map)
        return NULL;
    if (map == &blkdisk_lat_map) {
        for (u32 disk = 0; disk < 256; disk++)
            if (host_available[disk] && host_keys[disk] == *(const u64 *)key)
                return &host[disk];
        return NULL;
    }
    if (map == &blkcg_lat_map) {
        struct counter_entry *entry = counter_entry(key);
        return entry ? &entry->counters : NULL;
    }
    assert(map == &io_latency_group_map);
    unsigned slot = group_slot(*(const u64 *)key);
    return group_present[slot] ? &groups[slot] : NULL;
}

/* Direct map calls return int; the 64-bit helper register can expose either
 * zero-extended or sign-extended errno. Exercise both at the helper boundary.
 */
static long map_result(int error)
{
    return zero_extend_errno ? (long)(u32)error : (long)error;
}

static long bpf_map_update_elem(void *map, const void *key,
                                const void *value, u64 flags)
{
    if (map == update_error_map)
        return map_result(update_error);
    if (map == &blkdisk_lat_map) {
        for (u32 disk = 0; disk < 256; disk++) {
            if (host_keys[disk] == *(const u64 *)key) {
                host_available[disk] = true;
                host[disk] = *(const struct iolatency_host_counters *)value;
                return 0;
            }
        }
        return map_result(-ENOENT);
    }
    if (map == &io_latency_group_map) {
        unsigned slot = group_slot(*(const u64 *)key);
        if (group_present[slot] && flags == COMPAT_BPF_NOEXIST)
            return map_result(-EEXIST);
        groups[slot] = *(const struct io_latency_group_state *)value;
        group_present[slot] = true;
        return 0;
    }
    if (map == &blkcg_lat_map) {
        struct counter_entry *entry = counter_entry(key);
        if (entry && flags == COMPAT_BPF_NOEXIST)
            return map_result(-EEXIST);
        if (!entry) {
            for (unsigned i = 0; i < 128; i++) {
                if (!counter_rows[i].present) {
                    entry = &counter_rows[i];
                    entry->key = *(const struct iolatency_container_key *)key;
                    entry->present = true;
                    break;
                }
            }
        }
        if (!entry)
            return map_result(-E2BIG);
        entry->counters = *(const struct iolatency_counters *)value;
        return 0;
    }
    return 0;
}

static long bpf_map_delete_elem(void *map, const void *key)
{
    if (map == &blkcg_lat_map) {
        if (delete_error)
            return map_result(delete_error);
        struct counter_entry *entry = counter_entry(key);
        if (!entry)
            return map_result(-ENOENT);
        entry->present = false;
        return 0;
    }
    if (map == &io_latency_group_map) {
        unsigned slot = group_slot(*(const u64 *)key);
        if (delete_error)
            return map_result(delete_error);
        if (!group_present[slot])
            return map_result(-ENOENT);
        group_present[slot] = false;
        return 0;
    }
    return map_result(-ENOENT);
}
` + section(data, "static __noinline void io_latency_fail", "\n}\n") + "\n}\n" +
		section(data, "static __always_inline u32 io_histogram_above", "static __noinline void queue_bio_latency") +
		section(data, "static __noinline void queue_bio_latency", "SEC(\"raw_tracepoint/block_rq_complete\")") +
		section(data, "SEC(\"raw_tracepoint/block_rq_complete\")", "\n}\n") + "\n}\n" + `
static void set_queue_time(struct bio *bio, u64 timestamp)
{
    bio->bi_issue.value = ((u64)8 << 51) | (timestamp & ((1ULL << 51) - 1));
    bio->issue_time_ns = timestamp;
}

/* Supply the timestamp as kernel input, independently of the Q probe. */
static void submit_bio(struct bpf_raw_tracepoint_args *event)
{
    set_queue_time((struct bio *)event->args[block_bio_queue_bio_arg], clock_ns);
    trace_bio_queue(event);
}

static void finish_request(struct request *req, u32 bytes)
{
    struct bpf_raw_tracepoint_args event = { .args = { (u64)req, 0, bytes } };
    trace_request_complete(&event);
}

static void test_bucket_boundaries(bool requests)
{
    struct iolatency_counters host_counters = {}, expected = {};
    struct gendisk disk = { .major = 8, .first_minor = 16 };
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio second = { .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group };
    struct bio first = { .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
                         .bi_next = &second };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = { .bio = &first, .biotail = &second, .rq_disk = &disk,
                           .q = (struct request_queue *)&queue, .__data_len = 8192,
                           .start_time_ns = 1, .io_start_time_ns = 1 };
    struct bpf_raw_tracepoint_args event = {};
    if (requests)
        register_host(&disk);
    const u64 thresholds[] = {
        50000, 100000, 250000, 500000, 1000000, 2000000, 4000000,
        8000000, 16000000, 32000000, 64000000, 128000000, 256000000,
        512000000, 1000000000, 2000000000,
    };
    /* Check both bucket selection and published counters on either side of
     * every threshold, including the fast first bucket and arithmetic path.
     */
    for (int i = 0; i < 16; i++) {
        for (int offset = -1; offset <= 1; offset++) {
            u64 end_ns = 1 + thresholds[i] + offset;
            int bucket = i + (offset > 0);
            assert(io_latency_bucket(1, end_ns, false) == bucket);
            account_io_latency(&host_counters,
                               IO_LATENCY_STAGE_Q2D, 1, end_ns);
            expected.buckets[IO_LATENCY_STAGE_Q2D][bucket]++;
            if (requests) {
                /* The production C entry classifies D2C once for both bios. */
                clock_ns = 2;
                event.args[block_bio_queue_bio_arg] = (u64)&first;
                submit_bio(&event);
                event.args[block_bio_queue_bio_arg] = (u64)&second;
                submit_bio(&event);
                clock_ns = end_ns;
                finish_request(&req, req.__data_len);
                for (int b = 0; b < IO_LATENCY_BUCKETS; b++) {
                    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][b] ==
                        2 * expected.buckets[IO_LATENCY_STAGE_Q2D][b]);
                    assert((*container_sample(1, 0, 8)).buckets[IO_LATENCY_STAGE_D2C][b] ==
                        2 * expected.buckets[IO_LATENCY_STAGE_Q2D][b]);
                }
                assert(!status_value.failure);
            }
        }
    }
    assert(io_latency_bucket(0, 100, false) == -1);
    assert(io_latency_bucket(100, 100, false) == 0);
    assert(io_latency_bucket(100, 99, false) == -1);
    assert(io_latency_bucket(1, UINT64_MAX, false) == 16);
    account_io_latency(&host_counters, IO_LATENCY_STAGE_Q2D, 0, 100);
    account_io_latency(&host_counters, IO_LATENCY_STAGE_Q2D, 100, 0);
    account_io_latency(&host_counters, IO_LATENCY_STAGE_Q2D, 100, 100);
    account_io_latency(&host_counters, IO_LATENCY_STAGE_Q2D, 100, 99);
    account_io_latency(&host_counters, IO_LATENCY_STAGE_Q2D, 1, UINT64_MAX);
    expected.buckets[IO_LATENCY_STAGE_Q2D][0] += 2;
    expected.buckets[IO_LATENCY_STAGE_Q2D][16]++;
    assert(!memcmp(&host_counters, &expected, sizeof(expected)));
    const u32 sizes[] = { 4096, 16384, 65536, 262144, 1048576 };
    for (int i = 0; i < 5; i++) {
        assert(io_size_bucket(sizes[i]) == i);
        assert(io_size_bucket(sizes[i] + 1) == i + 1);
    }
    assert(io_size_bucket(UINT32_MAX) == 5);
}

static void test_queue_time(void)
{
    const u64 period = 1ULL << 51, mask = period - 1;
    struct bio io = {};
    bio_has_bi_issue = true;
    bio_has_issue_time_ns = false;
    const struct { u64 stamp, now, want; } packed[] = {
        { 100, 200, 100 },
        { ~mask | 100, 200, 100 },
        { period - 100, period + 200, period - 100 },
        { ~mask | 100, 3 * period + 200, 3 * period + 100 },
        { 0, period + 200, 0 },
        { ~mask, period + 200, 0 },
        { 300, 200, 0 },
    };
    for (u32 i = 0; i < sizeof(packed) / sizeof(packed[0]); i++) {
        io.bi_issue.value = packed[i].stamp;
        assert(io_latency_bio_queue_ns((u64)&io, packed[i].now) == packed[i].want);
    }
    read_error_source = &io.bi_issue;
    assert(!io_latency_bio_queue_ns((u64)&io, period + 200));
    read_error_source = NULL;

    bio_has_bi_issue = false;
    bio_has_issue_time_ns = true;
    io.issue_time_ns = 3 * period + 100;
    assert(io_latency_bio_queue_ns((u64)&io, 3 * period + 200) == io.issue_time_ns);
    assert(!io_latency_bio_queue_ns((u64)&io, 3 * period + 99));
    read_error_source = &io.issue_time_ns;
    assert(!io_latency_bio_queue_ns((u64)&io, 3 * period + 200));
    read_error_source = NULL;
    io.issue_time_ns = 0;
    assert(!io_latency_bio_queue_ns((u64)&io, 3 * period + 200));
    io.issue_time_ns = 100;
    bio_has_issue_time_ns = false;
    assert(!io_latency_bio_queue_ns((u64)&io, 200));
}

static void test_queue_layout(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct bio io = { .bi_disk = &disk, .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 4096 };
    struct bpf_raw_tracepoint_args event = { .args = { (u64)&io } };
    queue_snapshot_source = &io.bi_disk;
    trace_bio_queue(&event);
#ifdef IO_LATENCY_TEST_BIO_DEVICE_GAP
    assert(queue_snapshots == 0);
#else
    assert(queue_snapshots == 1);
#endif
    assert(host[8].counters[1].queued_size[0] == 1);
    assert(host[8].counters[0].queued_size[0] == 0 && !status_value.failure);
}

static void test_missing_queue(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio io = { .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1010000000, .io_start_time_ns = 1020000000,
    };
    clock_ns = 1030000000;
    /* Missing fields, zero timestamps and failed reads omit only Q stages. */
    for (u32 mode = 0; mode < 5; mode++) {
        bio_has_bi_issue = mode < 2;
        bio_has_issue_time_ns = mode >= 2 && mode < 4;
        set_queue_time(&io, mode == 0 || mode == 2 ? 0 : 1000000000);
        read_error_source = mode == 1 ? (void *)&io.bi_issue :
                            mode == 3 ? (void *)&io.issue_time_ns : NULL;
        finish_request(&req, 4096);
        for (u32 bucket = 0; bucket < IO_LATENCY_BUCKETS; bucket++)
            assert(!host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][bucket] &&
                !host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][bucket]);
        assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][8] == mode + 1);
        assert(host[8].counters[0].issued_size[0] == mode + 1);
        assert(!host[8].counters[0].queued_size[0]);
        assert(!memcmp(&host[8].counters[0], container_sample(1, 0, 8), sizeof(host[8].counters[0])));
    }
    /* Starting collection after Q still permits all completion intervals. */
    read_error_source = NULL;
    bio_has_bi_issue = true;
    set_queue_time(&io, 1000000000);
    finish_request(&req, 4096);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][8] == 1 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][9] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][8] == 6 && host[8].counters[0].issued_size[0] == 6);
    assert(!host[8].counters[0].queued_size[0] && !status_value.failure);
    assert(!memcmp(&host[8].counters[0], container_sample(1, 0, 8), sizeof(host[8].counters[0])));
}

static void test_remap(void)
{
    struct gendisk upper = { .major = 9 }, lower = { .major = 8, .first_minor = 16 };
    register_host(&upper);
    register_host(&lower);
    struct blkcg_gq source_group = { .blkcg = &test_groups[1] };
    struct blkcg_gq target_group = { .blkcg = &test_groups[2] };
    struct bio io = {
        .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 4096,
        .bi_disk = &upper, .bi_blkg = &source_group,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    struct request_queue___iolatency target_queue = { .disk = &lower };
    struct request req = {
        .bio = &io, .rq_disk = &lower,
        .q = (struct request_queue *)&target_queue,
        .cmd_flags = REQ_OP_WRITE, .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 2100000000, .io_start_time_ns = 2250000000,
    };

    clock_ns = 1000000000;
    submit_bio(&event);
    io.bi_disk = &lower;
    io.bi_blkg = &target_group;
    clock_ns = 2000000000;
    /* With a clear flag, destination Q owns the queued-size observation. */
    trace_bio_remap(&event);
    assert(!host[8].counters[1].queued_size[0]);
    clock_ns = 2050000000;
    submit_bio(&event);
    assert(host[9].counters[1].queued_size[0] == 1 && host[8].counters[1].queued_size[0] == 1);
    clock_ns = 2400000000;
    finish_request(&req, req.__data_len);
    assert(!status_value.failure);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_Q2G][10] == 1);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_Q2D][12] == 1);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_D2C][12] == 1 && host[8].counters[1].issued_size[0] == 1);
    assert(!host[9].counters[1].buckets[IO_LATENCY_STAGE_Q2D][12] &&
        !host[9].counters[1].buckets[IO_LATENCY_STAGE_D2C][12] &&
        !host[9].counters[1].issued_size[0]);
    assert(!memcmp(&host[8].counters[1], container_sample(2, 1, 8), sizeof(host[8].counters[1])));

    /* An existing completion flag suppresses kernel Q, so A owns size.
     * Completion still reads the kernel timestamp, not the later A time.
     */
    io.bi_flags = 1U << io_latency_bio_trace_completion;
    set_queue_time(&io, 3000000000);
    clock_ns = 3050000000;
    trace_bio_remap(&event);
    assert(host[8].counters[1].queued_size[0] == 2);
    req.start_time_ns = 3010000000;
    req.io_start_time_ns = 3020000000;
    clock_ns = 3100000000;
    finish_request(&req, req.__data_len);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_Q2G][8] == 1 &&
        host[8].counters[1].buckets[IO_LATENCY_STAGE_Q2D][9] == 1);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_D2C][11] == 1 && host[8].counters[1].issued_size[0] == 2);

    /* Flush sequencing restores an exhausted bio for a zero-byte C. */
    struct iolatency_counters completed_host = host[8].counters[1];
    struct iolatency_counters completed_container = *container_sample(2, 1, 8);
    io.bi_iter.bi_size = req.__data_len = 0;
    clock_ns = 3200000000;
    finish_request(&req, 0);
    assert(!status_value.failure);
    assert(!memcmp(&completed_host, &host[8].counters[1], sizeof(completed_host)));
    assert(!memcmp(&completed_container, container_sample(2, 1, 8), sizeof(completed_container)));
}

/* A partition translation is excluded even when the completion flag is set. */
static void test_partition_remap(void)
{
    struct hd_struct part = { .__dev.devt = (259U << 20) | 73 };
    struct disk_part_tbl table = { .part = { [3] = &part } };
    struct gendisk disk = {
        .major = 8, .first_minor = 16, .part_tbl = &table,
    };
    register_host(&disk);
    struct block_device bdev = { .bd_disk = &disk, .bd_dev = part.__dev.devt };
    struct bio io = {
        .bi_disk = &disk, .bi_partno = 3, .bi_bdev = &bdev,
        .bi_iter.bi_size = 4096,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_remap_bio_arg] = (u64)&io;

    for (int layout = 0; layout < 2; layout++) {
        bio_has_partno = layout != 0;
        u64 queued = host[8].counters[0].queued_size[0];
        io.bi_flags = 1U << io_latency_bio_trace_completion;
        event.args[block_bio_remap_bio_arg + 1] = part.__dev.devt;
        trace_bio_remap(&event);
        assert(host[8].counters[0].queued_size[0] == queued);

        /* A DM/MD remap names the upper device, including extended dev_t. */
        event.args[block_bio_remap_bio_arg + 1] = 253U << 20;
        trace_bio_remap(&event);
        assert(host[8].counters[0].queued_size[0] == queued + 1);
        event.args[block_bio_remap_bio_arg + 1] = part.__dev.devt;
        trace_bio_remap(&event);
        assert(host[8].counters[0].queued_size[0] == queued + 1);

        io.bi_flags = 0;
        clock_ns = 2000000000;
        submit_bio(&event);
        assert(host[8].counters[0].queued_size[0] == queued + 2);
        assert(!status_value.failure);
    }
}

static void test_request_device(void)
{
    struct gendisk upper = { .major = 9 }, lower = { .major = 8, .first_minor = 16 };
    register_host(&upper);
    register_host(&lower);
    host[8].scsi = 1;
    struct request_queue___iolatency source_queue = { .disk = &upper };
    struct request_queue___iolatency target_queue = { .disk = &lower };
    struct blkcg_gq first_group = { .blkcg = &test_groups[1] };
    struct blkcg_gq second_group = { .blkcg = &test_groups[2] };
    struct bio second = {
        .bi_opf = REQ_OP_READ, .bi_iter.bi_size = 4096,
        .bi_disk = &upper, .bi_blkg = &second_group,
    };
    struct bio first = {
        .bi_opf = REQ_OP_READ, .bi_iter.bi_size = 4096,
        .bi_disk = &upper, .bi_blkg = &first_group, .bi_next = &second,
    };
    struct request req = {
        .bio = &first, .rq_disk = &upper,
        .q = (struct request_queue *)&source_queue,
        .cmd_flags = REQ_OP_WRITE, .__data_len = 8192, .stats_sectors = 32,
        .start_time_ns = 2100000000, .io_start_time_ns = 2200000000,
    };
    struct bpf_raw_tracepoint_args request_remap = {};
    request_remap.args[block_rq_remap_request_arg] = (u64)&req;
    host_available[9] = false;
    clock_ns = 2000000000;
    set_queue_time(&first, clock_ns);
    set_queue_time(&second, clock_ns);
    trace_request_remap(&request_remap);
    clock_ns = 2500000000;
    finish_request(&req, req.__data_len);
    assert(!memcmp(&host[9].counters[1], &zero_latency_counters, sizeof(zero_latency_counters)));

    /* Request A uses the dispatch disk/operation even if bios name DM.
     * Its accounting is independent of the bio completion flag.
     */
    req.rq_disk = &lower;
    req.q = (struct request_queue *)&target_queue;
    first.bi_flags = 1U << io_latency_bio_trace_completion;
    trace_request_remap(&request_remap);
    assert(host[8].counters[1].queued_size[0] == 2 && !host[8].counters[0].queued_size[0]);

    /* SCSI's final C retains the full last-D size; its first remaining
     * bio owns the size sample after earlier bios complete.
     */
    finish_request(&req, 2048);
    assert(!host[8].counters[1].buckets[IO_LATENCY_STAGE_D2C][13] && !host[8].counters[1].issued_size[1]);
    first.bi_iter.bi_size -= 2048;
    req.__data_len -= 2048;
    finish_request(&req, 2048);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_D2C][13] == 1 && !host[8].counters[1].issued_size[1]);
    req.bio = &second;
    req.__data_len -= 2048;
    clock_ns = 2700000000;
    finish_request(&req, req.__data_len);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_D2C][13] == 2 &&
        container_sample(1, 1, 8)->buckets[IO_LATENCY_STAGE_D2C][13] == 1 &&
           container_sample(2, 1, 8)->buckets[IO_LATENCY_STAGE_D2C][13] == 1);
    assert(host[8].counters[1].buckets[IO_LATENCY_STAGE_Q2G][11] == 2 &&
        host[8].counters[1].buckets[IO_LATENCY_STAGE_Q2D][12] == 2);
    assert(host[8].counters[1].issued_size[1] == 1 && !host[8].counters[0].issued_size[1]);
    assert(!container_sample(1, 1, 8)->issued_size[1] && container_sample(2, 1, 8)->issued_size[1] == 1);
    assert(!status_value.failure);
}

/* C skips a partially completed bio, then settles each complete bio once.
 * Kernel advancement between events is separate from the traced function.
 */
static void test_partial_completion(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    host[8].scsi = 1;
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq first_group = { .blkcg = &test_groups[1] };
    struct blkcg_gq second_group = { .blkcg = &test_groups[2] };
    struct bio second = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &second_group,
    };
    struct bio first = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &first_group,
        .bi_next = &second,
    };
    struct request req = {
        .bio = &first, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 8192, .stats_sectors = 16,
        .start_time_ns = 1030000000, .io_start_time_ns = 1050000000,
    };
    struct bpf_raw_tracepoint_args remap = {};
    remap.args[block_rq_remap_request_arg] = (u64)&req;
    clock_ns = 1000000000;
    set_queue_time(&first, clock_ns);
    set_queue_time(&second, clock_ns);
    trace_request_remap(&remap);

    clock_ns = 1100000000;
    finish_request(&req, 2048);
    assert(!host[8].counters[0].issued_size[1]);
    for (int i = 0; i < IO_LATENCY_BUCKETS; i++)
        assert(!host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][i] &&
            !host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][i]);

    first.bi_iter.bi_size -= 2048;
    req.__data_len -= 2048;
    clock_ns = 1120000000;
    finish_request(&req, 2048);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][10] == 1 && !host[8].counters[0].issued_size[1]);

    req.bio = &second;
    req.__data_len -= 2048;
    clock_ns = 1140000000;
    finish_request(&req, req.__data_len);
    assert(!status_value.failure);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][11] == 2);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][9] == 2 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][10] == 2);
    assert(host[8].counters[0].issued_size[1] == 1);
    assert(!(*container_sample(1, 0, 8)).issued_size[1] &&
           (*container_sample(2, 0, 8)).issued_size[1] == 1);
}

static void test_aux_request_size(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    host[8].scsi = 1;
    request_has_stats_sectors = false;
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio io = { .bi_iter.bi_size = 8192, .bi_disk = &disk, .bi_blkg = &group };
    struct {
        struct request_aux aux;
        struct request req;
    } allocation = {
        .aux.stats_sectors = 16,
        .req = {
            .bio = &io, .biotail = &io, .rq_disk = &disk,
            .q = (struct request_queue *)&queue, .__data_len = 8192,
            .stats_sectors = 8, .start_time_ns = 1030000000,
            .io_start_time_ns = 1050000000,
        },
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    clock_ns = 1000000000;
    submit_bio(&event);
    clock_ns = 1100000000;
    finish_request(&allocation.req, 4096);
    assert(!host[8].counters[0].issued_size[0] && !host[8].counters[0].issued_size[1]);
    io.bi_iter.bi_size = allocation.req.__data_len = 4096;
    clock_ns = 1140000000;
    finish_request(&allocation.req, 4096);
    assert(!status_value.failure);
    assert(host[8].counters[0].issued_size[1] == 1 && !host[8].counters[0].issued_size[0]);
    assert(container_sample(1, 0, 8)->issued_size[1] == 1);
}

/* Exercise accounting across several completion chunks and the end of a
 * 512-bio walk. Four bios have Q timestamps; every completed bio has D2C.
 * A partial C stops at its current bio; final C accounts issued size once.
 */
static void test_completion_chunks(u32 count, bool partial)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    host[8].scsi = 1;
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq first_group = { .blkcg = &test_groups[1] };
    struct blkcg_gq second_group = { .blkcg = &test_groups[2] };
    struct bio chain[IO_LATENCY_REQUEST_SCAN_BUDGET + 1] = {};
    const u32 tracked[] = { 0, 32, partial ? 33 : 480, count - 1 };
    struct request req = {
        .bio = chain, .biotail = &chain[count - 1], .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .__data_len = count * 4096, .stats_sectors = count * 8,
        .start_time_ns = 1030000000, .io_start_time_ns = 1050000000,
    };
    struct bpf_raw_tracepoint_args event = {};
    for (u32 i = 0; i < count; i++) {
        chain[i].bi_iter.bi_size = 4096;
        chain[i].bi_disk = &disk;
        if (i + 1 < count)
            chain[i].bi_next = &chain[i + 1];
    }
    clock_ns = 1000000000;
    for (u32 i = 0; i < 4; i++) {
        chain[tracked[i]].bi_blkg = i < 2 ? &first_group : &second_group;
        event.args[block_bio_queue_bio_arg] = (u64)&chain[tracked[i]];
        submit_bio(&event);
    }
    clock_ns = 1100000000;
    if (partial) {
        const u32 bytes = 33 * 4096 + 2048;
        finish_request(&req, bytes);
        assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][10] == 33);
        for (u32 bucket = 0; bucket < IO_SIZE_BUCKETS; bucket++)
            assert(!host[8].counters[0].issued_size[bucket]);
        /* Apply the kernel's byte/bio advancement between the two events. */
        req.bio = &chain[33];
        req.__data_len -= bytes;
        chain[33].bi_iter.bi_size -= 2048;
        clock_ns = 1200000000;
    }
    finish_request(&req, req.__data_len);
    if (count > IO_LATENCY_REQUEST_SCAN_BUDGET) {
        assert(status_value.failure == (u64)IO_LATENCY_CHAIN_OVERFLOW << 32);
        return;
    }
    assert(!status_value.failure);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][9] == 4 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][10] == 4);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][10] == (partial ? 33 : count));
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][12] == (partial ? count - 33 : 0));
    const u32 issued_bucket = partial ? 4 : 5;
    assert(host[8].counters[0].issued_size[issued_bucket] == 1);
    assert((*container_sample(1, 0, 8)).issued_size[issued_bucket] == !partial);
    assert((*container_sample(2, 0, 8)).issued_size[issued_bucket] == partial);
    for (u32 group = 1; group <= 2; group++) {
        assert((*container_sample(group, 0, 8)).buckets[IO_LATENCY_STAGE_Q2G][9] == 2);
        assert((*container_sample(group, 0, 8)).buckets[IO_LATENCY_STAGE_Q2D][10] == 2);
    }
}

static void test_busy_series_write(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct bpf_raw_tracepoint_args event = {};
    struct request req = {
        .bio = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1010000000, .io_start_time_ns = 1020000000,
    };
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    clock_ns = 1000000000;
    update_error_map = &blkcg_lat_map;
    update_error = -EBUSY;
    submit_bio(&event);
    assert(!status_value.failure);
    assert(host[8].counters[0].queued_size[0] == 1);
    assert(!(*container_sample(1, 0, 8)).queued_size[0]);
    update_error_map = NULL;
    clock_ns += 100000000;
    finish_request(&req, req.__data_len);
    assert(!status_value.failure);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][9] == 1 &&
        (*container_sample(1, 0, 8)).buckets[IO_LATENCY_STAGE_Q2D][9] == 1);
}

static void test_container_allocation_failure(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio io = { .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    update_error_map = &blkcg_lat_map;
    update_error = -ENOMEM;
    clock_ns = 1000000000;
    submit_bio(&event);
    clock_ns += 3;
    finish_request(&req, req.__data_len);
    assert(!status_value.failure);
    assert(host[8].counters[0].queued_size[0] == 1 && host[8].counters[0].issued_size[0] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][0] == 1 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][0] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][0] == 1);
    assert(!memcmp(&(*container_sample(1, 0, 8)), &zero_latency_counters, sizeof(zero_latency_counters)));
}

/* Wide completion sizes must survive the kernel's narrow stats_sectors. */
static void test_large_completed_request(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    const u32 sizes[] = { 32U << 20, (32U << 20) + 4096 };
    for (u32 i = 0; i < 2; i++) {
        struct bio io = { .bi_disk = &disk, .bi_iter.bi_size = sizes[i] };
        struct request req = {
            .bio = &io, .biotail = &io, .rq_disk = &disk,
            .q = (struct request_queue *)&queue, .__data_len = sizes[i],
            .stats_sectors = (u16)(sizes[i] >> 9),
            .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
        };
        struct bpf_raw_tracepoint_args event = {};
        event.args[block_bio_queue_bio_arg] = (u64)&io;
        clock_ns = 1000000000;
        submit_bio(&event);
        clock_ns += 100000000;
        finish_request(&req, req.__data_len);
        assert(host[8].counters[0].issued_size[IO_SIZE_BUCKETS - 1] == i + 1);
        for (u32 bucket = 0; bucket < IO_SIZE_BUCKETS - 1; bucket++)
            assert(!host[8].counters[0].issued_size[bucket]);
        assert(!status_value.failure);
    }
}

/* Q2G/Q2D retain cached and merged request samples in the first bucket.
 * Each stage needs only its own timestamps; only D2C rejects reversed times.
 */
static void test_request_time(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct bio io = { .bi_iter.bi_size = 4096, .bi_disk = &disk };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .rq_disk = &disk, .__data_len = 4096,
        .q = (struct request_queue *)&queue,
        .stats_sectors = 8,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    const struct { u64 get, issue, complete; } times[] = {
        { 1010000000, 1030000000, 1040000000 },
        { 0,          1030000000, 1040000000 },
        { 999000000,  1030000000, 1040000000 },
        { 1010000000, 1050000000, 1040000000 },
        { 999999492,   999999492, 1040000000 },
        { 1000000000, 1000000000, 1040000000 },
        { 1010000000,          0, 1040000000 },
        { 1000000000, 1000000000, 1000000000 },
    };
    for (u32 test = 0; test < sizeof(times) / sizeof(times[0]); test++) {
        clock_ns = 1000000000;
        submit_bio(&event);
        req.start_time_ns = times[test].get;
        req.io_start_time_ns = 1020000000;
        /* The request is issued again without a new Q. Only last D is used. */
        req.io_start_time_ns = times[test].issue;
        clock_ns = times[test].complete;
        finish_request(&req, 4096);
        assert(!status_value.failure);
    }
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][8] == 3);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][0] == 4);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][9] == 3 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][10] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][0] == 3);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][8] == 3);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][10] == 2 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][0] == 1);
    assert(host[8].counters[0].issued_size[0] == 7);
}

/* A known root CSS is skipped. Without its symbol, raw counters still
 * track its lifetime; userspace decides whether it has container labels.
 */
static void test_root_blkcg(bool known)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = &test_groups[3] };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    io_latency_root_blkcg = known ? (u64)&test_groups[3] : 0;
    clock_ns = 1000000000;
    submit_bio(&event);
    clock_ns = 1000000003;
    finish_request(&req, req.__data_len);
    assert(host[8].counters[0].queued_size[0] == 1 && host[8].counters[0].issued_size[0] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][0] == 1 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][0] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][0] == 1 && !status_value.failure);
    if (known)
        assert(!memcmp(container_sample(3, 0, 8), &zero_latency_counters,
                       sizeof(zero_latency_counters)));
    else
        assert(!memcmp(container_sample(3, 0, 8), &host[8].counters[0],
                       sizeof(host[8].counters[0])));

    /* A known root address must not bypass an ordinary container's row. */
    group.blkcg = &test_groups[1];
    clock_ns = 2000000000;
    req.start_time_ns = clock_ns + 1;
    req.io_start_time_ns = clock_ns + 2;
    submit_bio(&event);
    clock_ns += 3;
    finish_request(&req, req.__data_len);
    assert((*container_sample(1, 0, 8)).queued_size[0] == 1);
    assert((*container_sample(1, 0, 8)).issued_size[0] == 1);
    assert((*container_sample(1, 0, 8)).buckets[IO_LATENCY_STAGE_Q2G][0] == 1);
    assert((*container_sample(1, 0, 8)).buckets[IO_LATENCY_STAGE_Q2D][0] == 1 &&
        (*container_sample(1, 0, 8)).buckets[IO_LATENCY_STAGE_D2C][0] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][0] == 2 && !status_value.failure);
}

/* Only disks registered at session startup have histogram rows. */
static void test_unselected_disk(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct request req = {
        .bio = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1010000000, .io_start_time_ns = 1020000000,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    host_available[8] = false;
    clock_ns = 1000000000;
    submit_bio(&event);
    trace_bio_remap(&event);
    /* An unadmitted disk does not enter the bounded request walk. */
    struct bio chain[IO_LATENCY_REQUEST_SCAN_BUDGET + 1] = {};
    for (int i = 0; i < IO_LATENCY_REQUEST_SCAN_BUDGET; i++)
        chain[i].bi_next = &chain[i + 1];
    req.bio = chain;
    struct bpf_raw_tracepoint_args remap = {};
    remap.args[block_rq_remap_request_arg] = (u64)&req;
    trace_request_remap(&remap);
    req.bio = &io;
    clock_ns = 1100000000;
    finish_request(&req, req.__data_len);
    assert(!host_available[8] && !status_value.failure);
    assert(!memcmp(&host[8].counters[0], &zero_latency_counters, sizeof(host[8].counters[0])));
    assert(!memcmp(&(*container_sample(1, 0, 8)), &zero_latency_counters,
                   sizeof((*container_sample(1, 0, 8)))));
}

/* Partial C waits until the single bio has completed all remaining bytes. */
static void test_single_bio_completion(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    clock_ns = 1000000000;
    submit_bio(&event);
    clock_ns = 1000000003;
    finish_request(&req, 1024);
    assert(!host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][0]);

    /* Model the kernel's advancement after that partial completion. */
    io.bi_iter.bi_size -= 1024;
    req.__data_len -= 1024;
    clock_ns++;
    finish_request(&req, req.__data_len);
    assert(!status_value.failure && host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2G][0] == 1);
    assert(host[8].counters[0].buckets[IO_LATENCY_STAGE_Q2D][0] == 1 &&
        host[8].counters[0].buckets[IO_LATENCY_STAGE_D2C][0] == 1);
    assert(host[8].counters[0].issued_size[0] == 1);
    assert(!memcmp(&host[8].counters[0], &(*container_sample(1, 0, 8)), sizeof(host[8].counters[0])));

    /* Flush's final zero-byte C must not duplicate any samples. */
    struct iolatency_counters before = host[8].counters[0];
    finish_request(&req, 0);
    assert(!memcmp(&before, &host[8].counters[0], sizeof(before)));
}


static void test_current_css(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq first = { .blkcg = &test_groups[1] };
    struct blkcg_gq second = { .blkcg = &test_groups[2] };
    struct bio io = { .bi_disk = &disk, .bi_blkg = &first, .bi_iter.bi_size = 4096 };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk,
        .q = (struct request_queue *)&queue, .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    clock_ns = 1000000000;
    submit_bio(&event);
    io.bi_blkg = &second;
    clock_ns += 3;
    finish_request(&req, req.__data_len);
    assert(container_sample(1, 0, 8)->queued_size[0] == 1);
    assert(!container_sample(1, 0, 8)->buckets[IO_LATENCY_STAGE_D2C][0]);
    assert(!container_sample(2, 0, 8)->queued_size[0]);
    assert(container_sample(2, 0, 8)->buckets[IO_LATENCY_STAGE_D2C][0] == 1);
    assert(container_sample(2, 0, 8)->issued_size[0] == 1);
    assert(!status_value.failure);
}

static void test_css_release(bool publication_failure)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = &test_groups[1] };
    struct bio io = { .bi_disk = &disk, .bi_blkg = &group, .bi_iter.bi_size = 4096 };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    clock_ns = 1000000000;
    submit_bio(&event);
    struct iolatency_container_key old = {
        .blkcg = (u64)&test_groups[1].css, .serial = 1, .major = 8,
    };
    struct counter_entry *old_row = counter_entry(&old);
    assert(old_row && old_row->counters.queued_size[0] == 1);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk,
        .q = (struct request_queue *)&queue, .__data_len = 4096,
    };
    clock_ns++;
    finish_request(&req, req.__data_len);
    struct pt_regs release = { .arg1 = &test_groups[1].css };
    if (publication_failure) {
        update_error_map = &blkcg_lat_map;
        update_error = -ENOMEM;
        status_value.devices_changed = 1;
    }
    io_latency_css_released(&release);
    assert(!group_present[1] && old_row->present);
    struct iolatency_container_key marker = {
        .blkcg = old.blkcg, .serial = old.serial, .kind = IO_LATENCY_CSS_RELEASED,
    };
    if (publication_failure) {
        assert(!counter_entry(&marker));
        assert(status_value.failure == ((u64)IO_LATENCY_GROUP_RELEASE_FAILED << 32 | (u32)-ENOMEM));
        assert(status_value.devices_changed == 1);
        return;
    }
    assert(counter_entry(&marker) && !status_value.failure);
    test_groups[1].css.serial_nr = 11;
    clock_ns++;
    submit_bio(&event);
    struct iolatency_container_key next = old;
    next.serial = 11;
    assert(counter_entry(&next) && counter_entry(&next) != old_row);
    assert(old_row->counters.queued_size[0] == 1);
    assert(counter_entry(&next)->counters.queued_size[0] == 1);
    assert(counter_entry(&marker) && groups[1].serial == 11);
}

int main(int argc, char **argv)
{
    assert(argc == 5);
    zero_extend_errno = !strcmp(argv[4], "zero-extended");
    request_has_disk = !strcmp(argv[3], "rq-disk");
    block_bio_queue_bio_arg = block_bio_remap_bio_arg =
        block_rq_remap_request_arg = atoi(argv[1]);
    if (!strcmp(argv[2], "classifier-buckets")) {
        test_bucket_boundaries(false);
        return 0;
    }
    /* Request buckets run in their own process, without sharing map state. */
    if (!strcmp(argv[2], "completion-buckets")) {
        test_bucket_boundaries(true);
        return 0;
    }
    if (!strcmp(argv[2], "queue-layout"))
        test_queue_layout();
    else if (!strcmp(argv[2], "queue-time"))
        test_queue_time();
    else if (!strcmp(argv[2], "missing-queue"))
        test_missing_queue();
    else if (!strcmp(argv[2], "css-release"))
        test_css_release(false);
    else if (!strcmp(argv[2], "css-release-failure"))
        test_css_release(true);
    else if (!strcmp(argv[2], "current-css"))
        test_current_css();
    else if (!strcmp(argv[2], "request-time"))
        test_request_time();
    else if (!strcmp(argv[2], "root-blkcg"))
        test_root_blkcg(true);
    else if (!strcmp(argv[2], "root-blkcg-unknown"))
        test_root_blkcg(false);
    else if (!strcmp(argv[2], "unselected-disk"))
        test_unselected_disk();
    else if (!strcmp(argv[2], "single-completion"))
        test_single_bio_completion();
    else if (!strcmp(argv[2], "partial-completion"))
        test_partial_completion();
    else if (!strcmp(argv[2], "aux-request-size"))
        test_aux_request_size();
    else if (!strcmp(argv[2], "completion-chunks"))
        test_completion_chunks(IO_LATENCY_REQUEST_SCAN_BUDGET, false);
    else if (!strcmp(argv[2], "completion-chunks-partial"))
        test_completion_chunks(65, true);
    else if (!strcmp(argv[2], "completion-overflow"))
        test_completion_chunks(IO_LATENCY_REQUEST_SCAN_BUDGET + 1, false);
    else if (!strcmp(argv[2], "remap"))
        test_remap();
    else if (!strcmp(argv[2], "remap-bit-six")) {
        io_latency_bio_trace_completion = 6;
        test_remap();
    }
    else if (!strcmp(argv[2], "partition-remap"))
        test_partition_remap();
    else if (!strcmp(argv[2], "busy-container"))
        test_busy_series_write();
    else if (!strcmp(argv[2], "container-allocation"))
        test_container_allocation_failure();
    else if (!strcmp(argv[2], "large-request"))
        test_large_completed_request();
    else {
        assert(!strcmp(argv[2], "request"));
        test_request_device();
    }
    return 0;
}
`
	// Native fixtures need member offsets, not the addresses of live objects.
	program = strings.NewReplacer(
		"compat_bpf_core_field_offset(bio->",
		"offsetof(struct bio, ",
		"compat_bpf_core_field_offset(req->",
		"offsetof(struct request, ",
		"compat_bpf_core_field_offset(\n\t\t((struct blkcg *)0)->css)",
		"offsetof(struct blkcg, css)",
		"compat_bpf_core_field_offset(\n\t\t\t((struct bio___5_12 *)bio)->bi_bdev)",
		"offsetof(struct bio, bi_bdev)",
	).Replace(program)
	// Preserve the branch semantics when compiling BPF's asm goto for the host.
	program = strings.ReplaceAll(program,
		`asm goto("if %0 != 0 goto %l[with_queue]" : : "r"(queue_ns) : : with_queue);`,
		"if (queue_ns) goto with_queue;")
	program = strings.ReplaceAll(program,
		`asm goto("if %0 != 0 goto %l[with_stamp]" : : "r"(stamp) : : with_stamp);`,
		"if (stamp) goto with_stamp;")
	abiIncludeFlag := "-I" + filepath.Join("..", "..", "bpf", "include")
	executable := compileIOControlTestC(
		t,
		"cc",
		"iolatency-remap",
		program,
		abiIncludeFlag,
	)
	for _, scenario := range []string{"classifier-buckets", "queue-time", "queue-layout"} {
		t.Run(scenario, func(t *testing.T) {
			output, err := exec.CommandContext(t.Context(), executable,
				"0", scenario, "rq-disk", "sign-extended").CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}
	for _, scenario := range []string{
		"remap", "remap-bit-six", "partition-remap", "request", "missing-queue",
		"busy-container", "css-release", "css-release-failure", "current-css",
		"container-allocation", "large-request", "root-blkcg", "root-blkcg-unknown",
		"request-time", "unselected-disk", "single-completion", "partial-completion",
		"aux-request-size", "completion-chunks", "completion-chunks-partial",
		"completion-overflow", "completion-buckets",
	} {
		slots, layouts, errnos := []string{"0"}, []string{"rq-disk"}, []string{"sign-extended"}
		// Raw event scenarios cover argument slots; request remap covers both
		// disk layouts. Errno width matters only when a helper returns an error.
		switch scenario {
		case "remap", "partition-remap", "request":
			slots = []string{"0", "1"}
		}
		if scenario == "request" {
			layouts = []string{"rq-disk", "queue-disk"}
		}
		switch scenario {
		case "busy-container", "container-allocation", "css-release-failure":
			errnos = []string{"sign-extended", "zero-extended"}
		}
		for _, slot := range slots {
			for _, layout := range layouts {
				for _, errno := range errnos {
					t.Run("slot-"+slot+"/"+scenario+"/"+layout+"/"+errno, func(t *testing.T) {
						output, err := exec.CommandContext(t.Context(), executable, slot, scenario, layout, errno).CombinedOutput()
						require.NoError(t, err, "%s", output)
					})
				}
			}
		}
	}
	t.Run("non-adjacent-bio-device", func(t *testing.T) {
		executable := compileIOControlTestC(
			t,
			"cc",
			"iolatency-device-gap",
			program,
			abiIncludeFlag,
			"-DIO_LATENCY_TEST_BIO_DEVICE_GAP",
		)
		output, err := exec.CommandContext(
			t.Context(),
			executable,
			"0",
			"queue-layout",
			"rq-disk",
			"sign-extended",
		).CombinedOutput()
		require.NoError(
			t,
			err,
			"%s",
			output,
		)
	})
	t.Run("non-adjacent-bio-tail", func(t *testing.T) {
		executable := compileIOControlTestC(
			t,
			"cc",
			"iolatency-remap-tail",
			program,
			abiIncludeFlag,
			"-DIO_LATENCY_TEST_BIO_TAIL_GAP",
		)
		output, err := exec.CommandContext(t.Context(), executable,
			"0", "single-completion", "rq-disk", "sign-extended").CombinedOutput()
		require.NoError(t, err, "%s", output)
	})
	t.Run("full-width-bio-time", func(t *testing.T) {
		executable := compileIOControlTestC(
			t,
			"cc",
			"iolatency-full-time",
			program,
			abiIncludeFlag,
			"-DIO_LATENCY_TEST_FULL_BIO_TIME",
		)
		for _, scenario := range []string{"remap", "request-time", "partial-completion"} {
			output, err := exec.CommandContext(t.Context(), executable,
				"1", scenario, "queue-disk", "sign-extended").CombinedOutput()
			require.NoError(t, err, "%s", output)
		}
	})
	t.Run("non-adjacent-request-times", func(t *testing.T) {
		executable := compileIOControlTestC(
			t,
			"cc",
			"iolatency-time-gap",
			program,
			abiIncludeFlag,
			"-DIO_LATENCY_TEST_REQUEST_TIME_GAP",
		)
		output, err := exec.CommandContext(t.Context(), executable,
			"0", "request-time", "rq-disk", "sign-extended").CombinedOutput()
		require.NoError(t, err, "%s", output)
	})
}

// Qualification: kernel prerequisites and real IO.

// Startup admission and BTF qualification.

func TestIOLatencyKernelStartup(t *testing.T) {
	for _, test := range []struct {
		name        string
		symbols     string
		config      string
		hostConfig  string
		localConfig string
		want        string
		root        uint64
		bioFlag     uint32
	}{
		{
			name:   "fallback not configured",
			config: "CONFIG_BLOCK=y\n# CONFIG_BLK_INLINE_ENCRYPTION is not set\n",
		},
		{
			name:   "fallback disabled",
			config: "CONFIG_BLK_INLINE_ENCRYPTION=y\n# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
		},
		{
			name:       "container reads host boot config without proc config",
			hostConfig: "# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
		},
		{
			name:        "container config cannot disable host fallback",
			hostConfig:  "CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK=y\n",
			localConfig: "# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
			want:        "tfms_inited",
		},
		{
			name:        "container config cannot replace missing host config",
			localConfig: "# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
			want:        "kernel config",
		},
		{
			name:    "root blkcg address is a startup constant",
			symbols: "ffffffff82003000 B blkcg_root\n",
			config:  "CONFIG_BLOCK=y\n",
			root:    0xffffffff82003000,
		},
		{
			name:    "anonymous bio flag is a startup constant",
			config:  "CONFIG_BLOCK=y\n",
			bioFlag: 6,
		},
		{
			name:   "enabled fallback requires its data symbol",
			config: "CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK=y\n",
			want:   "tfms_inited",
		},
		{
			name: "unavailable configuration is not disabled fallback",
			want: "kernel config",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			previous := filepath.Dir(procfs.DefaultPath())
			procfs.RootPrefix(root)
			t.Cleanup(func() { procfs.RootPrefix(previous) })
			word := &btf.Int{Name: "unsigned long", Size: 8, Encoding: btf.Unsigned}
			kernelTypes := []btf.Type{word}
			if test.bioFlag != 0 {
				kernelTypes = append(kernelTypes, &btf.Enum{
					Size:   4,
					Values: []btf.EnumValue{{Name: "BIO_TRACE_COMPLETION", Value: uint64(test.bioFlag)}},
				})
			}
			writeIOLatencyKernelBTF(t, kernelTypes)
			if err := os.MkdirAll(procfs.DefaultPath(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(procfs.Path("kallsyms"), []byte(test.symbols), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.hostConfig != "" || test.localConfig != "" {
				const release = "test-kernel"
				require.NoError(t, os.MkdirAll(procfs.Path("sys", "kernel"), 0o700))
				require.NoError(t, os.WriteFile(procfs.Path("sys", "kernel", "osrelease"), []byte(release+"\n"), 0o600))
				for path, config := range map[string]string{
					procfs.Path("1", "root", "boot", "config-"+release): test.hostConfig,
					filepath.Join(root, "boot", "config-"+release):      test.localConfig,
				} {
					if config != "" {
						require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
						require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
					}
				}
			}
			if test.config != "" {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write([]byte(test.config)); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(procfs.Path("config.gz"), compressed.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			constants, err := loadIOLatencyKernelConstants()
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("startup error = %v; want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if constants["io_latency_root_blkcg"] != test.root {
				t.Fatalf("root blkcg = %v; want %#x", constants["io_latency_root_blkcg"], test.root)
			}
			flag := test.bioFlag
			if flag == 0 {
				flag = 10
			}
			assertIOLatencyConstant(t, constants, ioLatencyBioTraceFlag, flag)
		})
	}
}

// Invalid BTF cannot provide the structure relocations required by IO tracing.
func TestIOLatencyKernelBTFStartup(t *testing.T) {
	word := &btf.Int{Name: "unsigned long", Size: 8, Encoding: btf.Unsigned}
	for _, test := range []struct {
		name      string
		types     []btf.Type
		missing   bool
		malformed bool
		want      string
		cause     error
	}{
		{name: "missing BTF", missing: true, want: "sys/kernel/btf/vmlinux", cause: os.ErrNotExist},
		{name: "malformed BTF", types: []btf.Type{word}, malformed: true, want: "sys/kernel/btf/vmlinux"},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := filepath.Dir(procfs.DefaultPath())
			procfs.RootPrefix(t.TempDir())
			t.Cleanup(func() { procfs.RootPrefix(previous) })
			if !test.missing {
				writeIOLatencyKernelBTF(t, test.types)
				if test.malformed {
					path := filepath.Join(procfs.DefaultPathByType("sys"), "kernel", "btf", "vmlinux")
					if err := os.WriteFile(path, []byte("invalid BTF"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			collector := &iolatencyTracing{}
			err := collector.Start(context.Background())
			if !errors.Is(err, types.ErrTracingStopped) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("startup error = %v, want stopped with %q", err, test.want)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("startup error = %v, want underlying cause %v", err, test.cause)
			}
			if collector.session != nil {
				t.Fatal("invalid kernel BTF published a session")
			}
		})
	}
}

func writeIOLatencyKernelBTF(t *testing.T, kernelTypes []btf.Type) {
	t.Helper()
	builder, err := btf.NewBuilder(kernelTypes)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := builder.Marshal(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(procfs.DefaultPathByType("sys"), "kernel", "btf", "vmlinux")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIOLatencyBioTraceCompletion(t *testing.T) {
	for _, test := range []struct {
		name     string
		values   []uint64
		enumName string
		want     uint32
		invalid  bool
	}{
		{name: "legacy macro ABI", want: 10},
		{name: "anonymous flag eight", values: []uint64{8}, want: 8},
		{name: "anonymous flag six", values: []uint64{6}, want: 6},
		{name: "named enum", values: []uint64{7}, enumName: "bio_flags", want: 7},
		{name: "first flag", values: []uint64{0}, want: 0},
		{name: "last flag", values: []uint64{15}, want: 15},
		{name: "equivalent enums", values: []uint64{8, 8}, want: 8},
		{name: "conflicting enums", values: []uint64{8, 6}, invalid: true},
		{name: "flag exceeds bi_flags", values: []uint64{16}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates := []btf.Type{&btf.Enum{
				Size:   4,
				Values: []btf.EnumValue{{Name: "UNRELATED_FLAG", Value: 1}},
			}}
			for index, value := range test.values {
				candidates = append(candidates, &btf.Enum{
					Name: test.enumName, Size: 4,
					Values: []btf.EnumValue{
						{Name: "BIO_TRACE_COMPLETION", Value: value},
						{Name: "UNRELATED_FLAG", Value: uint64(index)},
					},
				})
			}
			builder, err := btf.NewBuilder(candidates)
			require.NoError(t, err)
			raw, err := builder.Marshal(nil, nil)
			require.NoError(t, err)
			spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
			require.NoError(t, err)
			flag, err := ioLatencyBioTraceCompletion(spec)
			if test.invalid {
				require.ErrorContains(t, err, "BIO_TRACE_COMPLETION")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, flag)
		})
	}
}

// Software blk-crypto prerequisite decoding.

func TestIOLatencyCryptoModeCount(t *testing.T) {
	for _, test := range []struct {
		name       string
		bounds     []uint64
		isReversed bool
		want       uint32
	}{
		{name: "four modes", bounds: []uint64{4}, want: 4},
		{name: "five modes", bounds: []uint64{5}, want: 5},
		{name: "missing bound"},
		{name: "empty array", bounds: []uint64{0}},
		{name: "array exceeds probe capacity", bounds: []uint64{65}},
		{name: "equivalent enums", bounds: []uint64{4, 4}, want: 4},
		{name: "equivalent enums reversed", bounds: []uint64{4, 4}, isReversed: true, want: 4},
		{name: "conflicting enums", bounds: []uint64{4, 5}},
		{name: "conflicting enums reversed", bounds: []uint64{5, 4}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates := []btf.Type{}
			for index, bound := range test.bounds {
				// Compilation units may disagree on values unrelated to the bound.
				candidates = append(candidates, &btf.Enum{
					Name: "blk_crypto_mode_num", Size: 4,
					Values: []btf.EnumValue{
						{Name: "BLK_ENCRYPTION_MODE_MAX", Value: bound},
						{Name: "other", Value: uint64(index)},
					},
				})
			}
			if len(candidates) == 0 {
				candidates = append(candidates, &btf.Enum{
					Name: "blk_crypto_mode_num", Size: 4,
					Values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_INVALID", Value: 0}},
				})
			}
			if test.isReversed {
				slices.Reverse(candidates)
			}
			builder, err := btf.NewBuilder(candidates)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := builder.Marshal(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			count, err := ioLatencyCryptoModeCount(spec)
			if test.want == 0 {
				if err == nil {
					t.Fatal("invalid array bound accepted")
				}
			} else if err != nil || count != test.want {
				t.Fatalf("mode count = %d, %v; want %d", count, err, test.want)
			}
		})
	}
}

type fakeIOLatencyCryptoBPF struct {
	bpf.BPF
	result    int64
	attachErr error
	readErr   error
}

// Queue timestamps and disk enrollment isolation.

// Disks without both kernel timestamps are excluded, not startup failures.
// The BPF result is supplied at the map-read boundary, after reading iostats.
// Hidden NVMe physical paths have no sysfs dev file; identity comes from BPF.
func TestIOLatencyQueueStats(t *testing.T) {
	for _, test := range []struct {
		name    string
		disk    string
		iostats string
		result  int64
		want    string
		skip    bool
	}{
		{name: "hidden NVMe path with both timestamps enabled", iostats: "1\n", result: 2},
		{name: "NVMe namespace", disk: "nvme0n1", iostats: "1\n", result: 2},
		{name: "NVMe multipath head without timestamps", disk: "nvme0n1", iostats: "0\n", result: 1},
		{name: "SCSI disk", disk: "sda", iostats: "1\n", result: 2},
		{name: "SCSI extended disk name", disk: "sdaa", iostats: "1\n", result: 2},
		{name: "virtio disk", disk: "vda", iostats: "1\n", result: 2},
		{name: "nonzero virtio iostats", disk: "vda", iostats: "2\n", result: 2},
		{name: "virtio extended disk name", disk: "vdaa", iostats: "1\n", result: 2},
		{name: "virtio G disabled", disk: "vda", iostats: "0\n", result: 2},
		{name: "virtio D disabled", disk: "vda", iostats: "1\n", result: 3},
		{name: "G disabled", iostats: "0\n", result: 2},
		{name: "D disabled", iostats: "1\n", result: 3},
		{name: "probe missed", iostats: "1\n", want: "did not execute"},
		{name: "failed kernel read", iostats: "1\n", result: -int64(unix.EFAULT), want: "bad address"},
		{name: "device mapper is not probed", disk: "dm-0", skip: true},
		{name: "MD is not probed", disk: "md0", skip: true},
		{name: "loop is not probed", disk: "loop0", skip: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			disk := test.disk
			if disk == "" {
				disk = "nvme0c1n1"
			}
			path := filepath.Join(root, "block", disk)
			if err := os.MkdirAll(filepath.Join(path, "queue"), 0o700); err != nil {
				t.Fatal(err)
			}
			if !test.skip {
				if err := os.WriteFile(filepath.Join(path, "queue", "iostats"), []byte(test.iostats), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			object := &fakeIOLatencyQueueBPF{result: test.result}
			session := &ioLatencySession{object: object}
			err := session.discoverDisks(root)
			devices := session.devices
			if test.skip {
				if err != nil || len(devices) != 0 || len(object.keys) != 0 {
					t.Fatalf("excluded disk: devices=%v, requests=%v, error=%v", devices, object.keys, err)
				}
				return
			}
			if string(bytes.TrimRight(object.keys[0], "\x00")) != disk {
				t.Fatalf("probe requested disk %q", object.keys[0])
			}
			if object.resultArmed {
				t.Fatal("discovery left its publication command armed")
			}
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error = %v, want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.result == 1 || test.result == 3 || test.iostats == "0\n" {
				if len(devices) != 0 || len(object.host) != 0 || len(object.keys) != 1 {
					t.Fatalf("skipped disk: devices=%v, keys=%v", devices, object.keys)
				}
				return
			}
			require.Equal(t, map[[2]uint32]string{{259, 7}: disk}, devices)
		})
	}
}

// Execute the production queue probe with kernel reads and map helpers supplied
// by a native fixture. Only the requested disk's iostats read may report state.
func TestIOLatencyQueueProbeNVMeHead(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "bpf", "iolatency_tracing.c"))
	if err != nil {
		t.Fatal(err)
	}
	section := func(start, end string) string {
		return ioControlTestSourceSection(t, string(data), start, end)
	}
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
typedef uint32_t __u32;
typedef uint64_t __u64;
typedef int64_t __s64;
typedef uint64_t u64;
typedef uint32_t u32;
typedef uint8_t u8;
typedef int64_t s64;
#include "abi/iolatency_types.h"
#define SEC(name)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define PT_REGS_PARM1(ctx) ((ctx)->arg1)
#define PT_REGS_PARM2_CORE(ctx) ((ctx)->arg2)
#define bpf_core_field_exists(field) stats_has_accounting
#define blk_queue_stats___queue_probe blk_queue_stats
#define BPF_CORE_READ(src, field) ((src)->field)
#define BPF_CORE_READ_INTO(dst, src, field) \
    (memcpy((dst), &(src)->field, sizeof(*(dst))), 0)
#define QUEUE_PROBE_CONTAINER(ptr, type, field) \
    ((type *)((char *)(ptr) - offsetof(type, field)))
#define IO_LATENCY_EFAULT 14
#define COMPAT_BPF_EXIST 2
struct pt_regs { __u64 arg1, arg2; };
struct attribute { const char *name; };
struct kobject { struct kobject *parent; const char *name; };
struct device { struct kobject kobj; struct gendisk *disk; };
struct list_head { struct list_head *next; };
struct blk_mq_ops { int unused; };
struct blk_queue_stats {
    struct list_head callbacks;
    int accounting;
    bool enable_accounting;
};
struct request_queue {
    struct blk_queue_stats *stats;
    const struct blk_mq_ops *mq_ops;
};
struct gendisk {
    struct request_queue *queue;
    __u32 major, first_minor;
};
static struct iolatency_queue_probe_result result;
static struct iolatency_queue_probe_key target;
static int io_latency_probe_result;
static bool replace_result;
static bool stats_has_accounting;
static struct gendisk *io_latency_device_disk(struct device *device)
{
    return device->disk;
}
static int bpf_probe_read_str(void *destination, unsigned size, const char *src)
{
    char *dst = destination;
    unsigned length = strlen(src) + 1;
    if (length > size)
        length = size;
    memcpy(dst, src, length);
    dst[length - 1] = 0;
    return length;
}
static void *bpf_map_lookup_elem(void *map, const void *key)
{
    assert(map == &io_latency_probe_result);
    return memcmp(key, &target, sizeof(target)) ? NULL : &result;
}
static long bpf_map_update_elem(void *map, const void *key,
                               const void *value, __u64 flags)
{
    assert(map == &io_latency_probe_result);
    assert(flags == COMPAT_BPF_EXIST);
    if (replace_result) {
        strcpy((char *)target.name, "different-disk");
        replace_result = false;
    }
    if (memcmp(key, &target, sizeof(target)))
        return -2;
    result = *(const struct iolatency_queue_probe_result *)value;
    return 0;
}
` + section("SEC(\"kprobe/queue_attr_show\")", "/* Thresholds are below") + `
int main(int argc, char **argv)
{
    assert(argc == 8);
    stats_has_accounting = !strcmp(argv[7], "accounting");
    struct blk_queue_stats stats = {
        .callbacks.next = &stats.callbacks,
        .accounting = !strcmp(argv[3], "enabled"),
        .enable_accounting = !strcmp(argv[3], "enabled"),
    };
    struct blk_mq_ops ops = {};
    struct request_queue queue = {
        .stats = &stats,
        .mq_ops = !strcmp(argv[2], "mq") ? &ops : NULL,
    };
    struct gendisk disk = { .queue = &queue, .major = 259, .first_minor = 7 };
    struct device disk_device = { .kobj.name = argv[1], .disk = &disk };
    struct kobject kobj = { .parent = &disk_device.kobj };
    struct attribute attr = { .name = argv[5] };
    struct pt_regs ctx = { .arg1 = (__u64)&kobj, .arg2 = (__u64)&attr };
    strcpy((char *)target.name, argv[6]);
    replace_result = !strcmp(argv[4], "late-result");
    assert(!probe_queue_stats(&ctx));
    if (!strcmp(argv[4], "late-result")) {
        assert(!result.result);
        return 0;
    }
    if (result.result == 2) {
        assert(result.disk == (__u64)&disk);
        result.result = 0;
        assert(!probe_queue_stats(&ctx));
        assert(result.result == 2);
    }
    printf("%lld\n", (long long)result.result);
    return 0;
}
`
	executable := compileIOControlTestC(
		t,
		"cc",
		"iolatency-queue-probe",
		program,
		"-std=gnu11",
		"-I"+filepath.Join("..", "..", "bpf", "include"),
	)
	for _, test := range []struct {
		name, disk, queue, stats, attribute, target, want string
	}{
		{"head without timestamps", "nvme0n1", "legacy", "disabled", "iostats", "nvme0n1", "1\n"},
		{"head with timestamps", "nvme0n1", "legacy", "enabled", "iostats", "nvme0n1", "1\n"},
		{"hidden NVMe path", "nvme0c1n1", "mq", "enabled", "iostats", "nvme0c1n1", "2\n"},
		{"NVMe namespace", "nvme0n1", "mq", "enabled", "iostats", "nvme0n1", "2\n"},
		{"NVMe path without timestamps", "nvme0c1n1", "mq", "disabled", "iostats", "nvme0c1n1", "3\n"},
		{"legacy SCSI disk", "sda", "legacy", "enabled", "iostats", "sda", "2\n"},
		{"legacy SCSI without timestamps", "sda", "legacy", "disabled", "iostats", "sda", "3\n"},
		{"virtio disk", "vda", "mq", "enabled", "iostats", "vda", "2\n"},
		{"virtio without timestamps", "vda", "mq", "disabled", "iostats", "vda", "3\n"},
		{"other disk", "sdb", "legacy", "enabled", "iostats", "sda", "0\n"},
		{"other attribute", "sda", "legacy", "enabled", "scheduler", "sda", "0\n"},
		{"attribute prefix", "sda", "legacy", "enabled", "iostats-extra", "sda", "0\n"},
	} {
		for _, layout := range []string{"accounting", "enable-accounting"} {
			t.Run(test.name+"/"+layout, func(t *testing.T) {
				want := test.want
				output, err := exec.CommandContext(t.Context(), executable, test.disk, test.queue, test.stats, "probe", test.attribute, test.target, layout).CombinedOutput()
				if err != nil || string(output) != want {
					t.Fatalf("queue probe = %q, %v; want %q", output, err, want)
				}
			})
		}
	}
	t.Run("late-result", func(t *testing.T) {
		output, err := exec.CommandContext(t.Context(), executable,
			"nvme0n1", "mq", "enabled", "late-result", "iostats", "nvme0n1", "accounting").CombinedOutput()
		require.NoError(t, err, "%s", output)
	})
}

type fakeIOLatencyQueueBPF struct {
	bpf.BPF
	result      int64
	keys        [][]byte
	readErr     error
	writeErr    error
	deleteErr   error
	resultKey   []byte
	resultArmed bool
	host        map[uint64]ioLatencyDiskCounters
}

func (b *fakeIOLatencyQueueBPF) MapIDByName(name string) uint32 {
	if name == ioLatencyProbeResultMap {
		return 2
	}
	if name == blkDiskBucketMap {
		return 3
	}
	return 0
}

func (b *fakeIOLatencyQueueBPF) WriteMapItems(id uint32, items []bpf.MapItem) error {
	if id == 3 {
		if b.host == nil {
			b.host = make(map[uint64]ioLatencyDiskCounters)
		}
		for _, item := range items {
			var value ioLatencyDiskCounters
			if err := decodeBPFMapData(item.Value, &value); err != nil {
				return err
			}
			b.host[binary.NativeEndian.Uint64(item.Key)] = value
		}
		return nil
	}
	if id != 2 {
		return fmt.Errorf("unexpected disk probe result map ID %d", id)
	}
	if b.writeErr != nil {
		return b.writeErr
	}
	b.resultKey = bytes.Clone(items[0].Key)
	b.keys = append(b.keys, bytes.Clone(items[0].Key))
	b.resultArmed = true
	return nil
}

func (b *fakeIOLatencyQueueBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	if id != 2 || !b.resultArmed || !bytes.Equal(keys[0], b.resultKey) {
		return unix.ENOENT
	}
	b.resultArmed = false
	return nil
}

func (b *fakeIOLatencyQueueBPF) ReadMap(id uint32, key []byte) ([]byte, error) {
	if id == 3 {
		value, found := b.host[binary.NativeEndian.Uint64(key)]
		if !found {
			return nil, nil
		}
		return bytesutil.ToBytes(value), nil
	}
	if id != 2 || !b.resultArmed || !bytes.Equal(key, b.resultKey) {
		return nil, unix.ENOENT
	}
	minor := uint32(7)
	switch string(bytes.TrimRight(key, "\x00")) {
	case "sdb":
		minor = 8
	case "nvme1n1":
		minor = 9
	case "vdb":
		minor = 10
	}
	return bytesutil.ToBytes(ioLatencyQueueProbe{
		Result: b.result, Major: 259, Minor: minor, Disk: uint64(minor),
	}), b.readErr
}

func TestIOLatencyQueueProbeDiskIsolation(t *testing.T) {
	root := t.TempDir()
	for _, disk := range []string{"sda", "sdb", "vda", "vdb", "nvme1n1"} {
		path := filepath.Join(root, "block", disk, "queue")
		require.NoError(t, os.MkdirAll(path, 0o700))
		stats := "1\n"
		if disk == "vda" {
			stats = "0\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(path, "iostats"), []byte(stats), 0o600))
	}
	object := &fakeIOLatencyQueueBPF{result: 2}
	session := &ioLatencySession{object: object}
	require.NoError(t, session.discoverDisks(root))
	require.False(t, object.resultArmed)
	require.Equal(t, map[[2]uint32]string{
		{259, 7}: "sda", {259, 8}: "sdb", {259, 9}: "nvme1n1", {259, 10}: "vdb",
	}, session.devices)
	for i, want := range []string{"nvme1n1", "sda", "sdb", "vda", "vdb"} {
		require.Equal(t, want, string(bytes.TrimRight(object.keys[i], "\x00")))
	}
	require.Equal(t, map[uint64]ioLatencyDiskCounters{
		7: {Major: 259, Minor: 7, Scsi: 1}, 8: {Major: 259, Minor: 8, Scsi: 1},
		9: {Major: 259, Minor: 9}, 10: {Major: 259, Minor: 10},
	}, object.host)
}

// Software blk-crypto probe results.

func (b *fakeIOLatencyCryptoBPF) AttachWithOptions(_ []bpf.AttachOption) error {
	return b.attachErr
}

func (b *fakeIOLatencyCryptoBPF) MapIDByName(_ string) uint32 { return 1 }

func (b *fakeIOLatencyCryptoBPF) ReadMap(_ uint32, _ []byte) ([]byte, error) {
	value := make([]byte, 8)
	netutil.NativeEndian.PutUint64(value, uint64(b.result))
	return value, b.readErr
}

func TestIOLatencyCryptoProbeResult(t *testing.T) {
	for _, test := range []struct {
		name string
		bpf  fakeIOLatencyCryptoBPF
		want error
	}{
		{name: "all modes uninitialized", bpf: fakeIOLatencyCryptoBPF{result: 1}},
		{name: "initialized mode stops tracing", bpf: fakeIOLatencyCryptoBPF{result: 2}, want: types.ErrTracingStopped},
		{name: "kernel data read error", bpf: fakeIOLatencyCryptoBPF{result: -int64(unix.EFAULT)}, want: unix.EFAULT},
		{name: "attach failure", bpf: fakeIOLatencyCryptoBPF{attachErr: unix.EPERM}, want: unix.EPERM},
		{name: "map read failure", bpf: fakeIOLatencyCryptoBPF{readErr: unix.EIO}, want: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := readIOLatencyCryptoProbe(&test.bpf); !errors.Is(err, test.want) {
				t.Fatalf("probe error = %v; want %v", err, test.want)
			}
		})
	}
	if err := readIOLatencyCryptoProbe(&fakeIOLatencyCryptoBPF{}); err == nil {
		t.Fatal("an unexecuted probe was accepted as disabled fallback")
	}
}

// Startup-probe objects and live kernel qualification.

func TestIOLatencyKernelProbeObject(t *testing.T) {
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(
		"..", "..", "bpf", "iolatency_probe.o"))
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.RewriteConstants(map[string]any{
		"io_latency_tfms_inited":  uint64(0xffff888000000000),
		"io_latency_crypto_modes": uint32(5),
	}); err != nil {
		t.Fatal(err)
	}
	if program := spec.Programs["probe_crypto_fallback"]; program == nil ||
		program.Type != ebpf.RawTracepoint || program.AttachTo != "sys_enter" {
		t.Fatalf("crypto probe program = %#v", program)
	}
	result := spec.Maps["io_latency_crypto_map"]
	if result == nil || result.Type != ebpf.Array || result.KeySize != 4 ||
		result.ValueSize != 8 || result.MaxEntries != 1 {
		t.Fatalf("crypto result map = %#v", result)
	}
	spec, err = ebpf.LoadCollectionSpec(filepath.Join(
		"..", "..", "bpf", "iolatency_tracing.o"))
	if err != nil {
		t.Fatal(err)
	}
	if program := spec.Programs["probe_queue_stats"]; program == nil ||
		program.Type != ebpf.Kprobe || program.AttachTo != "queue_attr_show" {
		t.Fatalf("queue probe program = %#v", program)
	}
	result = spec.Maps[ioLatencyProbeResultMap]
	if result == nil || result.Type != ebpf.Hash || result.KeySize != 32 ||
		result.ValueSize != 24 || result.MaxEntries != 1 || result.Flags != 0 {
		t.Fatalf("queue result map = %#v", result)
	}
}
