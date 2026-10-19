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

/*
 * Measure each blk-throttle bio from its first admission to the final top-level
 * pop. Transfers between service queues retain the start time; final pop
 * settles one count and the complete wait before deleting pending state.
 *
 * Device changes end the whole session and discard its counters. blkg release
 * removes its saved owner and aggregate rows. Kernel pointers and CSS serials
 * isolate raw counters, while major:minor identifies the public disk metrics.
 */

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_blkio.h"
#include "bpf_common.h"
#include "bpf_iocontrol.h"

char __license[] SEC("license") = "Dual MIT/GPL";

IO_CONTROL_DEVICE_HOOKS()

#define BLK_THROTL_PENDING_ENTRIES 10240
#define BLK_THROTL_OBJECT_ENTRIES 4096
#define BLK_THROTL_ENOENT 2
#define BLK_THROTL_EEXIST 17

#ifndef REQ_OP_MASK
#define REQ_OP_MASK ((1U << 8) - 1)
#endif

/* Go resolves these values from live kallsyms before load. */
volatile const u64 blk_throtl_tracked_caller_start = 0;
volatile const u64 blk_throtl_tracked_caller_end = 0;
volatile const u64 blk_throtl_ignored_caller_start = 0;
volatile const u64 blk_throtl_ignored_caller_end = 0;

/* Cache the identity so pd_free can retire rows without following the queue. */
struct blk_throtl_blkg_owner {
	u64 css;
	u64 css_serial;
	u32 major;
	u32 minor;
};

_Static_assert(sizeof(struct blk_throtl_blkg_owner) == 24,
	       "blk_throtl_blkg_owner ABI");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct blk_throtl_blkg_owner);
	__uint(max_entries, BLK_THROTL_OBJECT_ENTRIES);
} blk_throtl_blkg_owner_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, u64);
	__uint(max_entries, BLK_THROTL_PENDING_ENTRIES);
} blk_throtl_pending_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, bool);
	__uint(max_entries, BLK_THROTL_OBJECT_ENTRIES);
} blk_throtl_pop_call_map SEC(".maps");

/* The loader preallocates these values on bpf_mem_alloc kernels, where an
 * IRQ-off dispatch loop can exhaust the cache before its refill can run.
 * Legacy sparse maps retain RCU protection for single-lookup readers.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__type(key, struct io_control_wait_key);
	__type(value, u64);
	/* Each owner can complete both read and write waits. */
	__uint(max_entries, BLK_THROTL_OBJECT_ENTRIES * 2);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} blk_throtl_wait_agg_map SEC(".maps");

static __always_inline bool blk_throtl_unhealthy(void)
{
	struct io_control_status *status = io_control_status_value();

	return !status || status->failure != 0;
}

/*
 * Publish one concrete reason and signed helper errno together. Concurrent
 * failures may replace the diagnostic, but never restore a healthy zero.
 */
static __always_inline void blk_throtl_fail(u32 reason, int error)
{
	struct io_control_status *status = io_control_status_value();

	if (status)
		status->failure = ((u64)reason << 32) | (u32)error;
}

/*
 * Keep helper results 32-bit: target kernels may zero-extend the map
 * implementation's int errno before returning it to BPF.
 */
static __always_inline bool blk_throtl_delete_pending(u64 bio)
{
	int ret = bpf_map_delete_elem(&blk_throtl_pending_map, &bio);

	if (!ret || ret == -BLK_THROTL_ENOENT)
		return true;
	blk_throtl_fail(BLK_THROTL_FAILURE_PENDING_DELETE, ret);
	return false;
}

static __always_inline bool blk_throtl_delete_aggregate(
	const struct io_control_wait_key *key)
{
	int ret = bpf_map_delete_elem(&blk_throtl_wait_agg_map, key);

	if (!ret || ret == -BLK_THROTL_ENOENT)
		return true;
	blk_throtl_fail(BLK_THROTL_FAILURE_AGGREGATE, ret);
	return false;
}

static __always_inline bool blk_throtl_delete_owner(u64 blkg)
{
	int ret = bpf_map_delete_elem(&blk_throtl_blkg_owner_map, &blkg);

	if (!ret || ret == -BLK_THROTL_ENOENT)
		return true;
	blk_throtl_fail(BLK_THROTL_FAILURE_OWNER, ret);
	return false;
}

