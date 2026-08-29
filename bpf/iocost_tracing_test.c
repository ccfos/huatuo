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

static __always_inline void iocost_diag_mark_start_guard_pass(
	u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor);
static __always_inline void iocost_diag_mark_wake_entry_hit(u64 iocg_ptr);
static __always_inline void iocost_diag_mark_wake_return_zero(u64 iocg_ptr);
static __always_inline void iocost_diag_mark_wake_return_minus_one(u64 iocg_ptr);
static __always_inline void iocost_diag_mark_settled(
	u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor);
static __always_inline bool iocost_diag_inject_start_fault(
	u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor, u64 bio_ptr,
	const struct iocost_pending *pending, int *update_ret);
static __always_inline void iocost_diag_inject_delete_failure(
	u64 bio_ptr, u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor);

#define IOCOST_TEST 1
#include "iocost_tracing.c"

#define IOCOST_DIAG_FAULT_MAP_FULL (1U << 0)
#define IOCOST_DIAG_FAULT_COLLISION (1U << 1)
#define IOCOST_DIAG_FAULT_DELETE_FAILURE (1U << 2)

volatile const u64 iocost_diag_iocg_ptr = 0;
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

static __always_inline struct iocost_diag_stat *iocost_diag_stat_value(void)
{
	u32 zero = 0;

	return bpf_map_lookup_elem(&iocost_diag_stat_map, &zero);
}

static __always_inline bool iocost_diag_target_iocg(u64 iocg_ptr)
{
	return iocost_diag_iocg_ptr != 0 &&
	       iocg_ptr == iocost_diag_iocg_ptr;
}

static __always_inline bool iocost_diag_fault_mask_valid(u32 fault_mask)
{
	return fault_mask == 0 ||
	       fault_mask == IOCOST_DIAG_FAULT_MAP_FULL ||
	       fault_mask == IOCOST_DIAG_FAULT_COLLISION ||
	       fault_mask == IOCOST_DIAG_FAULT_DELETE_FAILURE;
}

static __always_inline bool iocost_diag_fixture_matches(u64 iocg_ptr,
						       u64 css_serial,
						       u32 major,
						       u32 first_minor)
{
	return iocost_diag_iocg_ptr != 0 && iocost_diag_css_serial != 0 &&
	       iocost_diag_fault_mask_valid(iocost_diag_fault_mask) &&
	       iocg_ptr == iocost_diag_iocg_ptr &&
	       css_serial == iocost_diag_css_serial &&
	       major == iocost_diag_major &&
	       first_minor == iocost_diag_first_minor;
}

static __always_inline bool
iocost_diag_claim_fault(struct iocost_diag_stat *stat)
{
	u32 key = 0;
	u64 value = iocost_diag_iocg_ptr;
	int ret;

	if (!stat)
		return false;
	ret = bpf_map_update_elem(&iocost_diag_fault_scratch_map, &key, &value,
				  COMPAT_BPF_NOEXIST);
	return ret == 0;
}

static __always_inline bool iocost_diag_inject_start_fault(
	u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor, u64 bio_ptr,
	const struct iocost_pending *pending, int *update_ret)
{
	struct iocost_diag_stat *stat;
	u32 second_key = 1;
	u32 fault_mask = iocost_diag_fault_mask;
	u64 value = iocg_ptr;
	int ret;

	if (!iocost_diag_fault_mask_valid(fault_mask) || fault_mask == 0 ||
	    fault_mask == IOCOST_DIAG_FAULT_DELETE_FAILURE)
		return false;
	if (!iocost_diag_fixture_matches(iocg_ptr, css_serial, major,
					 first_minor))
		return false;
	stat = iocost_diag_stat_value();
	if (!iocost_diag_claim_fault(stat))
		return false;

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
	u64 bio_ptr, u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor)
{
	struct iocost_diag_stat *stat;
	u32 fault_mask = iocost_diag_fault_mask;
	int ret;

	if (!iocost_diag_fault_mask_valid(fault_mask) ||
	    fault_mask != IOCOST_DIAG_FAULT_DELETE_FAILURE)
		return;
	if (!iocost_diag_fixture_matches(iocg_ptr, css_serial, major,
					 first_minor))
		return;
	stat = iocost_diag_stat_value();
	if (!iocost_diag_claim_fault(stat))
		return;
	/* The production delete consumes ENOENT after this fixture-only removal. */
	ret = bpf_map_delete_elem(&iocost_pending_map, &bio_ptr);
	if (!ret)
		__sync_fetch_and_add(&stat->delete_failure_injections, 1);
}

static __always_inline bool iocost_diag_counter_filter_valid(u64 iocg_ptr)
{
	return iocost_diag_target_iocg(iocg_ptr) &&
	       iocost_diag_css_serial != 0 &&
	       iocost_diag_fault_mask_valid(iocost_diag_fault_mask);
}

#define DEFINE_IOCOST_DIAG_TARGET_COUNTER(name, field)                        \
	static __always_inline void name(u64 iocg_ptr)                            \
	{                                                                          \
		struct iocost_diag_stat *stat;                                       \
		if (!iocost_diag_counter_filter_valid(iocg_ptr))                      \
			return;                                                        \
		stat = iocost_diag_stat_value();                                     \
		if (stat)                                                            \
			__sync_fetch_and_add(&stat->field, 1);                         \
	}

DEFINE_IOCOST_DIAG_TARGET_COUNTER(iocost_diag_mark_wake_entry_hit,
				  wake_entry_hits)
DEFINE_IOCOST_DIAG_TARGET_COUNTER(iocost_diag_mark_wake_return_zero,
				  wake_return_zero)
DEFINE_IOCOST_DIAG_TARGET_COUNTER(iocost_diag_mark_wake_return_minus_one,
				  wake_return_minus_one)

#undef DEFINE_IOCOST_DIAG_TARGET_COUNTER

static __always_inline void iocost_diag_mark_start_guard_pass(
	u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor)
{
	struct iocost_diag_stat *stat;

	if (!iocost_diag_fixture_matches(iocg_ptr, css_serial, major,
					 first_minor))
		return;
	stat = iocost_diag_stat_value();
	if (stat)
		__sync_fetch_and_add(&stat->start_guard_passes, 1);
}

static __always_inline void iocost_diag_mark_settled(
	u64 iocg_ptr, u64 css_serial, u32 major, u32 first_minor)
{
	struct iocost_diag_stat *stat;

	if (!iocost_diag_fixture_matches(iocg_ptr, css_serial, major,
					 first_minor))
		return;
	stat = iocost_diag_stat_value();
	if (stat)
		__sync_fetch_and_add(&stat->settled_count, 1);
}
