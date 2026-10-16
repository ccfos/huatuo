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

// Native fixtures exercise the production freeze probes with both queue layouts.
package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"

	"github.com/stretchr/testify/require"
)

func TestIOHealthFreezeKernel(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to load and attach freeze probes")
	}
	require.NoError(t, bpf.Init(&bpf.Option{}))
	t.Cleanup(bpf.Shutdown)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })
	_, quietMask := loadIOHealthKernelEnums()
	object, err := bpf.LoadBPF("io_health.o", map[string]any{"io_health_rqf_quiet_mask": quietMask})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, object.Close()) })
	symbol := "blk_mq_freeze_queue"
	if !bpf.HasKprobeFunction(symbol) {
		symbol = "blk_mq_freeze_queue_nomemsave"
	}
	for _, option := range []bpf.AttachOption{
		{ProgramName: "io_health_freeze_disk_removed", Symbol: "del_gendisk"},
		{ProgramName: "io_health_disk_publishing", Symbol: "device_add"},
		{ProgramName: "io_health_freeze_disk_added", Symbol: "blk_register_queue"},
		{ProgramName: "io_health_freeze_queue", Symbol: symbol},
	} {
		require.NoError(t, object.AttachIndependently(&option))
	}
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
