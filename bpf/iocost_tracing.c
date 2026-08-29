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

#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define IOCOST_LIVE_ENTRIES 4096
#define IOCOST_WAIT_AGG_ENTRIES (IOCOST_LIVE_ENTRIES * 2)
#define IOCOST_PENDING_ENTRIES 10240
#define IOCOST_ENOENT 2
#define IOCOST_EEXIST 17
#define IOCOST_U32_MAX 0xffffffffU
/*
 * Each CPU/key publishes one word: high 26-bit count, low 38-bit wait in 10us.
 * These are independent modulo fields; a wait carry must not become an IO.
 * The moduli are 67,108,864 IOs (about 1.118M/s over 60s, 559K/s over 120s)
 * and 2,748,779.06944s of total wait. Give wait 38 bits because completion
 * accounts the full episode, including time spent in prior scrape periods.
 * The scrape interval alone cannot bound that total; exact deltas require
 * both interval increments to be smaller than their respective moduli.
 */
#define IOCOST_WAIT_COUNT_BITS 26
#define IOCOST_WAIT_10US_BITS 38
#define IOCOST_WAIT_UNIT_NS 10000ULL
#define IOCOST_WAIT_COUNT_MASK ((1ULL << IOCOST_WAIT_COUNT_BITS) - 1)
#define IOCOST_WAIT_10US_MASK ((1ULL << IOCOST_WAIT_10US_BITS) - 1)
#ifndef REQ_OP_MASK
#define REQ_OP_MASK ((1U << 8) - 1)
#endif

/* Go resolves these values from live kallsyms before load. */
volatile const u64 iocost_wake_fn_addr = 0;
volatile const u64 iocost_throttle_caller_start = 0;
volatile const u64 iocost_throttle_caller_end = 0;
volatile const u64 iocost_over_budget_caller_start = 0;
volatile const u64 iocost_over_budget_caller_end = 0;

/* Supplement the baseline header with IOCOST types and newer device fields. */
struct iocg_wait___iocost {
	struct wait_queue_entry wait;
	struct bio *bio;
	bool committed;
} __attribute__((preserve_access_index));

struct iocg_wake_ctx___iocost {
	struct ioc_gq *iocg;
} __attribute__((preserve_access_index));

struct ioc_gq___iocost {
	struct blkg_policy_data pd;
	struct ioc *ioc;
	struct wait_queue_head waitq;
} __attribute__((preserve_access_index));

struct ioc___iocost {
	struct rq_qos rqos;
} __attribute__((preserve_access_index));

struct rq_qos___iocost_mainline {
	struct gendisk *disk;
} __attribute__((preserve_access_index));

struct bio___iocost_mainline {
	struct block_device *bi_bdev;
} __attribute__((preserve_access_index));

struct iocost_ioc_state {
	u64 ioc_id;
	u64 device;
};

struct iocost_owner_state {
	u64 ioc_ptr;
	u64 ioc_id;
	u64 css;
	u64 css_serial;
};

struct iocost_pending {
	u64 start_ns;
	u32 operation;
	u32 reserved;
};

struct iocost_wait_key {
	u64 ioc_id;
	u64 css_serial;
	u32 operation;
	u32 reserved;
};

struct iocost_wake_frame {
	u64 bio_ptr; /* Published last; zero means the frame is idle. */
	u64 iocg_ptr;
	u64 end_ns;
	struct iocost_pending pending;
};

enum iocost_failure {
	IOCOST_FAILURE_IDENTITY = 1,
	IOCOST_FAILURE_PENDING_COLLISION,
	IOCOST_FAILURE_PENDING_INSERT,
	IOCOST_FAILURE_PENDING_DELETE,
	IOCOST_FAILURE_AGGREGATE_INSERT,
	IOCOST_FAILURE_WAKE_FRAME,
	IOCOST_FAILURE_TIME_ROLLBACK,
	IOCOST_FAILURE_IOC_INSERT,
	IOCOST_FAILURE_OWNER_INSERT,
	IOCOST_FAILURE_AGGREGATE_DELETE,
	IOCOST_FAILURE_OWNER_DELETE,
	IOCOST_FAILURE_IOC_DELETE,
};

