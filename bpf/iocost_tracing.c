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
 * Account complete residence times for bios which enter an IOCOST waitq.
 * The running kernel's BTF relocates the local flavor structs below; no
 * target-kernel vmlinux.h is embedded in the object.
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

#define IOCOST_LIVE_ENTRIES 4096
/* Active and retired read/write rows share this bounded capacity. */
#define IOCOST_WAIT_AGG_ENTRIES (IOCOST_LIVE_ENTRIES * 2)
#define IOCOST_PENDING_ENTRIES 10240
#define IOCOST_ENOENT 2
#define IOCOST_EEXIST 17
#ifndef REQ_OP_MASK
#define REQ_OP_MASK ((1U << 8) - 1)
#endif

/* Go resolves these values from live kallsyms before load. */
volatile const u64 iocost_throttle_caller_start = 0;
volatile const u64 iocost_throttle_caller_end = 0;
volatile const u64 iocost_over_budget_caller_start = 0;
volatile const u64 iocost_over_budget_caller_end = 0;

/* CO-RE waiter fields are not declared by the baseline header. */
struct iocg_wait___iocost {
	struct wait_queue_entry wait;
	struct bio *bio;
} __attribute__((preserve_access_index));

struct iocg_wake_ctx___iocost {
	struct ioc_gq *iocg;
} __attribute__((preserve_access_index));

struct ioc_gq___iocost {
	struct blkg_policy_data pd;
	struct wait_queue_head waitq;
} __attribute__((preserve_access_index));

struct iocost_owner_state {
	u64 css;
	u64 css_serial;
	u32 major;
	u32 minor;
};

struct iocost_pending {
	u64 start_ns;
	u32 operation;
	u32 reserved;
};

struct iocost_wake_frame {
	u64 bio_ptr; /* Published last; zero means the frame is idle. */
	u64 end_ns;
	struct iocost_pending pending;
	struct io_control_wait_key key;
};

#define IOCOST_ASSERT_OFFSET(type, field, expected)                           \
	_Static_assert(__builtin_offsetof(struct type, field) == (expected),    \
		       #type "." #field " ABI")

_Static_assert(sizeof(struct iocost_owner_state) == 24,
	       "iocost_owner_state ABI");
IOCOST_ASSERT_OFFSET(iocost_owner_state, css, 0);
IOCOST_ASSERT_OFFSET(iocost_owner_state, css_serial, 8);
IOCOST_ASSERT_OFFSET(iocost_owner_state, major, 16);
IOCOST_ASSERT_OFFSET(iocost_owner_state, minor, 20);

_Static_assert(sizeof(struct iocost_pending) == 16, "iocost_pending ABI");
IOCOST_ASSERT_OFFSET(iocost_pending, start_ns, 0);
IOCOST_ASSERT_OFFSET(iocost_pending, operation, 8);
IOCOST_ASSERT_OFFSET(iocost_pending, reserved, 12);

_Static_assert(sizeof(struct iocost_wake_frame) == 64,
	       "iocost_wake_frame ABI");
IOCOST_ASSERT_OFFSET(iocost_wake_frame, bio_ptr, 0);
IOCOST_ASSERT_OFFSET(iocost_wake_frame, end_ns, 8);
IOCOST_ASSERT_OFFSET(iocost_wake_frame, pending, 16);
IOCOST_ASSERT_OFFSET(iocost_wake_frame, key, 32);

/* One IRQ-off timer can first observe several groups. Reserve identities at
 * load time instead of depending on the allocator's asynchronous refill.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct iocost_owner_state);
	__uint(max_entries, IOCOST_LIVE_ENTRIES);
} iocost_owner_state_map SEC(".maps");

/* pd_free can run in an RCU callback; reserve retirement records at load. */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, struct io_control_retired_key);
	__type(value, u8);
	__uint(max_entries, IOCOST_LIVE_ENTRIES);
} iocost_retired_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct iocost_pending);
	__uint(max_entries, IOCOST_PENDING_ENTRIES);
} iocost_pending_map SEC(".maps");

