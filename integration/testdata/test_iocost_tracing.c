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
 * Build the production IOCOST probes with fixture-scoped observations and
 * real map-helper fault injection. Keeping this as the compiler input lets
 * the test share all accounting code while owning its maps and controls.
 */

#include "vmlinux.h"

#include <bpf/bpf_helpers.h>

struct iocost_pending;
struct ioc_gq;

static __always_inline void iocost_diag_mark_wake_entry_hit(u64 iocg_ptr);
static __always_inline void iocost_diag_mark_wake_return_zero(
	u64 css_serial, u32 major, u32 first_minor);
static __always_inline void iocost_diag_mark_wake_return_minus_one(
	u64 css_serial, u32 major, u32 first_minor);
static __always_inline void iocost_diag_mark_settled(
	u64 css_serial, u32 major, u32 first_minor);
static __always_inline bool iocost_diag_inject_start_fault(
	struct ioc_gq *iocg, struct bio *bio,
	const struct iocost_pending *pending, int *update_ret);
static __always_inline void iocost_diag_inject_delete_failure(
	u64 bio_ptr, u64 iocg_ptr);

#define IOCOST_TEST 1
#include "../../bpf/iocost_tracing.c"

#define IOCOST_DIAG_FAULT_MAP_FULL (1U << 0)
#define IOCOST_DIAG_FAULT_COLLISION (1U << 1)
#define IOCOST_DIAG_FAULT_DELETE_FAILURE (1U << 2)

volatile const u64 iocost_diag_css_serial = 0;
volatile const u32 iocost_diag_major = 0;
volatile const u32 iocost_diag_first_minor = 0;
volatile const u32 iocost_diag_fault_mask = 0;

struct iocost_diag_stat {
	u64 start_guard_passes;
	u64 wake_entry_hits;
	u64 wake_return_zero;
	u64 wake_return_minus_one;
	u64 settled_count;
	u64 map_full_injections;
	u64 collision_injections;
	u64 delete_failure_injections;
};

_Static_assert(sizeof(struct iocost_diag_stat) == 64,
	       "iocost_diag_stat ABI");
IOCOST_ASSERT_OFFSET(iocost_diag_stat, start_guard_passes, 0);
IOCOST_ASSERT_OFFSET(iocost_diag_stat, wake_entry_hits, 8);
IOCOST_ASSERT_OFFSET(iocost_diag_stat, wake_return_zero, 16);
IOCOST_ASSERT_OFFSET(iocost_diag_stat, wake_return_minus_one, 24);
IOCOST_ASSERT_OFFSET(iocost_diag_stat, settled_count, 32);
IOCOST_ASSERT_OFFSET(iocost_diag_stat, map_full_injections, 40);
IOCOST_ASSERT_OFFSET(iocost_diag_stat, collision_injections, 48);
IOCOST_ASSERT_OFFSET(iocost_diag_stat, delete_failure_injections, 56);

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, struct iocost_diag_stat);
	__uint(max_entries, 1);
} iocost_diag_stat_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u32);
	__type(value, u64);
	__uint(max_entries, 1);
} iocost_diag_fault_scratch_map SEC(".maps");

/* Keep each observed wait's identity so qualification can distinguish an
 * unreclaimed pending row from a later bio which reused the same address.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, u64);
	__uint(max_entries, IOCOST_PENDING_ENTRIES);
} iocost_diag_pending_map SEC(".maps");

static __always_inline struct iocost_diag_stat *iocost_diag_stat_value(void)
{
	u32 zero = 0;

	return bpf_map_lookup_elem(&iocost_diag_stat_map, &zero);
}

static __always_inline bool iocost_diag_fault_mask_valid(u32 fault_mask)
{
	return fault_mask == 0 ||
	       fault_mask == IOCOST_DIAG_FAULT_MAP_FULL ||
	       fault_mask == IOCOST_DIAG_FAULT_COLLISION ||
	       fault_mask == IOCOST_DIAG_FAULT_DELETE_FAILURE;
}

static __always_inline bool iocost_diag_fixture_matches(u64 css_serial,
						       u32 major,
						       u32 first_minor)
{
	/* Faults require an exact owner. Observation-only qualification may
	 * include all cgroups on its private, explicitly selected disk.
	 */
	bool matches_css = css_serial != 0 &&
		(css_serial == iocost_diag_css_serial ||
		 (!iocost_diag_css_serial && !iocost_diag_fault_mask));

	return matches_css &&
	       iocost_diag_fault_mask_valid(iocost_diag_fault_mask) &&
	       major == iocost_diag_major &&
	       first_minor == iocost_diag_first_minor;
}

static __always_inline bool
iocost_diag_claim_fault(struct iocost_diag_stat *stat)
{
	u32 key = 0;
	u64 value = iocost_diag_css_serial;
	int ret;

	if (!stat)
		return false;
	ret = bpf_map_update_elem(&iocost_diag_fault_scratch_map, &key, &value,
				  COMPAT_BPF_NOEXIST);
	return ret == 0;
}