struct iocost_status {
	u64 failure; /* High 32-bit reason, low signed 32-bit helper errno. */
};

#define IOCOST_ASSERT_OFFSET(type, field, expected)                           \
	_Static_assert(__builtin_offsetof(struct type, field) == (expected),    \
		       #type "." #field " ABI")

_Static_assert(sizeof(struct iocost_ioc_state) == 16,
	       "iocost_ioc_state ABI");
IOCOST_ASSERT_OFFSET(iocost_ioc_state, ioc_id, 0);
IOCOST_ASSERT_OFFSET(iocost_ioc_state, device, 8);

_Static_assert(sizeof(struct iocost_owner_state) == 32,
	       "iocost_owner_state ABI");
IOCOST_ASSERT_OFFSET(iocost_owner_state, ioc_ptr, 0);
IOCOST_ASSERT_OFFSET(iocost_owner_state, ioc_id, 8);
IOCOST_ASSERT_OFFSET(iocost_owner_state, css, 16);
IOCOST_ASSERT_OFFSET(iocost_owner_state, css_serial, 24);

_Static_assert(sizeof(struct iocost_pending) == 16, "iocost_pending ABI");
IOCOST_ASSERT_OFFSET(iocost_pending, start_ns, 0);
IOCOST_ASSERT_OFFSET(iocost_pending, operation, 8);
IOCOST_ASSERT_OFFSET(iocost_pending, reserved, 12);

_Static_assert(sizeof(struct iocost_wait_key) == 24,
	       "iocost_wait_key ABI");
IOCOST_ASSERT_OFFSET(iocost_wait_key, ioc_id, 0);
IOCOST_ASSERT_OFFSET(iocost_wait_key, css_serial, 8);
IOCOST_ASSERT_OFFSET(iocost_wait_key, operation, 16);
IOCOST_ASSERT_OFFSET(iocost_wait_key, reserved, 20);

_Static_assert(IOCOST_WAIT_COUNT_BITS + IOCOST_WAIT_10US_BITS == 64,
	       "iocost packed counter ABI");

_Static_assert(sizeof(struct iocost_wake_frame) == 40,
	       "iocost_wake_frame ABI");
IOCOST_ASSERT_OFFSET(iocost_wake_frame, bio_ptr, 0);
IOCOST_ASSERT_OFFSET(iocost_wake_frame, iocg_ptr, 8);
IOCOST_ASSERT_OFFSET(iocost_wake_frame, end_ns, 16);
IOCOST_ASSERT_OFFSET(iocost_wake_frame, pending, 24);

_Static_assert(sizeof(struct iocost_status) == 8, "iocost_status ABI");
IOCOST_ASSERT_OFFSET(iocost_status, failure, 0);

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, u32);
	__type(value, u64);
	__uint(max_entries, 1);
} iocost_id_seq_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct iocost_ioc_state);
	__uint(max_entries, IOCOST_LIVE_ENTRIES);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} iocost_ioc_state_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct iocost_owner_state);
	__uint(max_entries, IOCOST_LIVE_ENTRIES);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} iocost_owner_state_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct iocost_pending);
	__uint(max_entries, IOCOST_PENDING_ENTRIES);
} iocost_pending_map SEC(".maps");