/*
 * The loader preallocates on bpf_mem_alloc kernels: wake loops cannot wait for
 * an asynchronous refill. Legacy kernels retain RCU-backed sparse rows for
 * single-lookup readers. Go consumes the final delta before deleting retired
 * rows; ordinary IO never reads retirement state.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__type(key, struct io_control_wait_key);
	__type(value, u64);
	__uint(max_entries, IOCOST_WAIT_AGG_ENTRIES);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} iocost_wait_agg_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, u32);
	__type(value, struct iocost_wake_frame);
	__uint(max_entries, 1);
} iocost_wake_frame_map SEC(".maps");

#define IOCOST_CORE_READ(dst, src, field) \
	(BPF_CORE_READ_INTO((dst), (src), field) == 0)

static __always_inline bool iocost_unhealthy(void)
{
	struct io_control_status *status = io_control_status_value();

	return !status || status->failure != 0;
}

/*
 * Direct map ops can return a zero-extended int even with long helper
 * declarations. Publish one concrete reason and errno together. Concurrent
 * failures may replace the diagnostic, but never restore a healthy zero.
 */
static __always_inline void iocost_fail(u32 reason, int error)
{
	struct io_control_status *status = io_control_status_value();

	if (status)
		status->failure = ((u64)reason << 32) | (u32)error;
}

static __always_inline void iocost_classify_pending_update_ret(int ret)
{
	if (ret)
		iocost_fail(ret == -IOCOST_EEXIST ?
			    IOCOST_FAILURE_PENDING_COLLISION :
			    IOCOST_FAILURE_PENDING_INSERT, ret);
}

static __always_inline bool iocost_range_contains(u64 address, u64 start,
						   u64 end)
{
	return start < end && address >= start && address < end;
}

static __always_inline bool iocost_enqueue_caller(u64 address)
{
	if (iocost_range_contains(address, iocost_throttle_caller_start,
				  iocost_throttle_caller_end))
		return true;
	return iocost_range_contains(address, iocost_over_budget_caller_start,
				     iocost_over_budget_caller_end);
}

/* Returns 1 for READ/WRITE, 0 for an ignored operation and -1 on read error. */
static __always_inline int iocost_read_bio(
	struct bio *raw_bio, u32 *operation)
{
	u32 opf = 0;
	u32 op;

	if (!raw_bio || !IOCOST_CORE_READ(&opf, raw_bio, bi_opf))
		return -1;
	op = opf & REQ_OP_MASK;
	if (op != REQ_OP_READ && op != REQ_OP_WRITE)
		return 0;
	*operation = op;
	return 1;
}

static __always_inline void iocost_retire_aggregate(
	const struct iocost_owner_state *owner)
{
	struct io_control_retired_key key = {
		.css = owner->css,
		.css_serial = owner->css_serial,
		.major = owner->major,
		.minor = owner->minor,
	};
	u8 marker = 1;
	int ret = bpf_map_update_elem(&iocost_retired_map, &key, &marker,
				      COMPAT_BPF_NOEXIST);

	if (ret && ret != -IOCOST_EEXIST)
		iocost_fail(IOCOST_FAILURE_RETIRE, ret);
}

static __always_inline struct iocost_owner_state *iocost_ensure_owner(
	struct ioc_gq *raw_iocg, struct bio *bio)
{
	struct iocost_owner_state initial = {};
	struct ioc_gq___iocost *iocg = (void *)raw_iocg;
	struct iocost_owner_state *owner;
	struct blkcg_gq *blkg = NULL;
	struct blkcg *blkcg = NULL;
	struct gendisk *disk;
	u64 iocg_ptr = (u64)raw_iocg;
	u64 css_offset;
	int ret;

	owner = bpf_map_lookup_elem(&iocost_owner_state_map, &iocg_ptr);
	if (owner)
		return owner;

