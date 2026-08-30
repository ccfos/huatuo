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
 * Measure each throttled bio from its first admission to the final top-level
 * pop. Transfers between service queues retain the start time; final pop
 * settles one count and the complete wait before deleting pending state.
 *
 * State lifetime follows blk_throtl_exit and throtl_pd_free. td retirement
 * lets userspace discard and reclaim only that td's rows; blkg release removes
 * its saved owner and aggregate rows. Kernel pointers and CSS serials isolate
 * raw counters, while major:minor identifies the public disk metrics.
 */

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_blkio.h"
#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define THROTL_PENDING_ENTRIES 10240
#define THROTL_OBJECT_ENTRIES 4096
#define THROTL_ENOENT 2
#define THROTL_EEXIST 17

/*
 * Capacity model: at the default 60s scrape interval, allow one miss.
 * A lane can first complete 10240 bios that already waited 60s,
 * then reuse those slots during the resulting 120s interval.
 * 38 wait bits at 10us cover the resulting 1,843,200s total.
 * 26 count bits cover 67,108,864 delayed bios per CPU/key,
 * or about 559K/s over 120s. Longer intervals may undercount.
 */
#define THROTL_WAIT_COUNT_BITS 26
#define THROTL_WAIT_10US_BITS 38
#define THROTL_WAIT_UNIT_NS 10000ULL
#define THROTL_WAIT_COUNT_MASK \
	((1ULL << THROTL_WAIT_COUNT_BITS) - 1)
#define THROTL_WAIT_10US_MASK \
	((1ULL << THROTL_WAIT_10US_BITS) - 1)

#ifndef REQ_OP_MASK
#define REQ_OP_MASK ((1U << 8) - 1)
#endif

enum throtl_td_lifecycle {
	THROTL_TD_ACTIVE = 1,
	THROTL_TD_RETIRED = 2,
};

/* Go resolves these values from live kallsyms before load. */
volatile const u64 throtl_tracked_caller_start = 0;
volatile const u64 throtl_tracked_caller_end = 0;
volatile const u64 throtl_ignored_caller_start = 0;
volatile const u64 throtl_ignored_caller_end = 0;

struct throtl_td_state {
	u32 state;
	u32 reserved;
	union {
		struct {
			u32 major;
			u32 minor;
		};
		u64 device;
	};
};

struct throtl_blkg_owner {
	u64 td;
	u64 css;
	u64 css_serial;
};

struct throtl_pending_key {
	u64 td;
	u64 bio;
};

struct throtl_pending {
	u64 start_ns;
};

struct throtl_wait_key {
	u64 td;
	u64 blkg;
	u64 css;
	u64 css_serial;
	u32 operation;
	u32 reserved;
};

struct throtl_pop_call_state {
	u64 source;
};

enum throtl_failure {
	THROTL_FAILURE_PENDING_COLLISION = 1,
	THROTL_FAILURE_PENDING_INSERT,
	THROTL_FAILURE_PENDING_DELETE,
	THROTL_FAILURE_OWNER,
	THROTL_FAILURE_AGGREGATE,
	THROTL_FAILURE_UNKNOWN_CALLER,
	THROTL_FAILURE_TIME_ROLLBACK,
	THROTL_FAILURE_POP_STATE,
	THROTL_FAILURE_LIFECYCLE,
};

struct throtl_status {
	u64 failure; /* High 32-bit reason, low signed 32-bit helper errno. */
};

_Static_assert(sizeof(struct throtl_td_state) == 16,
	       "throtl_td_state ABI");
_Static_assert(__builtin_offsetof(struct throtl_td_state, device) == 8,
	       "throtl_td_state device alignment");
_Static_assert(sizeof(struct throtl_blkg_owner) == 24,
	       "throtl_blkg_owner ABI");
_Static_assert(sizeof(struct throtl_pending_key) == 16,
	       "throtl_pending_key ABI");
_Static_assert(sizeof(struct throtl_pending) == 8, "throtl_pending ABI");
_Static_assert(sizeof(struct throtl_wait_key) == 40,
	       "throtl_wait_key ABI");
_Static_assert(THROTL_WAIT_COUNT_BITS + THROTL_WAIT_10US_BITS == 64,
	       "throtl_wait_counter bit layout");
_Static_assert(sizeof(struct throtl_pop_call_state) == 8,
	       "throtl_pop_call_state ABI");