/*
 * Policy deactivation can enter pd_free before a timer's wake return ends.
 * Avoid the preallocated freelist's cross-CPU reuse of that writer's value:
 * non-preallocated values retire through RCU on older kernels, or through
 * the deleting CPU's local cache. That CPU stays IRQ-off until timer drain,
 * so neither a new admission nor allocator irq_work can reuse the value.
 * This relies on IOCOST teardown and the non-RT support gate, not a general
 * NO_PREALLOC guarantee for arbitrary map writers.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__type(key, struct iocost_wait_key);
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

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, struct iocost_status);
	__uint(max_entries, 1);
} iocost_stat_map SEC(".maps");

#define IOCOST_CORE_READ(dst, src, field) \
	(BPF_CORE_READ_INTO((dst), (src), field) == 0)

struct iocost_live_identity {
	u64 ioc_ptr;
	u64 iocg_ptr;
	u64 css;
	u64 css_serial;
	struct blkcg_gq *blkg;
};

static __always_inline struct iocost_status *iocost_status_value(void)
{
	u32 zero = 0;

	return bpf_map_lookup_elem(&iocost_stat_map, &zero);
}

static __always_inline bool iocost_unhealthy(void)
{
	struct iocost_status *status = iocost_status_value();

	return !status || status->failure != 0;
}

/*
 * Direct map ops can return a zero-extended int even with long helper
 * declarations. Publish one concrete reason and errno together. Concurrent
 * failures may replace the diagnostic, but never restore a healthy zero.
 */