	/* IOCG and bio references protect this first read until pd_free. */
	disk = bio_disk(bio);
	if (!raw_iocg || !bio ||
	    !IOCOST_CORE_READ(&blkg, iocg, pd.blkg) || !blkg ||
	    !IOCOST_CORE_READ(&blkcg, blkg, blkcg) || !blkcg ||
	    !IOCOST_CORE_READ(&initial.css_serial, blkcg, css.serial_nr) ||
	    !initial.css_serial ||
	    !disk || !IOCOST_CORE_READ(&initial.major, disk, major) ||
	    !IOCOST_CORE_READ(&initial.minor, disk, first_minor)) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return NULL;
	}
	css_offset = compat_bpf_core_field_offset(((struct blkcg *)0)->css);
	initial.css = (u64)blkcg + css_offset;
	ret = bpf_map_update_elem(&iocost_owner_state_map, &iocg_ptr,
				  &initial, COMPAT_BPF_NOEXIST);
	if (ret && ret != -IOCOST_EEXIST) {
		iocost_fail(IOCOST_FAILURE_OWNER_INSERT, ret);
		return NULL;
	}
	owner = bpf_map_lookup_elem(&iocost_owner_state_map, &iocg_ptr);
	if (!owner) {
		iocost_fail(IOCOST_FAILURE_OWNER_INSERT, -IOCOST_ENOENT);
		return NULL;
	}
	return owner;
}

static __always_inline bool iocost_ensure_aggregate(
	const struct io_control_wait_key *key)
{
	u64 zero = 0;
	u64 *counter;
	int ret;

	counter = bpf_map_lookup_elem(&iocost_wait_agg_map, key);
	if (counter)
		return true;
	ret = bpf_map_update_elem(&iocost_wait_agg_map, key, &zero,
				  COMPAT_BPF_NOEXIST);
	if (ret && ret != -IOCOST_EEXIST) {
		iocost_fail(IOCOST_FAILURE_AGGREGATE_INSERT, ret);
		return false;
	}
	return true;
}

static __always_inline struct bio *iocost_tail_waiter(struct ioc_gq *raw_iocg)
{
	struct ioc_gq___iocost *iocg = (void *)raw_iocg;
	struct wait_queue_head *waitq;
	struct iocg_wait___iocost *wait;
	struct list_head *raw_tail = NULL;
	struct bio *bio = NULL;
	u64 waitq_offset;
	u64 entry_offset;
	u64 waiter_offset;
	u64 waiter_address;

	if (!raw_iocg)
		return NULL;
	waitq_offset = compat_bpf_core_field_offset(
		((struct ioc_gq___iocost *)0)->waitq);
	waitq = (void *)((u64)iocg + waitq_offset);
	if (!IOCOST_CORE_READ(&raw_tail, waitq, head.prev) || !raw_tail)
		return NULL;

	/* The enqueue caller holds waitq.lock and just appended this waiter. */
	entry_offset = compat_bpf_core_field_offset(
		((struct wait_queue_entry *)0)->entry);
	waiter_offset = compat_bpf_core_field_offset(
		((struct iocg_wait___iocost *)0)->wait) + entry_offset;
	if ((u64)raw_tail < waiter_offset)
		return NULL;
	waiter_address = (u64)raw_tail - waiter_offset;
	wait = (void *)waiter_address;
	if (!IOCOST_CORE_READ(&bio, wait, bio) || !bio)
		return NULL;

	return bio;
}

