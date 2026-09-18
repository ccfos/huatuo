/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/* Shared IO device notifications and throttle/IOCOST wait word.
 * Publish one aligned word so every IO's
 * count stays paired with its wait. Each episode loses less than 10us.
 * The independent moduli are 67,108,864 IOs and 2,748,779.06944s of wait per
 * CPU/key; interval increments beyond either capacity may be undercounted.
 * Completion includes the whole wait, including prior collection periods.
 */
#ifndef __BPF_IOCONTROL_H__
#define __BPF_IOCONTROL_H__

#define IO_CONTROL_WAIT_COUNT_BITS 26
#define IO_CONTROL_WAIT_10US_BITS 38
#define IO_CONTROL_WAIT_UNIT_NS 10000ULL
#define IO_CONTROL_WAIT_COUNT_MASK ((1ULL << IO_CONTROL_WAIT_COUNT_BITS) - 1)
#define IO_CONTROL_WAIT_10US_MASK ((1ULL << IO_CONTROL_WAIT_10US_BITS) - 1)

/* Kernel CSS serial identifies a counter lifetime; userspace owns its delta. */
struct io_control_wait_key {
	u64 css;
	u64 css_serial;
	u32 major;
	u32 minor;
	u32 operation;
	u32 reserved;
};

struct io_control_status {
	u64 failure;
	u64 devices_changed;
};

_Static_assert(sizeof(struct io_control_wait_key) == 32,
	       "IO wait key ABI");
_Static_assert(sizeof(struct io_control_status) == 16,
	       "IO session status ABI");

_Static_assert(IO_CONTROL_WAIT_COUNT_BITS + IO_CONTROL_WAIT_10US_BITS == 64,
	       "IO wait counter bit layout");

static __always_inline u64 io_control_wait_add(u64 packed, u64 wait_ns)
{
	u64 count = ((packed >> IO_CONTROL_WAIT_10US_BITS) + 1) &
		    IO_CONTROL_WAIT_COUNT_MASK;
	u64 wait = ((packed & IO_CONTROL_WAIT_10US_MASK) +
		    wait_ns / IO_CONTROL_WAIT_UNIT_NS) & IO_CONTROL_WAIT_10US_MASK;

	return (count << IO_CONTROL_WAIT_10US_BITS) | wait;
}

/* Disk changes discard the whole collection session. Ignore loop/ram/zram
 * additions, but any real disk destruction invalidates its counters.
 * Ordinary loop backing-file binding/unbinding does not destroy the disk.
 * Observe registration at entry so no return-argument state is needed.
 * Keep the notification sticky until its object is closed; ordinary IO needs
 * no lifecycle checks.
 * Instantiate the hooks once in each IO tracing object.
 */
#define IO_CONTROL_DEVICE_HOOKS()                                            \
struct {                                                                   \
	__uint(type, BPF_MAP_TYPE_ARRAY);                                    \
	__uint(max_entries, 1);                                             \
	__type(key, u32);                                                   \
	__type(value, struct io_control_status);                            \
} io_control_status_map SEC(".maps");                                       \
static __always_inline struct io_control_status *io_control_status_value(void) \
{                                                                          \
	u32 key = 0;                                                       \
	return bpf_map_lookup_elem(&io_control_status_map, &key);            \
}                                                                          \
static __always_inline void                                                \
io_control_note_device_change(struct gendisk *disk, bool removed)           \
{                                                                          \
	char name[5] = {};                                                 \
	struct io_control_status *status;                                  \
	if (!removed && disk &&                                            \
	    !bpf_core_read(name, sizeof(name), &disk->disk_name) &&          \
	    ((name[0] == 'l' && name[1] == 'o' && name[2] == 'o' &&          \
	      name[3] == 'p') ||                                           \
	     (name[0] == 'r' && name[1] == 'a' && name[2] == 'm') ||         \
	     (name[0] == 'z' && name[1] == 'r' && name[2] == 'a' &&          \
	      name[3] == 'm')))                                            \
		return;                                                     \
	status = io_control_status_value();                               \
	if (status)                                                        \
		status->devices_changed = 1;                               \
}                                                                          \
SEC("kprobe/del_gendisk")                                                   \
int io_control_device_removed(struct pt_regs *ctx)                           \
{                                                                          \
	io_control_note_device_change((void *)PT_REGS_PARM1_CORE(ctx), true); \
	return 0;                                                          \
}                                                                          \
SEC("kprobe/blk_register_queue")                                           \
int io_control_device_changed(struct pt_regs *ctx)                           \
{                                                                          \
	io_control_note_device_change((void *)PT_REGS_PARM1_CORE(ctx), false); \
	return 0;                                                          \
}                                                                          \
SEC("kprobe/device_add_disk_no_queue_reg")                                  \
int io_control_disk_added(struct pt_regs *ctx)                               \
{                                                                          \
	io_control_note_device_change((void *)PT_REGS_PARM2_CORE(ctx), false); \
	return 0;                                                          \
}

#endif