static __always_inline void iocost_fail(u32 reason, int error)
{
	struct iocost_status *status = iocost_status_value();

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

static __always_inline bool iocost_read_live_identity(
	struct ioc_gq *raw_iocg, struct iocost_live_identity *identity)
{
	struct ioc_gq___iocost *iocg = (void *)raw_iocg;
	void *css;
	struct blkcg_gq *blkg = NULL;
	struct blkcg *blkcg = NULL;
	struct ioc *ioc = NULL;
	u64 css_offset;
	u64 serial = 0;

	if (!raw_iocg)
		return false;
	if (!IOCOST_CORE_READ(&ioc, iocg, ioc) || !ioc)
		return false;
	if (!IOCOST_CORE_READ(&blkg, iocg, pd.blkg) || !blkg)
		return false;
	if (!IOCOST_CORE_READ(&blkcg, blkg, blkcg) || !blkcg)
		return false;

	css_offset = compat_bpf_core_field_offset(
		((struct blkcg *)0)->css);
	css = (void *)((u64)blkcg + css_offset);
	if (!css ||
	    !IOCOST_CORE_READ(&serial, blkcg, css.serial_nr) || !serial)
		return false;

	identity->ioc_ptr = (u64)ioc;
	identity->iocg_ptr = (u64)raw_iocg;
	identity->css = (u64)css;
	identity->css_serial = serial;
	identity->blkg = blkg;
	return true;
}

/* Returns 1 for READ/WRITE, 0 for an ignored operation and -1 on read error. */
static __always_inline int iocost_read_bio(
	struct bio *raw_bio, struct blkcg_gq **blkg, u32 *operation)
{
	struct blkcg_gq *raw_blkg = NULL;
	u32 opf = 0;
	u32 op;

	if (!raw_bio || !IOCOST_CORE_READ(&opf, raw_bio, bi_opf))
		return -1;
	op = opf & REQ_OP_MASK;
	if (op != REQ_OP_READ && op != REQ_OP_WRITE)
		return 0;
	if (!IOCOST_CORE_READ(&raw_blkg, raw_bio, bi_blkg) || !raw_blkg)
		return -1;
	*blkg = raw_blkg;
	*operation = op;
	return 1;
}

static __always_inline bool iocost_read_device(
	struct ioc *raw_ioc, struct bio *raw_bio,
	u32 *major, u32 *first_minor)
{
	struct ioc___iocost *ioc = (void *)raw_ioc;
	struct rq_qos *rqos_510;
	struct rq_qos___iocost_mainline *rqos_mainline;
	struct bio___iocost_mainline *bio_mainline = (void *)raw_bio;
	struct gendisk *bio_disk = NULL;
	struct gendisk *ioc_disk = NULL;
	struct block_device *raw_bdev = NULL;
	struct request_queue *disk_queue = NULL;
	struct request_queue *ioc_queue = NULL;
	u64 rqos_offset;
	s32 disk_major = 0;
	s32 disk_first_minor = 0;

	if (!raw_ioc || !raw_bio)
		return false;
	rqos_offset = compat_bpf_core_field_offset(
		((struct ioc___iocost *)0)->rqos);

	/* Bio and rq_qos layouts are selected independently for backports. */
	if (bpf_core_field_exists(bio_mainline->bi_bdev)) {
		if (!IOCOST_CORE_READ(&raw_bdev, bio_mainline, bi_bdev) ||
		    !raw_bdev)
			return false;
		if (!IOCOST_CORE_READ(&bio_disk, raw_bdev, bd_disk))
			return false;
	} else if (!IOCOST_CORE_READ(&bio_disk, raw_bio, bi_disk)) {
		return false;
	}
	if (!bio_disk)
		return false;

	if (bpf_core_field_exists(
		    ((struct rq_qos___iocost_mainline *)0)->disk)) {
		rqos_mainline = (void *)((u64)ioc + rqos_offset);
		if (!IOCOST_CORE_READ(&ioc_disk, rqos_mainline, disk) ||
		    bio_disk != ioc_disk)
			return false;
	} else {
		rqos_510 = (void *)((u64)ioc + rqos_offset);
		if (!IOCOST_CORE_READ(&ioc_queue, rqos_510, q) || !ioc_queue)
			return false;
		if (!IOCOST_CORE_READ(&disk_queue, bio_disk, queue) ||
		    disk_queue != ioc_queue)
			return false;
		ioc_disk = bio_disk;
	}

	if (!IOCOST_CORE_READ(&disk_major, ioc_disk, major) || disk_major < 0 ||
	    !IOCOST_CORE_READ(&disk_first_minor, ioc_disk, first_minor) ||
	    disk_first_minor < 0)
		return false;
	*major = (u32)disk_major;
	*first_minor = (u32)disk_first_minor;
	return true;
}

static __always_inline u64 iocost_allocate_ioc_id(void)
{
	u32 zero = 0;
	u32 cpu;
	u64 *stored;
	u64 sequence;
	u64 id;

	stored = bpf_map_lookup_elem(&iocost_id_seq_map, &zero);
	if (!stored) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	sequence = *stored;
	if (sequence >= IOCOST_U32_MAX) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	sequence++;
	cpu = bpf_get_smp_processor_id();
	if (cpu == IOCOST_U32_MAX) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	id = ((u64)(cpu + 1) << 32) | sequence;
	*stored = sequence;
	return id;
}

static __always_inline void iocost_delete_lifecycle_aggregate(
	const struct iocost_wait_key *key)
{
	int ret = bpf_map_delete_elem(&iocost_wait_agg_map, key);

	if (ret && ret != -IOCOST_ENOENT)
		iocost_fail(IOCOST_FAILURE_AGGREGATE_DELETE, ret);
}

static __always_inline bool iocost_ioc_state_valid(
	const struct iocost_ioc_state *state)
{
	return state && state->ioc_id;
}

static __always_inline u64 iocost_ensure_ioc(
	u64 ioc_ptr, u32 major, u32 first_minor)
{
	struct iocost_ioc_state initial = {
		.device = ((u64)major << 32) | first_minor,
	};
	struct iocost_ioc_state *state;
	int ret;

	if (!ioc_ptr) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	state = bpf_map_lookup_elem(&iocost_ioc_state_map, &ioc_ptr);
	if (!state) {
		initial.ioc_id = iocost_allocate_ioc_id();
		if (!initial.ioc_id)
			return 0;
		ret = bpf_map_update_elem(&iocost_ioc_state_map, &ioc_ptr,
					  &initial, COMPAT_BPF_NOEXIST);
		if (ret && ret != -IOCOST_EEXIST) {
			iocost_fail(IOCOST_FAILURE_IOC_INSERT, ret);
			return 0;
		}
		/* A concurrent IOCG may have published the winning identity. */
		state = bpf_map_lookup_elem(&iocost_ioc_state_map, &ioc_ptr);
	}
	if (!iocost_ioc_state_valid(state)) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	initial.ioc_id = state->ioc_id;
	if (state->device != initial.device) {
		/*
		 * The IOC owns the counters; device is best-effort presentation.
		 * Ordinary HASH copies can overlap this store. Go rebaselines
		 * observed label changes without restarting the session.
		 */
		state->device = initial.device;
	}
	return initial.ioc_id;
}

static __always_inline struct iocost_owner_state *iocost_ensure_owner(
	const struct iocost_live_identity *identity, u64 ioc_id)
{
	struct iocost_owner_state initial = {
		.ioc_ptr = identity->ioc_ptr,
		.ioc_id = ioc_id,
		.css = identity->css,
		.css_serial = identity->css_serial,
	};
	struct iocost_owner_state *owner;
	int ret;

	owner = bpf_map_lookup_elem(&iocost_owner_state_map,
				    &identity->iocg_ptr);
	if (!owner) {
		ret = bpf_map_update_elem(&iocost_owner_state_map,
					  &identity->iocg_ptr, &initial,
					  COMPAT_BPF_NOEXIST);
		if (ret && ret != -IOCOST_EEXIST) {
			iocost_fail(IOCOST_FAILURE_OWNER_INSERT, ret);
			return NULL;
		}
		owner = bpf_map_lookup_elem(&iocost_owner_state_map,
					    &identity->iocg_ptr);
	}
	if (!owner || owner->ioc_ptr != initial.ioc_ptr ||
	    owner->ioc_id != initial.ioc_id || owner->css != initial.css ||
	    owner->css_serial != initial.css_serial) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return NULL;
	}
	return owner;
}