_Static_assert(sizeof(struct throtl_status) == 8, "throtl_status ABI");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct throtl_td_state);
	__uint(max_entries, THROTL_OBJECT_ENTRIES);
} throtl_td_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct throtl_blkg_owner);
	__uint(max_entries, THROTL_OBJECT_ENTRIES);
} throtl_blkg_owner_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, struct throtl_pending_key);
	__type(value, struct throtl_pending);
	__uint(max_entries, THROTL_PENDING_ENTRIES);
} throtl_pending_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct throtl_pop_call_state);
	__uint(max_entries, THROTL_OBJECT_ENTRIES);
} throtl_pop_call_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__type(key, struct throtl_wait_key);
	__type(value, u64);
	/* Each owner can complete both read and write waits. */
	__uint(max_entries, THROTL_OBJECT_ENTRIES * 2);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} throtl_wait_agg_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, struct throtl_status);
	__uint(max_entries, 1);
} throtl_stat_map SEC(".maps");

static __always_inline struct throtl_status *throtl_status(void)
{
	u32 zero = 0;

	return bpf_map_lookup_elem(&throtl_stat_map, &zero);
}

static __always_inline bool throtl_unhealthy(void)
{
	struct throtl_status *status = throtl_status();

	return !status || status->failure != 0;
}

/*
 * Publish one concrete reason and signed helper errno together. Concurrent
 * failures may replace the diagnostic, but never restore a healthy zero.
 */
static __always_inline void throtl_fail(u32 reason, int error)
{
	struct throtl_status *status = throtl_status();

	if (status)
		status->failure = ((u64)reason << 32) | (u32)error;
}

/*
 * Keep helper results 32-bit: target kernels may zero-extend the map
 * implementation's int errno before returning it to BPF.
 */
static __always_inline bool throtl_delete_pending(const struct throtl_pending_key *key)
{
	int ret = bpf_map_delete_elem(&throtl_pending_map, key);

	if (!ret || ret == -THROTL_ENOENT)
		return true;
	throtl_fail(THROTL_FAILURE_PENDING_DELETE, ret);
	return false;
}

static __always_inline bool throtl_delete_aggregate(
	const struct throtl_wait_key *key)
{
	int ret = bpf_map_delete_elem(&throtl_wait_agg_map, key);

	if (!ret || ret == -THROTL_ENOENT)
		return true;
	throtl_fail(THROTL_FAILURE_AGGREGATE, ret);
	return false;
}

static __always_inline bool throtl_delete_owner(u64 blkg)
{
	int ret = bpf_map_delete_elem(&throtl_blkg_owner_map, &blkg);

	if (!ret || ret == -THROTL_ENOENT)
		return true;
	throtl_fail(THROTL_FAILURE_OWNER, ret);
	return false;
}

static __always_inline bool throtl_delete_blkg_aggregates(
	u64 blkg, const struct throtl_blkg_owner *owner)
{
	struct throtl_wait_key key = {
		.td = owner->td,
		.blkg = blkg,
		.css = owner->css,
		.css_serial = owner->css_serial,
		.operation = REQ_OP_READ,
	};
	bool read_deleted = throtl_delete_aggregate(&key);
	bool write_deleted;

	key.operation = REQ_OP_WRITE;
	write_deleted = throtl_delete_aggregate(&key);
	return read_deleted && write_deleted;
}

static __always_inline void throtl_delete_blkg_state(
	u64 blkg, const struct throtl_blkg_owner *owner)
{
	if (throtl_delete_blkg_aggregates(blkg, owner))
		throtl_delete_owner(blkg);
}

static __always_inline struct throtl_td_state *throtl_lookup_td(u64 td)
{
	return bpf_map_lookup_elem(&throtl_td_map, &td);
}

static __always_inline struct throtl_td_state *throtl_ensure_active_td(
	u64 td, struct gendisk *disk)
{
	struct throtl_td_state initial = {
		.state = THROTL_TD_ACTIVE,
	};
	struct throtl_td_state *current;
	int ret = 0;

	if (!td || !disk) {
		throtl_fail(THROTL_FAILURE_LIFECYCLE, 0);
		return NULL;
	}
	initial.major = BPF_CORE_READ(disk, major);
	initial.minor = BPF_CORE_READ(disk, first_minor);

	current = throtl_lookup_td(td);
	if (!current) {
		ret = bpf_map_update_elem(&throtl_td_map, &td, &initial,
				    COMPAT_BPF_NOEXIST);
		current = throtl_lookup_td(td);
	}
	if (!current) {
		throtl_fail(THROTL_FAILURE_LIFECYCLE, ret);
		return NULL;
	}
	if (current->state == THROTL_TD_RETIRED)
		return NULL;
	if (current->state != THROTL_TD_ACTIVE) {
		throtl_fail(THROTL_FAILURE_LIFECYCLE, 0);
		return NULL;
	}
	/* Publish the major:minor pair with one aligned 64-bit store. */
	if (current->device != initial.device)
		current->device = initial.device;
	return current;
}