static __always_inline bool blk_throtl_delete_blkg_aggregates(
	const struct blk_throtl_blkg_owner *owner)
{
	struct io_control_wait_key key = {
		.css = owner->css,
		.css_serial = owner->css_serial,
		.major = owner->major,
		.minor = owner->minor,
		.operation = REQ_OP_READ,
	};
	bool read_deleted = blk_throtl_delete_aggregate(&key);
	bool write_deleted;

	key.operation = REQ_OP_WRITE;
	write_deleted = blk_throtl_delete_aggregate(&key);
	return read_deleted && write_deleted;
}

static __always_inline void blk_throtl_delete_blkg_state(
	u64 blkg, const struct blk_throtl_blkg_owner *owner)
{
	if (blk_throtl_delete_blkg_aggregates(owner))
		blk_throtl_delete_owner(blkg);
}

static __always_inline bool blk_throtl_wait_bio_operation(struct bio *bio, u32 *operation)
{
	u32 current = BPF_CORE_READ(bio, bi_opf) & REQ_OP_MASK;
	u64 bytes = BPF_CORE_READ(bio, bi_iter.bi_size);

	if ((current != REQ_OP_READ && current != REQ_OP_WRITE) || !bytes)
		return false;
	*operation = current;
	return true;
}

static __always_inline struct blk_throtl_blkg_owner *blk_throtl_lookup_or_create_owner(
	u64 blkg_key, struct blkcg_gq *blkg, struct bio *bio)
{
	struct blk_throtl_blkg_owner initial = {};
	struct blk_throtl_blkg_owner *owner;
	struct gendisk *disk;
	struct blkcg *blkcg;
	int ret = 0;

	owner = bpf_map_lookup_elem(&blk_throtl_blkg_owner_map, &blkg_key);
	if (!owner) {
		blkcg = BPF_CORE_READ(blkg, blkcg);
		disk = bio_disk(bio);
		if (!blkcg || !disk) {
			blk_throtl_fail(BLK_THROTL_FAILURE_OWNER, 0);
			return NULL;
		}
		initial.css = (u64)blkcg + compat_bpf_core_field_offset(
			((struct blkcg *)0)->css);
		initial.css_serial = BPF_CORE_READ(blkcg, css.serial_nr);
		initial.major = BPF_CORE_READ(disk, major);
		initial.minor = BPF_CORE_READ(disk, first_minor);
		ret = bpf_map_update_elem(&blk_throtl_blkg_owner_map, &blkg_key,
				    &initial, COMPAT_BPF_NOEXIST);
		owner = bpf_map_lookup_elem(&blk_throtl_blkg_owner_map, &blkg_key);
	}
	if (!owner) {
		blk_throtl_fail(BLK_THROTL_FAILURE_OWNER, ret);
		return NULL;
	}
	return owner;
}

static __always_inline void blk_throtl_account_wait(const struct io_control_wait_key *key,
					 u64 wait_ns)
{
	u64 zero = 0;
	u64 *counter;
	int ret = 0;

	counter = bpf_map_lookup_elem(&blk_throtl_wait_agg_map, key);
	if (!counter) {
		ret = bpf_map_update_elem(&blk_throtl_wait_agg_map, key, &zero,
				    COMPAT_BPF_NOEXIST);
		counter = bpf_map_lookup_elem(&blk_throtl_wait_agg_map, key);
	}
	if (!counter) {
		blk_throtl_fail(BLK_THROTL_FAILURE_AGGREGATE, ret);
		return;
	}

	*counter = io_control_wait_add(*counter, wait_ns);
}