static __always_inline bool iocost_ensure_aggregate(
	const struct iocost_wait_key *key)
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
	counter = bpf_map_lookup_elem(&iocost_wait_agg_map, key);
	if (!counter) {
		iocost_fail(IOCOST_FAILURE_AGGREGATE_INSERT, -IOCOST_ENOENT);
		return false;
	}
	return true;
}

static __always_inline struct bio *iocost_tail_waiter(struct ioc_gq *raw_iocg)
{
	struct ioc_gq___iocost *iocg = (void *)raw_iocg;
	struct wait_queue_head *waitq;
	struct wait_queue_entry *entry;
	struct iocg_wait___iocost *wait;
	struct list_head *raw_tail = NULL;
	struct bio *bio = NULL;
	wait_queue_func_t callback = NULL;
	void *private = NULL;
	u64 waitq_offset;
	u64 head_offset;
	u64 entry_offset;
	u64 waiter_offset;
	u64 head_address;
	u64 waiter_address;
	bool committed = true;

	if (!raw_iocg)
		return NULL;
	waitq_offset = compat_bpf_core_field_offset(
		((struct ioc_gq___iocost *)0)->waitq);
	head_offset = compat_bpf_core_field_offset(
		((struct wait_queue_head *)0)->head);
	head_address = (u64)raw_iocg + waitq_offset + head_offset;
	waitq = (void *)((u64)iocg + waitq_offset);
	if (!IOCOST_CORE_READ(&raw_tail, waitq, head.prev) || !raw_tail ||
	    (u64)raw_tail == head_address)
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
	entry = (void *)(waiter_address +
		compat_bpf_core_field_offset(
			((struct iocg_wait___iocost *)0)->wait));
	if (!IOCOST_CORE_READ(&callback, entry, func) ||
	    (u64)callback != iocost_wake_fn_addr ||
	    !IOCOST_CORE_READ(&private, entry, private) ||
	    private != (void *)bpf_get_current_task() ||
	    !IOCOST_CORE_READ(&committed, wait, committed) || committed ||
	    !IOCOST_CORE_READ(&bio, wait, bio) || !bio)
		return NULL;

	return bio;
}