static __always_inline bool throtl_td_is_active(u64 td)
{
	struct throtl_td_state *current = throtl_lookup_td(td);

	if (!current || current->state == THROTL_TD_RETIRED)
		return false;
	if (current->state != THROTL_TD_ACTIVE) {
		throtl_fail(THROTL_FAILURE_LIFECYCLE, 0);
		return false;
	}
	return true;
}

static __always_inline void throtl_retire_td(u64 td)
{
	struct throtl_td_state *current;

	if (!td)
		return;
	current = throtl_lookup_td(td);
	if (!current)
		return;
	if (current->state == THROTL_TD_ACTIVE) {
		current->state = THROTL_TD_RETIRED;
		return;
	}
	if (current->state != THROTL_TD_RETIRED)
		throtl_fail(THROTL_FAILURE_LIFECYCLE, 0);
}

static __always_inline bool throtl_wait_bio_operation(struct bio *bio, u32 *operation)
{
	u32 current = BPF_CORE_READ(bio, bi_opf) & REQ_OP_MASK;
	u64 bytes = BPF_CORE_READ(bio, bi_iter.bi_size);

	if ((current != REQ_OP_READ && current != REQ_OP_WRITE) || !bytes)
		return false;
	*operation = current;
	return true;
}

static __always_inline struct throtl_blkg_owner *throtl_lookup_or_create_owner(
	u64 blkg_key, struct blkcg_gq *blkg, u64 td)
{
	struct throtl_blkg_owner initial = {
		.td = td,
	};
	struct throtl_blkg_owner *owner;
	struct blkcg *blkcg;
	int ret = 0;

	owner = bpf_map_lookup_elem(&throtl_blkg_owner_map, &blkg_key);
	if (!owner) {
		blkcg = BPF_CORE_READ(blkg, blkcg);
		if (!blkcg) {
			throtl_fail(THROTL_FAILURE_OWNER, 0);
			return NULL;
		}
		initial.css = (u64)blkcg + compat_bpf_core_field_offset(
			((struct blkcg *)0)->css);
		initial.css_serial = BPF_CORE_READ(blkcg, css.serial_nr);
		ret = bpf_map_update_elem(&throtl_blkg_owner_map, &blkg_key,
				    &initial, COMPAT_BPF_NOEXIST);
		owner = bpf_map_lookup_elem(&throtl_blkg_owner_map, &blkg_key);
	}
	if (!owner || owner->td != td || !owner->css) {
		throtl_fail(THROTL_FAILURE_OWNER, ret);
		return NULL;
	}
	return owner;
}

static __always_inline bool throtl_account_wait(const struct throtl_wait_key *key,
					 u64 wait_ns)
{
	u64 delayed_count;
	u64 wait_10us;
	u64 current;
	u64 zero = 0;
	u64 *counter;
	int ret = 0;

	counter = bpf_map_lookup_elem(&throtl_wait_agg_map, key);
	if (!counter) {
		ret = bpf_map_update_elem(&throtl_wait_agg_map, key, &zero,
				    COMPAT_BPF_NOEXIST);
		counter = bpf_map_lookup_elem(&throtl_wait_agg_map, key);
	}
	if (!counter) {
		throtl_fail(THROTL_FAILURE_AGGREGATE, ret);
		return false;
	}

	current = *counter;
	delayed_count = ((current >> THROTL_WAIT_10US_BITS) + 1) &
			THROTL_WAIT_COUNT_MASK;
	wait_10us = ((current & THROTL_WAIT_10US_MASK) +
		     wait_ns / THROTL_WAIT_UNIT_NS) &
		    THROTL_WAIT_10US_MASK;
	*counter = (delayed_count << THROTL_WAIT_10US_BITS) | wait_10us;
	return true;
}