static __always_inline bool iocost_diag_inject_start_fault(
	struct ioc_gq *iocg, struct bio *bio,
	const struct iocost_pending *pending, int *update_ret)
{
	struct iocost_diag_stat *stat;
	struct blkcg *blkcg;
	struct gendisk *disk;
	u64 css_serial, bio_ptr = (u64)bio;
	u32 major, first_minor;
	u32 second_key = 1;
	u32 fault_mask = iocost_diag_fault_mask;
	u64 value;
	int ret;

	/* Observe admission without creating production owner or aggregate rows. */
	blkcg = BPF_CORE_READ((struct ioc_gq___iocost *)iocg, pd.blkg, blkcg);
	disk = bio_disk(bio);
	if (!blkcg || !disk ||
	    !IOCOST_CORE_READ(&css_serial, blkcg, css.serial_nr) ||
	    !IOCOST_CORE_READ(&major, disk, major) ||
	    !IOCOST_CORE_READ(&first_minor, disk, first_minor))
		return false;
	if (!iocost_diag_fixture_matches(css_serial, major,
					 first_minor))
		return false;
	stat = iocost_diag_stat_value();
	if (fault_mask == 0)
		bpf_map_update_elem(&iocost_diag_pending_map, &bio_ptr,
				    &pending->start_ns, COMPAT_BPF_ANY);
	if (stat)
		__sync_fetch_and_add(&stat->start_guard_passes, 1);
	if (fault_mask == 0 || fault_mask == IOCOST_DIAG_FAULT_DELETE_FAILURE)
		return false;
	if (!iocost_diag_claim_fault(stat))
		return false;
	value = css_serial;

	if (fault_mask == IOCOST_DIAG_FAULT_MAP_FULL) {
		ret = bpf_map_update_elem(&iocost_diag_fault_scratch_map,
					  &second_key, &value,
					  COMPAT_BPF_NOEXIST);
		if (ret) {
			__sync_fetch_and_add(&stat->map_full_injections, 1);
			*update_ret = ret;
		}
		return ret != 0;
	}
	ret = bpf_map_update_elem(&iocost_pending_map, &bio_ptr, pending,
				  COMPAT_BPF_NOEXIST);
	if (!ret) {
		__sync_fetch_and_add(&stat->collision_injections, 1);
		return false;
	}
	*update_ret = ret;
	return ret != -IOCOST_EEXIST;
}

static __always_inline void iocost_diag_inject_delete_failure(
	u64 bio_ptr, u64 iocg_ptr)
{
	struct iocost_diag_stat *stat;
	struct iocost_owner_state *owner;
	u32 fault_mask = iocost_diag_fault_mask;
	int ret;

	if (!iocost_diag_fault_mask_valid(fault_mask) ||
	    fault_mask != IOCOST_DIAG_FAULT_DELETE_FAILURE)
		return;
	owner = bpf_map_lookup_elem(&iocost_owner_state_map, &iocg_ptr);
	if (!owner || !iocost_diag_fixture_matches(owner->css_serial,
						owner->major, owner->minor))
		return;
	stat = iocost_diag_stat_value();
	if (!iocost_diag_claim_fault(stat))
		return;
	/* The production delete consumes ENOENT after this fixture-only removal. */
	ret = bpf_map_delete_elem(&iocost_pending_map, &bio_ptr);
	if (!ret)
		__sync_fetch_and_add(&stat->delete_failure_injections, 1);
}

static __always_inline void iocost_diag_mark_wake_entry_hit(u64 iocg_ptr)
{
	struct iocost_owner_state *owner;
	struct iocost_diag_stat *stat;

	owner = bpf_map_lookup_elem(&iocost_owner_state_map, &iocg_ptr);
	if (!owner || !iocost_diag_fixture_matches(owner->css_serial,
						owner->major, owner->minor))
		return;
	stat = iocost_diag_stat_value();
	if (stat)
		__sync_fetch_and_add(&stat->wake_entry_hits, 1);
}

#define DEFINE_IOCOST_DIAG_TARGET_COUNTER(name, field)                        \
	static __always_inline void name(u64 css_serial, u32 major,               \
					 u32 first_minor)                            \
	{                                                                          \
		struct iocost_diag_stat *stat;                                       \
		if (!iocost_diag_fixture_matches(css_serial, major, first_minor))    \
			return;                                                        \
		stat = iocost_diag_stat_value();                                     \
		if (stat)                                                            \
			__sync_fetch_and_add(&stat->field, 1);                         \
	}

DEFINE_IOCOST_DIAG_TARGET_COUNTER(iocost_diag_mark_wake_return_zero,
				  wake_return_zero)
DEFINE_IOCOST_DIAG_TARGET_COUNTER(iocost_diag_mark_wake_return_minus_one,
				  wake_return_minus_one)

#undef DEFINE_IOCOST_DIAG_TARGET_COUNTER

static __always_inline void iocost_diag_mark_settled(
	u64 css_serial, u32 major, u32 first_minor)
{
	struct iocost_diag_stat *stat;

	if (!iocost_diag_fixture_matches(css_serial, major,
					 first_minor))
		return;
	stat = iocost_diag_stat_value();
	if (stat)
		__sync_fetch_and_add(&stat->settled_count, 1);
}