SEC("kprobe/iocg_kick_waitq")
int kprobe_iocg_kick_waitq(struct pt_regs *ctx)
{
	struct iocost_live_identity identity = {};
	struct iocost_wait_key wait_key = {};
	struct iocost_pending pending = {};
	struct blkcg_gq *bio_blkg = NULL;
	struct ioc_gq *iocg;
	struct bio *bio = NULL;
	u64 caller = 0;
	u64 ioc_id;
	int ret;
	u32 major;
	u32 first_minor;
	u32 operation;
	int bio_result;

	BPF_KPROBE_READ_RET_IP(caller, ctx);
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
	bio_result = iocost_read_bio(bio, &bio_blkg, &operation);
	if (bio_result == 0)
		return 0;
	if (bio_result < 0 || !iocost_read_live_identity(iocg, &identity) ||
	    bio_blkg != identity.blkg ||
	    !iocost_read_device((struct ioc *)identity.ioc_ptr, bio,
				&major, &first_minor)) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}

	ioc_id = iocost_ensure_ioc(identity.ioc_ptr, major, first_minor);
	if (!ioc_id)
		return 0;
	if (!iocost_ensure_owner(&identity, ioc_id))
		return 0;

	wait_key.ioc_id = ioc_id;
	wait_key.css_serial = identity.css_serial;
	wait_key.operation = operation;
	if (!iocost_ensure_aggregate(&wait_key))
		return 0;

	pending.operation = operation;
	/* A new admission owns this bio, including any missed-wake residue. */
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

static __always_inline bool iocost_owner_matches(
	const struct iocost_owner_state *owner,
	const struct iocost_live_identity *identity)
{
	return owner && owner->ioc_ptr == identity->ioc_ptr &&
	       owner->ioc_id != 0 && owner->css == identity->css &&
	       owner->css_serial == identity->css_serial;
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
	struct iocost_live_identity identity = {};
	struct iocost_wake_frame *frame;
	struct iocost_ioc_state *ioc_state;
	struct iocost_owner_state *owner;
	struct iocost_pending *pending;
	struct iocg_wake_ctx___iocost *wake_ctx;
	struct iocg_wait___iocost *wait;
	struct wait_queue_entry *wq_entry;
	struct blkcg_gq *bio_blkg = NULL;
	struct ioc_gq *iocg = NULL;
	struct bio *bio = NULL;
	u64 wait_offset;
	u64 end_ns;
	u32 operation;
	int bio_result;

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
	bio_result = iocost_read_bio(bio, &bio_blkg, &operation);
	if (bio_result != 1 || operation != pending->operation ||
	    !iocost_read_live_identity(iocg, &identity) ||
	    bio_blkg != identity.blkg) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	owner = bpf_map_lookup_elem(&iocost_owner_state_map,
				    &identity.iocg_ptr);
	if (!iocost_owner_matches(owner, &identity)) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	ioc_state = bpf_map_lookup_elem(&iocost_ioc_state_map,
					&owner->ioc_ptr);
	if (!ioc_state) {
		iocost_delete_pending((u64)bio);
		return 0;
	}
	if (!iocost_ioc_state_valid(ioc_state) ||
	    ioc_state->ioc_id != owner->ioc_id) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}

	frame->iocg_ptr = identity.iocg_ptr;
	frame->end_ns = end_ns;
	frame->pending = *pending;
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
	struct iocost_owner_state *stored_owner;
	struct iocost_owner_state owner;
	struct iocost_ioc_state *ioc_state;
	struct iocost_pending pending;
	u64 *counter;
	struct iocost_wait_key wait_key = {};
	s32 ret;
	u64 wait_ns;
	u64 packed;
	u64 io_count;
	u64 wait_10us;

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

	pending = frame->pending;
	stored_owner = bpf_map_lookup_elem(&iocost_owner_state_map,
					   &frame->iocg_ptr);
	if (!stored_owner) {
		iocost_clear_wake_frame(frame);
		return 0;
	}
	owner = *stored_owner;
	if (!owner.ioc_ptr || !owner.ioc_id || !owner.css ||
	    !owner.css_serial) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		iocost_clear_wake_frame(frame);
		return 0;
	}
	ioc_state = bpf_map_lookup_elem(&iocost_ioc_state_map, &owner.ioc_ptr);
	if (!ioc_state) {
		iocost_clear_wake_frame(frame);
		return 0;
	}
	if (!iocost_ioc_state_valid(ioc_state) ||
	    ioc_state->ioc_id != owner.ioc_id) {
		/*
		 * IOC exit precedes timer drain. A deleted HASH value may already
		 * belong to another key; confirm the original IOC is still live
		 * before treating the retained value as a broken identity.
		 */
		if (!bpf_map_lookup_elem(&iocost_ioc_state_map, &owner.ioc_ptr)) {
			iocost_clear_wake_frame(frame);
			return 0;
		}
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		iocost_clear_wake_frame(frame);
		return 0;
	}
	wait_key.ioc_id = owner.ioc_id;
	wait_key.css_serial = owner.css_serial;
	wait_key.operation = pending.operation;
	counter = bpf_map_lookup_elem(&iocost_wait_agg_map, &wait_key);
	if (!counter) {
		/* IOC exit may race the successful live lookup above. */
		if (!bpf_map_lookup_elem(&iocost_ioc_state_map, &owner.ioc_ptr)) {
			iocost_clear_wake_frame(frame);
			return 0;
		}
		iocost_fail(IOCOST_FAILURE_AGGREGATE_INSERT, -IOCOST_ENOENT);
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
	io_count = ((packed >> IOCOST_WAIT_10US_BITS) + 1) &
		   IOCOST_WAIT_COUNT_MASK;
	wait_10us = ((packed & IOCOST_WAIT_10US_MASK) +
		     wait_ns / IOCOST_WAIT_UNIT_NS) & IOCOST_WAIT_10US_MASK;
	packed = (io_count << IOCOST_WAIT_10US_BITS) | wait_10us;
	/*
	 * The IRQ-off waitq lock serializes this CPU's writer. Publish count and
	 * time together with one aligned store.
	 */
	*counter = packed;
	iocost_clear_wake_frame(frame);
	return 0;
}