SEC("kprobe/iocg_kick_waitq")
int kprobe_iocg_kick_waitq(struct pt_regs *ctx)
{
	struct iocost_pending pending = {};
	struct ioc_gq *iocg;
	struct bio *bio = NULL;
	u64 caller = 0;
	int ret;
	u32 operation;
	int bio_result;

	IO_CONTROL_KPROBE_READ_RET_IP(caller, ctx);
	if (!iocost_enqueue_caller(caller))
		return 0;
	if (iocost_unhealthy())
		return 0;
	pending.start_ns = bpf_ktime_get_ns();

	iocg = (struct ioc_gq *)PT_REGS_PARM1_CORE(ctx);
	bio = iocost_tail_waiter(iocg);
	if (!bio) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	bio_result = iocost_read_bio(bio, &operation);
	if (bio_result == 0)
		return 0;
	if (bio_result < 0) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	pending.operation = operation;
	/* A new admission owns this bio, including any missed-wake residue. */
#ifdef IOCOST_TEST
	/* Inject a helper result; the production consumer below owns the error. */
	if (!iocost_diag_inject_start_fault(iocg, bio, &pending, &ret))
#endif
	ret = bpf_map_update_elem(&iocost_pending_map, &bio, &pending,
				  COMPAT_BPF_ANY);
	iocost_classify_pending_update_ret(ret);
	return 0;
}

static __always_inline struct iocost_wake_frame *iocost_wake_frame(void)
{
	u32 zero = 0;

	return bpf_map_lookup_elem(&iocost_wake_frame_map, &zero);
}

static __always_inline void iocost_clear_wake_frame(
	struct iocost_wake_frame *frame)
{
	frame->bio_ptr = 0;
}

static __always_inline bool iocost_delete_pending(u64 bio_ptr)
{
	int ret = bpf_map_delete_elem(&iocost_pending_map, &bio_ptr);

	if (!ret)
		return true;
	iocost_fail(IOCOST_FAILURE_PENDING_DELETE, ret);
	return false;
}

SEC("kprobe/iocg_wake_fn")
int kprobe_iocg_wake_fn(struct pt_regs *ctx)
{
	struct iocost_wake_frame *frame;
	struct iocost_owner_state *owner;
	struct iocost_pending *pending;
	struct iocg_wake_ctx___iocost *wake_ctx;
	struct iocg_wait___iocost *wait;
	struct wait_queue_entry *wq_entry;
	struct ioc_gq *iocg = NULL;
	struct bio *bio = NULL;
	u64 wait_offset;
	u64 end_ns;

	frame = iocost_wake_frame();
	if (!frame) {
		iocost_fail(IOCOST_FAILURE_WAKE_FRAME, 0);
		return 0;
	}
	/* IRQ-off callbacks do not nest. A leftover frame missed its return. */
	iocost_clear_wake_frame(frame);
	if (iocost_unhealthy())
		return 0;
	end_ns = bpf_ktime_get_ns();

	wq_entry = (struct wait_queue_entry *)PT_REGS_PARM1_CORE(ctx);
	wake_ctx = (void *)PT_REGS_PARM4_CORE(ctx);
	if (!wq_entry || !wake_ctx ||
	    !IOCOST_CORE_READ(&iocg, wake_ctx, iocg) || !iocg) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	wait_offset = compat_bpf_core_field_offset(
		((struct iocg_wait___iocost *)0)->wait);
	if ((u64)wq_entry < wait_offset) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	wait = (void *)((u64)wq_entry - wait_offset);
	if (!IOCOST_CORE_READ(&bio, wait, bio) || !bio) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}

	pending = bpf_map_lookup_elem(&iocost_pending_map, &bio);
	if (!pending)
		return 0;
	if (!pending->start_ns || pending->reserved) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	frame->end_ns = end_ns;
	frame->pending = *pending;
	/* Prepare identity before the callback can release the issuer's bio. */
	owner = iocost_ensure_owner(iocg, bio);
	if (!owner)
		return 0;
	frame->key = (struct io_control_wait_key){
		.css = owner->css,
		.css_serial = owner->css_serial,
		.major = owner->major,
		.minor = owner->minor,
		.operation = frame->pending.operation,
	};
	if (!iocost_ensure_aggregate(&frame->key))
		return 0;
#ifdef IOCOST_TEST
	iocost_diag_mark_wake_entry_hit((u64)iocg);
	iocost_diag_inject_delete_failure((u64)bio, (u64)iocg);
