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

// Native fixtures exercise kernel-layout compatibility in the health probes.
package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIOHealthSCSITracepointLayout(t *testing.T) {
	code := ioControlBPFSource(t, "io_health.c")
	layout := ioControlTestSourceSection(
		t,
		code,
		"struct io_health_scsi_timeout",
		"static __always_inline u32 encode_dev",
	)
	probes := ioControlTestSourceSection(
		t,
		code,
		`SEC("tracepoint/scsi/scsi_dispatch_cmd_timeout")`,
		"/*\n * nvme_sysfs_show_state",
	)
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
typedef uint64_t u64;
typedef uint32_t u32;
typedef uint8_t u8;
typedef int32_t s32;
#include "abi/io_health_types.h"
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define SEC(name)
#define bpf_core_type_size(type) header_size
static unsigned char context[64];
static u32 header_size;
static int events;
static struct io_health_event last_event;
static int bpf_probe_read_kernel(void *dst, u32 size, const void *src)
{
    assert((const unsigned char *)src >= context);
    assert((const unsigned char *)src + size <= context + sizeof(context));
    memcpy(dst, src, size);
    return 0;
}
static u64 io_health_topology_generation(bool changed) { return 9; }
static void submit_event(void *ctx, struct io_health_event *event)
{
    assert(ctx == context);
    last_event = *event;
    events++;
}
` + layout + probes + `
int main(int argc, char **argv)
{
    header_size = strtoul(argv[1], NULL, 10);
    const u32 identity[] = { 6, 7, 8, 0x10203040 };
    const s32 results[] = { 0x1055, -5 };
    memset(context, 0xa5, sizeof(context));
    memcpy(context + header_size, identity, sizeof(identity));
    for (unsigned i = 0; i < 2; i++) {
	memcpy(context + header_size + sizeof(identity), &results[i], sizeof(s32));
	for (unsigned error = 0; error < 2; error++) {
	    events = 0;
	    if (error)
		trace_scsi_dispatch_error((void *)context);
	    else
		trace_scsi_timeout((void *)context);
	    assert(events == 1);
	    assert(last_event.type == (error ? HEALTH_EVENT_SCSI_DISPATCH_ERROR :
					       HEALTH_EVENT_SCSI_TIMEOUT));
	    assert(last_event.host == identity[0]);
	    assert(last_event.channel == identity[1]);
	    assert(last_event.target == identity[2]);
	    assert(last_event.lun == identity[3]);
	    assert(last_event.status == (error ? results[i] : 0));
	    assert(last_event.topology_generation == 9);
	}
    }
    return 0;
}
`
	executable := compileIOControlTestC(
		t,
		"cc",
		"io-health-scsi-layout",
		program,
		"-I"+filepath.Join("..", "..", "bpf", "include"),
	)
	for _, test := range []struct {
		name       string
		headerSize int
	}{
		{
			name:       "standard header",
			headerSize: 8,
		},
		{
			name:       "lazy preempt header",
			headerSize: 12,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.CommandContext(
				t.Context(),
				executable,
				strconv.Itoa(test.headerSize),
			).CombinedOutput()
			require.NoError(
				t,
				err,
				"%s",
				output,
			)
		})
	}
}

func TestIOHealthBlockStatusMapping(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "bpf", "io_health.c"))
	require.NoError(t, err)
	code := string(source)
	start := strings.Index(code, "static __always_inline s32 io_health_block_errno")
	end := strings.Index(code, "/*\n * Use completion")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	program := `
#include <assert.h>
#include <errno.h>
#include <stdint.h>
#include <string.h>
typedef uint64_t u64;
typedef uint32_t u32;
typedef int32_t s32;
static u64 io_health_blk_errors_addr;
static u32 io_health_blk_errors_stride, io_health_blk_errors_count;
static int fail_read, reads;
static int bpf_probe_read(void *dst, u32 size, const void *src)
{
    assert((uintptr_t)src >= io_health_blk_errors_addr);
    assert((uintptr_t)src + size <= io_health_blk_errors_addr +
           io_health_blk_errors_stride * io_health_blk_errors_count);
    if (++reads == fail_read)
        return -1;
    memcpy(dst, src, size);
    return 0;
}
` + code[start:end] + `
int main(void)
{
    unsigned char table[20 * 24] = {};
    /* The same status 16 means active-zone resource on the old layout and
     * offline after removal of ZONE_RESOURCE. Later kernels also add tag.
     */
    for (unsigned layout = 0; layout < 3; layout++) {
        io_health_blk_errors_addr = (uintptr_t)table;
        io_health_blk_errors_stride = layout == 2 ? 24 : 16;
        io_health_blk_errors_count = 20;
        const s32 entries[] = { 0, -EOPNOTSUPP, -ETIMEDOUT, -ENOSPC };
        for (unsigned i = 0; i < 4; i++)
            memcpy(table + i * io_health_blk_errors_stride, &entries[i], sizeof(s32));
        s32 wanted = layout == 0 ? -EOVERFLOW : -ENODEV;
        memcpy(table + 16 * io_health_blk_errors_stride, &wanted, sizeof(wanted));
        reads = 0;
        assert(io_health_block_errno(0) == 0);
        assert(io_health_block_errno(-ETIMEDOUT) == -ETIMEDOUT);
        assert(io_health_block_errno(20) == 20);
        assert(reads == 0);
        assert(io_health_block_errno(16) == wanted);
        assert(io_health_block_errno(2) == -ETIMEDOUT);
        assert(io_health_block_errno(18) == 18); /* A hole is not success. */
        for (fail_read = 1; fail_read <= 5; fail_read++) {
            reads = 0;
            assert(io_health_block_errno(16) == 16);
        }
        fail_read = 0;
        table[0] = 1; /* Reject a table whose prefix does not match. */
        assert(io_health_block_errno(16) == 16);
    }
    io_health_blk_errors_addr = 0;
    assert(io_health_block_errno(16) == 16);
    return 0;
}
`
	compiler, err := exec.LookPath("cc")
	require.NoError(t, err)
	executable := filepath.Join(t.TempDir(), "block-status")
	compile := exec.CommandContext(t.Context(), compiler, "-x", "c", "-O2", "-o", executable, "-")
	compile.Stdin = strings.NewReader(program)
	output, err := compile.CombinedOutput()
	require.NoError(t, err, "%s", output)
	output, err = exec.CommandContext(t.Context(), executable).CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestIOHealthFreezeAccounting(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "bpf", "io_health.c"))
	require.NoError(t, err)
	code := string(source)
	start := strings.Index(code, "static __always_inline u64 io_health_topology_generation")
	end := strings.Index(code, "/* The dedicated error tracepoint")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
typedef uint64_t u64;
typedef uint32_t u32;
#define SEC(name)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define BPF_CORE_READ(src, field) ((src)->field)
#define bpf_core_read(dst, size, src) (memcpy(dst, src, size), 0)
#define bpf_core_field_exists(field) modern
#define compat_bpf_core_field_offset(field) ((u64)&(field))
#define COMPAT_BPF_ANY 0
#define COMPAT_BPF_NOEXIST 1
struct kobject { u64 pad; struct kobject *parent; };
struct device_type { const char *name; };
struct device { u64 pad; struct kobject kobj; const struct device_type *type; };
struct gendisk {
    u32 major, first_minor;
    char disk_name[32];
    struct { struct device __dev; } part0;
};
struct request_queue { struct gendisk *disk; struct kobject kobj; };
struct request_queue___5_14 { struct gendisk *disk; };
struct pt_regs { void *arg1; };
static bool modern;
static int io_health_freeze_counts, io_health_topology;
static u64 topology_generation;
static u64 keys[2], values[2];
static bool occupied[2];
static int bpf_probe_read_str(char *dst, u32 size, const char *src)
{
    if (!src)
        return -1;
    u32 len = strlen(src) + 1;
    if (len > size)
        len = size;
    memcpy(dst, src, len);
    dst[len - 1] = 0;
    return len;
}
static u64 *bpf_map_lookup_elem(void *map, const void *raw_key)
{
    if (map == &io_health_topology)
        return &topology_generation;
    const u64 *key = raw_key;
    assert(map == &io_health_freeze_counts);
    for (unsigned i = 0; i < 2; i++)
        if (occupied[i] && keys[i] == *key)
            return &values[i];
    return NULL;
}
static int bpf_map_update_elem(void *map, const u64 *key, const u64 *value, int flags)
{
    u64 *stored = bpf_map_lookup_elem(map, key);
    if (stored) {
        if (flags == COMPAT_BPF_NOEXIST)
            return -17;
        *stored = *value;
        return 0;
    }
    for (unsigned i = 0; i < 2; i++) {
        if (!occupied[i]) {
            occupied[i] = true;
            keys[i] = *key;
            values[i] = *value;
            return 0;
        }
    }
    return -7;
}
static int bpf_map_delete_elem(void *map, const u64 *key)
{
    assert(map == &io_health_freeze_counts);
    for (unsigned i = 0; i < 2; i++) {
        if (occupied[i] && keys[i] == *key) {
            occupied[i] = false;
            return 0;
        }
    }
    return -2;
}
` + code[start:end] + `
int main(void)
{
    for (unsigned layout = 0; layout < 2; layout++) {
        memset(occupied, 0, sizeof(occupied));
        modern = layout;
        struct gendisk first = { .major = 8, .disk_name = "sda" };
        struct gendisk second = { .major = 259, .disk_name = "nvme0n1" };
        struct request_queue q = { .disk = &first, .kobj.parent = &first.part0.__dev.kobj };
        struct pt_regs queue = { .arg1 = &q }, disk = { .arg1 = &first };
        u64 first_key = (u64)8 << 32, second_key = (u64)259 << 32;
        const struct device_type disk_type = { .name = "disk" };
        struct device published = { .type = &disk_type };
        struct pt_regs publishing = { .arg1 = &published };
        for (unsigned count = 0; count < 2; count++)
            io_health_freeze_queue(&queue);
        assert(*bpf_map_lookup_elem(&io_health_freeze_counts, &first_key) == 2);
        disk.arg1 = &second;
        io_health_disk_publishing(&publishing);
        io_health_freeze_disk_added(&disk);
        assert(io_health_topology_generation(false) == 1);
        disk.arg1 = &first;
        io_health_freeze_disk_removed(&disk);
        assert(io_health_topology_generation(false) == 2);
        assert(!bpf_map_lookup_elem(&io_health_freeze_counts, &first_key));
        assert(*bpf_map_lookup_elem(&io_health_freeze_counts, &second_key) == 0);
        u64 old_event_generation = io_health_topology_generation(false);
        io_health_disk_publishing(&publishing);
        /* Replacement identity is now visible, before queue registration. */
        assert(io_health_topology_generation(false) != old_event_generation);
        io_health_freeze_disk_added(&disk);
        assert(io_health_topology_generation(false) == 3);
        assert(*bpf_map_lookup_elem(&io_health_freeze_counts, &first_key) == 0);
        io_health_freeze_queue(&queue);
        assert(*bpf_map_lookup_elem(&io_health_freeze_counts, &first_key) == 1);
        const struct device_type other_types[] = {
            { .name = "partition" }, { .name = "diskette" },
        };
        for (unsigned i = 0; i < 2; i++) {
            published.type = &other_types[i];
            io_health_disk_publishing(&publishing);
        }
        published.type = NULL;
        io_health_disk_publishing(&publishing);
        publishing.arg1 = NULL;
        io_health_disk_publishing(&publishing);
        assert(io_health_topology_generation(false) == 3);
        topology_generation = 0;
        strcpy(first.disk_name, "loop0");
        io_health_freeze_queue(&queue);
        assert(*bpf_map_lookup_elem(&io_health_freeze_counts, &first_key) == 1);
    }
    return 0;
}
`
	compiler, err := exec.LookPath("cc")
	require.NoError(t, err)
	executable := filepath.Join(t.TempDir(), "io-health-freeze")
	compile := exec.CommandContext(t.Context(), compiler, "-x", "c", "-O2", "-o", executable, "-")
	compile.Stdin = strings.NewReader(program)
	output, err := compile.CombinedOutput()
	require.NoError(t, err, "%s", output)
	output, err = exec.CommandContext(t.Context(), executable).CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestIOHealthNVMeResetAccounting(t *testing.T) {
	code := ioControlBPFSource(t, "io_health.c")
	probes := ioControlTestSourceSection(
		t,
		code,
		`SEC("kprobe/nvme_reset_ctrl")`,
		`SEC("kprobe/nvme_change_ctrl_state")`,
	)
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
typedef uint64_t u64;
typedef uint32_t u32;
typedef uint8_t u8;
typedef int32_t s32;
#include "abi/io_health_types.h"
#define SEC(name)
#define BPF_KPROBE(name, ...) name(struct pt_regs *ctx, __VA_ARGS__)
#define BPF_KRETPROBE(name) name(struct pt_regs *ctx)
#define PT_REGS_RC(ctx) ((ctx)->result)
#define COMPAT_BPF_ANY 0
struct pt_regs { long result; };
static int nvme_reset_calls, events;
static u64 task = 1, keys[2], values[2];
static bool occupied[2];
static struct io_health_event last_event;
static u64 bpf_get_current_pid_tgid(void) { return task; }
static u64 io_health_topology_generation(bool changed) { return 9; }
static u64 *bpf_map_lookup_elem(void *map, const u64 *key)
{
    assert(map == &nvme_reset_calls);
    for (unsigned i = 0; i < 2; i++)
        if (occupied[i] && keys[i] == *key)
            return &values[i];
    return NULL;
}
static int bpf_map_update_elem(void *map, const u64 *key, const u64 *value, int flags)
{
    u64 *stored = bpf_map_lookup_elem(map, key);
    if (stored) {
        *stored = *value;
        return 0;
    }
    for (unsigned i = 0; i < 2; i++) {
        if (!occupied[i]) {
            occupied[i] = true;
            keys[i] = *key;
            values[i] = *value;
            return 0;
        }
    }
    return -7;
}
static int bpf_map_delete_elem(void *map, const u64 *key)
{
    assert(map == &nvme_reset_calls);
    for (unsigned i = 0; i < 2; i++) {
        if (occupied[i] && keys[i] == *key) {
            occupied[i] = false;
            return 0;
        }
    }
    return -2;
}
static void set_nvme_controller(struct io_health_event *event, u64 ctrl)
{
    assert(ctrl == 17 || ctrl == 23);
    memcpy(event->controller, ctrl == 17 ? "nvme7" : "nvme8", 6);
}
static void submit_event(struct pt_regs *ctx, struct io_health_event *event)
{
    events++;
    last_event = *event;
}
` + probes + `
int main(void)
{
    struct pt_regs ctx = {};
    kprobe_nvme_reset(&ctx, (void *)17);
    assert(events == 0);
    ctx.result = -16; /* An already-resetting or deleting controller rejects it. */
    kretprobe_nvme_reset(&ctx);
    assert(events == 0 && !occupied[0] && !occupied[1]);

    for (unsigned i = 0; i < 2; i++) {
        ctx.result = 0;
        kprobe_nvme_reset(&ctx, (void *)17);
        assert(events == i);
        kretprobe_nvme_reset(&ctx);
        assert(events == i + 1);
        assert(last_event.type == HEALTH_EVENT_NVME_RESET);
        assert(last_event.topology_generation == 9);
        assert(!strcmp((char *)last_event.controller, "nvme7"));
        assert(!occupied[0] && !occupied[1]);
    }
    kretprobe_nvme_reset(&ctx); /* No matching entry must not invent an event. */
    assert(events == 2);

    kprobe_nvme_reset(&ctx, (void *)17);
    task = 2;
    kprobe_nvme_reset(&ctx, (void *)23);
    kretprobe_nvme_reset(&ctx);
    assert(events == 3 && !strcmp((char *)last_event.controller, "nvme8"));
    task = 1;
    ctx.result = -16;
    kretprobe_nvme_reset(&ctx);
    assert(events == 3 && !occupied[0] && !occupied[1]);
    return 0;
}
`
	executable := compileIOControlTestC(
		t,
		"cc",
		"io-health-nvme-reset",
		program,
		"-I"+filepath.Join("..", "..", "bpf", "include"),
	)
	output, err := exec.CommandContext(t.Context(), executable).CombinedOutput()
	require.NoError(t, err, "%s", output)
}