SEC("kprobe/ioc_pd_free")
int kprobe_ioc_pd_free(struct pt_regs *ctx)
{
	struct iocost_owner_state *stored_owner;
	struct iocost_owner_state owner;
	struct iocost_wait_key wait_key = {};
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

	ret = bpf_map_delete_elem(&iocost_owner_state_map, &iocg_ptr);
	if (ret)
		iocost_fail(IOCOST_FAILURE_OWNER_DELETE, ret);
	if (!owner.ioc_ptr || !owner.ioc_id || !owner.css ||
	    !owner.css_serial) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}

	wait_key.ioc_id = owner.ioc_id;
	wait_key.css_serial = owner.css_serial;
	wait_key.operation = REQ_OP_READ;
	iocost_delete_lifecycle_aggregate(&wait_key);
	wait_key.operation = REQ_OP_WRITE;
	iocost_delete_lifecycle_aggregate(&wait_key);
	return 0;
}

SEC("kprobe/ioc_rqos_exit")
int kprobe_ioc_rqos_exit(struct pt_regs *ctx)
{
	struct iocost_ioc_state *state;
	struct rq_qos *rqos;
	u64 rqos_offset;
	u64 ioc_ptr;
	int ret;

	rqos = (struct rq_qos *)PT_REGS_PARM1_CORE(ctx);
	if (!rqos)
		return 0;
	rqos_offset = compat_bpf_core_field_offset(
		((struct ioc___iocost *)0)->rqos);
	if ((u64)rqos < rqos_offset) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	ioc_ptr = (u64)rqos - rqos_offset;
	state = bpf_map_lookup_elem(&iocost_ioc_state_map, &ioc_ptr);
	if (!state)
		return 0;
	if (!iocost_ioc_state_valid(state)) {
		iocost_fail(IOCOST_FAILURE_IDENTITY, 0);
		return 0;
	}
	ret = bpf_map_delete_elem(&iocost_ioc_state_map, &ioc_ptr);
	if (ret)
		iocost_fail(IOCOST_FAILURE_IOC_DELETE, ret);
	return 0;
}