#endif
	/*
	 * Own the sample before the callback wakes its issuer. That task may
	 * finish_wait() and reuse the bio on another CPU before our return probe.
	 * Successful returns therefore never access pending by this bio address.
	 */
	if (!iocost_delete_pending((u64)bio)) {
		iocost_clear_wake_frame(frame);
		return 0;
	}
	asm volatile("" ::: "memory");
	frame->bio_ptr = (u64)bio;
	return 0;
}

SEC("kretprobe/iocg_wake_fn")
int kretprobe_iocg_wake_fn(struct pt_regs *ctx)
{
	struct iocost_wake_frame *frame;
	struct iocost_pending pending;
	u64 *counter;
	s32 ret;
	u64 wait_ns;
	u64 packed;

	frame = iocost_wake_frame();
	if (!frame) {
		iocost_fail(IOCOST_FAILURE_WAKE_FRAME, 0);
		return 0;
	}
	if (!frame->bio_ptr)
		return 0;

	ret = (s32)(u32)PT_REGS_RC(ctx);
	if (ret == -1) {
		int update_ret;

#ifdef IOCOST_TEST
		iocost_diag_mark_wake_return_minus_one(frame->key.css_serial,
						       frame->key.major,
						       frame->key.minor);
#endif
		/* No wake or unlink occurred; the same bio is still waiting. */
		update_ret = bpf_map_update_elem(&iocost_pending_map,
						 &frame->bio_ptr, &frame->pending,
						 COMPAT_BPF_NOEXIST);
		iocost_classify_pending_update_ret(update_ret);
		iocost_clear_wake_frame(frame);
		return 0;
	}
	if (ret != 0) {
		iocost_fail(IOCOST_FAILURE_WAKE_FRAME, 0);
		iocost_clear_wake_frame(frame);
		return 0;
	}
#ifdef IOCOST_TEST
	iocost_diag_mark_wake_return_zero(frame->key.css_serial,
					  frame->key.major, frame->key.minor);
#endif

	pending = frame->pending;
	counter = bpf_map_lookup_elem(&iocost_wait_agg_map, &frame->key);
	if (!counter) {
		iocost_clear_wake_frame(frame);
		return 0;
	}
	packed = *counter;
	if (frame->end_ns < pending.start_ns) {
		iocost_fail(IOCOST_FAILURE_TIME_ROLLBACK, 0);
		iocost_clear_wake_frame(frame);
		return 0;
	}
	wait_ns = frame->end_ns - pending.start_ns;
	packed = io_control_wait_add(packed, wait_ns);
	/*
	 * The IRQ-off waitq lock serializes this CPU's writer. Publish count and
	 * time together with one aligned store.
	 */
	*counter = packed;
#ifdef IOCOST_TEST
	iocost_diag_mark_settled(frame->key.css_serial,
				 frame->key.major, frame->key.minor);
#endif
	iocost_clear_wake_frame(frame);
	return 0;
}

SEC("kprobe/ioc_pd_free")
int kprobe_ioc_pd_free(struct pt_regs *ctx)
{
	struct iocost_owner_state *stored_owner;
	struct iocost_owner_state owner;
	struct blkg_policy_data *pd;
	u64 pd_offset;
	u64 iocg_ptr;
	int ret;

	pd = (struct blkg_policy_data *)PT_REGS_PARM1_CORE(ctx);
	if (!pd)
		return 0;
	pd_offset = compat_bpf_core_field_offset(
		((struct ioc_gq___iocost *)0)->pd);
	if ((u64)pd < pd_offset) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	iocg_ptr = (u64)pd - pd_offset;
	stored_owner = bpf_map_lookup_elem(&iocost_owner_state_map, &iocg_ptr);
	if (!stored_owner)
		return 0;
	owner = *stored_owner;

	/* The last bio's RCU release follows the IRQ-off wake return and settlement. */
	iocost_retire_aggregate(&owner);
	ret = bpf_map_delete_elem(&iocost_owner_state_map, &iocg_ptr);
	if (ret)
		iocost_fail(IOCOST_FAILURE_OWNER_DELETE, ret);
	return 0;
}