SEC("kprobe/throtl_add_bio_tg")
int kprobe_throtl_add_bio_tg(struct pt_regs *ctx)
{
	struct throtl_pending initial = {};
	struct throtl_pending_key key = {};
	struct throtl_td_state *state;
	struct throtl_grp *tg;
	struct gendisk *disk;
	struct bio *bio;
	u32 operation;
	u64 caller = 0;
	int ret;

	BPF_KPROBE_READ_RET_IP(caller, ctx);
	if (caller >= throtl_ignored_caller_start &&
	    caller < throtl_ignored_caller_end)
		return 0;
	if (caller < throtl_tracked_caller_start ||
	    caller >= throtl_tracked_caller_end) {
		throtl_fail(THROTL_FAILURE_UNKNOWN_CALLER, 0);
		return 0;
	}
	if (throtl_unhealthy())
		return 0;

	bio = (struct bio *)PT_REGS_PARM1_CORE(ctx);
	if (!bio || !throtl_wait_bio_operation(bio, &operation))
		return 0;
	disk = bio_disk(bio);
	if (!disk)
		return 0;
	tg = (struct throtl_grp *)PT_REGS_PARM3_CORE(ctx);
	key.td = tg ? (u64)BPF_CORE_READ(tg, td) : 0;
	state = throtl_ensure_active_td(key.td, disk);
	if (!state)
		return 0;

	key.bio = (u64)bio;
	initial.start_ns = bpf_ktime_get_ns();
	ret = bpf_map_update_elem(&throtl_pending_map, &key, &initial,
				  COMPAT_BPF_NOEXIST);
	if (ret) {
		throtl_fail(ret == -THROTL_EEXIST ?
			    THROTL_FAILURE_PENDING_COLLISION :
			    THROTL_FAILURE_PENDING_INSERT, ret);
		return 0;
	}
	/* Close a retirement race after publishing the pending entry. */
	state = throtl_lookup_td(key.td);
	if (!state || state->state == THROTL_TD_RETIRED) {
		throtl_delete_pending(&key);
		return 0;
	}
	if (state->state != THROTL_TD_ACTIVE) {
		throtl_fail(THROTL_FAILURE_LIFECYCLE, 0);
		throtl_delete_pending(&key);
	}
	return 0;
}

static __always_inline void throtl_save_pop_call(u64 source)
{
	struct throtl_pop_call_state call = {
		.source = source,
	};
	u64 task = (u64)bpf_get_current_task();
	int ret;

	ret = bpf_map_update_elem(&throtl_pop_call_map, &task, &call,
				  COMPAT_BPF_NOEXIST);
	if (ret)
		throtl_fail(THROTL_FAILURE_POP_STATE, ret);
}

static __always_inline bool throtl_pop_uses_queued_list(void)
{
	/* The v6.16 pop ABI change replaced nr_queued in the same commit. */
	return bpf_core_field_exists(
		((struct throtl_service_queue *)0)->nr_queued);
}

SEC("kprobe/throtl_pop_queued")
int kprobe_throtl_pop_queued(struct pt_regs *ctx)
{
	struct throtl_service_queue *sq;
	u64 td;

	if (PT_REGS_PARM2_CORE(ctx))
		return 0;
	if (throtl_pop_uses_queued_list()) {
		throtl_save_pop_call(PT_REGS_PARM1_CORE(ctx));
		return 0;
	}
	sq = (struct throtl_service_queue *)PT_REGS_PARM1_CORE(ctx);
	if (!sq || BPF_CORE_READ(sq, parent_sq)) {
		throtl_fail(THROTL_FAILURE_POP_STATE, 0);
		return 0;
	}
	td = (u64)sq - compat_bpf_core_field_offset(
		((struct throtl_data *)0)->service_queue);
	throtl_save_pop_call(td);
	return 0;
}

static __always_inline u64 throtl_legacy_pop_td(u64 queued, u32 operation)
{
	u64 offset = compat_bpf_core_field_offset(
		((struct throtl_data *)0)->service_queue.queued[0]);

	if (operation == REQ_OP_WRITE)
		offset += sizeof(struct list_head);
	if (queued < offset)
		return 0;
	return queued - offset;
}