SEC("kprobe/throtl_add_bio_tg")
int kprobe_blk_throtl_add_bio_tg(struct pt_regs *ctx)
{
	struct bio *bio;
	u32 operation;
	u64 start_ns;
	u64 key;
	u64 caller = 0;
	int ret;

	BPF_KPROBE_READ_RET_IP(caller, ctx);
	if (caller >= blk_throtl_ignored_caller_start &&
	    caller < blk_throtl_ignored_caller_end)
		return 0;
	if (caller < blk_throtl_tracked_caller_start ||
	    caller >= blk_throtl_tracked_caller_end) {
		blk_throtl_fail(BLK_THROTL_FAILURE_UNKNOWN_CALLER, 0);
		return 0;
	}
	if (blk_throtl_unhealthy())
		return 0;

	bio = (struct bio *)PT_REGS_PARM1_CORE(ctx);
	if (!bio || !blk_throtl_wait_bio_operation(bio, &operation))
		return 0;
	key = (u64)bio;
	start_ns = bpf_ktime_get_ns();
	ret = bpf_map_update_elem(&blk_throtl_pending_map, &key, &start_ns,
				  COMPAT_BPF_NOEXIST);
	if (ret) {
		blk_throtl_fail(ret == -BLK_THROTL_EEXIST ?
			    BLK_THROTL_FAILURE_PENDING_COLLISION :
			    BLK_THROTL_FAILURE_PENDING_INSERT, ret);
		return 0;
	}
	return 0;
}

SEC("kprobe/throtl_pop_queued")
int kprobe_blk_throtl_pop_queued(struct pt_regs *ctx)
{
	u64 task = (u64)bpf_get_current_task();
	bool final = true;
	int ret;

	if (PT_REGS_PARM2_CORE(ctx))
		return 0;
	ret = bpf_map_update_elem(&blk_throtl_pop_call_map, &task, &final,
				  COMPAT_BPF_NOEXIST);
	if (ret)
		blk_throtl_fail(BLK_THROTL_FAILURE_POP_STATE, ret);
	return 0;
}

SEC("kretprobe/throtl_pop_queued")
int kretprobe_blk_throtl_pop_queued(struct pt_regs *ctx)
{
	struct blk_throtl_blkg_owner *owner;
	struct io_control_wait_key wait_key = {};
	struct blkcg_gq *blkg;
	struct bio *bio;
	u64 *pending;
	u64 task = (u64)bpf_get_current_task();
	u64 pending_key;
	u64 start_ns;
	u64 now;
	u32 operation;
	int ret;

	if (!bpf_map_lookup_elem(&blk_throtl_pop_call_map, &task)) {
		/* Non-final calls and pre-attachment entries have no state. */
		return 0;
	}
	ret = bpf_map_delete_elem(&blk_throtl_pop_call_map, &task);
	if (ret) {
		blk_throtl_fail(BLK_THROTL_FAILURE_POP_STATE, ret);
		return 0;
	}
	bio = (struct bio *)PT_REGS_RC(ctx);
	if (!bio || !blk_throtl_wait_bio_operation(bio, &operation))
		return 0;
	pending_key = (u64)bio;
	pending = bpf_map_lookup_elem(&blk_throtl_pending_map, &pending_key);
	if (!pending)
		return 0;
	start_ns = *pending;

	if (blk_throtl_unhealthy())
		goto out;
	now = bpf_ktime_get_ns();
	if (now < start_ns) {
		blk_throtl_fail(BLK_THROTL_FAILURE_TIME_ROLLBACK, 0);
		goto out;
	}

	blkg = BPF_CORE_READ(bio, bi_blkg);
	if (!blkg) {
		blk_throtl_fail(BLK_THROTL_FAILURE_OWNER, 0);
		goto out;
	}
	owner = blk_throtl_lookup_or_create_owner((u64)blkg, blkg, bio);
	if (!owner)
		goto out;
	wait_key.css = owner->css;
	wait_key.css_serial = owner->css_serial;
	wait_key.major = owner->major;
	wait_key.minor = owner->minor;
	wait_key.operation = operation;
	blk_throtl_account_wait(&wait_key, now - start_ns);

out:
	blk_throtl_delete_pending(pending_key);
	return 0;
}

SEC("kprobe/throtl_pd_free")
int kprobe_blk_throtl_pd_free(struct pt_regs *ctx)
{
	struct blk_throtl_blkg_owner *owner;
	struct blk_throtl_blkg_owner copy;
	struct blkg_policy_data *pd;
	u64 blkg;

	pd = (struct blkg_policy_data *)PT_REGS_PARM1_CORE(ctx);
	blkg = pd ? (u64)BPF_CORE_READ(pd, blkg) : 0;
	if (!blkg)
		return 0;
	owner = bpf_map_lookup_elem(&blk_throtl_blkg_owner_map, &blkg);
	if (!owner)
		return 0;
	copy = *owner;
	blk_throtl_delete_blkg_state(blkg, &copy);
	return 0;
}