SEC("kretprobe/throtl_pop_queued")
int kretprobe_throtl_pop_queued(struct pt_regs *ctx)
{
	struct throtl_pop_call_state *stored;
	struct throtl_pop_call_state call;
	struct throtl_blkg_owner *owner;
	struct throtl_td_state *state;
	struct throtl_wait_key wait_key = {};
	struct throtl_pending_key pending_key = {};
	struct throtl_pending *pending;
	struct throtl_pending copy;
	struct blkcg_gq *blkg;
	struct bio *bio;
	u64 task = (u64)bpf_get_current_task();
	u64 now;
	u32 operation;
	int ret;

	stored = bpf_map_lookup_elem(&throtl_pop_call_map, &task);
	if (!stored) {
		/* Non-final calls and pre-attachment entries have no state. */
		return 0;
	}
	call = *stored;
	ret = bpf_map_delete_elem(&throtl_pop_call_map, &task);
	if (ret) {
		throtl_fail(THROTL_FAILURE_POP_STATE, ret);
		return 0;
	}
	bio = (struct bio *)PT_REGS_RC(ctx);
	if (!bio || !throtl_wait_bio_operation(bio, &operation))
		return 0;
	if (throtl_pop_uses_queued_list())
		pending_key.td = throtl_legacy_pop_td(call.source, operation);
	else
		pending_key.td = call.source;
	if (!pending_key.td) {
		throtl_fail(THROTL_FAILURE_POP_STATE, 0);
		return 0;
	}
	pending_key.bio = (u64)bio;
	pending = bpf_map_lookup_elem(&throtl_pending_map, &pending_key);
	if (!pending)
		return 0;
	copy = *pending;

	if (throtl_unhealthy()) {
		throtl_delete_pending(&pending_key);
		return 0;
	}
	state = throtl_lookup_td(pending_key.td);
	if (!state || state->state == THROTL_TD_RETIRED) {
		throtl_delete_pending(&pending_key);
		return 0;
	}
	if (state->state != THROTL_TD_ACTIVE) {
		throtl_fail(THROTL_FAILURE_LIFECYCLE, 0);
		throtl_delete_pending(&pending_key);
		return 0;
	}
	now = bpf_ktime_get_ns();
	if (now < copy.start_ns) {
		throtl_fail(THROTL_FAILURE_TIME_ROLLBACK, 0);
		throtl_delete_pending(&pending_key);
		return 0;
	}

	blkg = BPF_CORE_READ(bio, bi_blkg);
	if (!blkg) {
		throtl_fail(THROTL_FAILURE_OWNER, 0);
		throtl_delete_pending(&pending_key);
		return 0;
	}
	if (!throtl_td_is_active(pending_key.td)) {
		throtl_delete_pending(&pending_key);
		return 0;
	}
	owner = throtl_lookup_or_create_owner((u64)blkg, blkg, pending_key.td);
	if (!owner) {
		throtl_delete_pending(&pending_key);
		return 0;
	}
	wait_key.td = pending_key.td;
	wait_key.blkg = (u64)blkg;
	wait_key.css = owner->css;
	wait_key.css_serial = owner->css_serial;
	wait_key.operation = operation;
	if (!throtl_account_wait(&wait_key, now - copy.start_ns)) {
		throtl_delete_pending(&pending_key);
		return 0;
	}

	/* Retirement discards every row owned by this td. */
	if (!throtl_td_is_active(pending_key.td)) {
		struct throtl_blkg_owner copy = *owner;

		throtl_delete_blkg_aggregates(wait_key.blkg, &copy);
		throtl_delete_pending(&pending_key);
		return 0;
	}
	throtl_delete_pending(&pending_key);
	return 0;
}

SEC("kprobe/throtl_pd_free")
int kprobe_throtl_pd_free(struct pt_regs *ctx)
{
	struct throtl_blkg_owner *owner;
	struct throtl_blkg_owner copy;
	struct blkg_policy_data *pd;
	u64 blkg;

	pd = (struct blkg_policy_data *)PT_REGS_PARM1_CORE(ctx);
	blkg = pd ? (u64)BPF_CORE_READ(pd, blkg) : 0;
	if (!blkg)
		return 0;
	owner = bpf_map_lookup_elem(&throtl_blkg_owner_map, &blkg);
	if (!owner)
		return 0;
	copy = *owner;
	throtl_delete_blkg_state(blkg, &copy);
	return 0;
}

/*
 * blk_throtl_exit() takes request_queue * in the queue-based ABI and
 * gendisk * in the disk-based ABI. Only argument decoding differs: both
 * retire the q->td instance released by the kernel callback.
 */
SEC("kprobe/blk_throtl_exit_legacy")
int kprobe_blk_throtl_exit_legacy(struct pt_regs *ctx)
{
	struct request_queue *q;
	u64 td;

	q = (struct request_queue *)PT_REGS_PARM1_CORE(ctx);
	td = q ? (u64)BPF_CORE_READ(q, td) : 0;
	throtl_retire_td(td);
	return 0;
}

SEC("kprobe/blk_throtl_exit_mainline")
int kprobe_blk_throtl_exit_mainline(struct pt_regs *ctx)
{
	struct request_queue *q;
	struct gendisk *disk;
	u64 td;

	/*
	 * blk_throtl_cancel_bios() bypasses final pop. Canceled pending rows remain
	 * keyed by td until this exit boundary, which gendisk references may delay.
	 */
	disk = (struct gendisk *)PT_REGS_PARM1_CORE(ctx);
	q = disk ? BPF_CORE_READ(disk, queue) : NULL;
	/* Retire the same q->td object that blk_throtl_exit() tears down. */
	td = q ? (u64)BPF_CORE_READ(q, td) : 0;
	throtl_retire_td(td);
	return 0;
}
