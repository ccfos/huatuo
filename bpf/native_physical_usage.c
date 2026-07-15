#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_profiler.h"

char __license[] SEC("license") = "GPL";

DEFINE_PROFILER_PAGE_TRACKING_MAP();
DEFINE_PROFILER_MAPS(struct profiler_event_base_t);

#define COMPAT_PG_HEAD_BIT 6

struct folio___compat {
	unsigned long _flags_1;
	unsigned int _folio_nr_pages;
	unsigned int _nr_pages;
} __attribute__((preserve_access_index));

static volatile const bool profiler_folio_npages = false;

static __always_inline s64 profiler_folio_nr_pages(struct folio___compat *folio)
{
	unsigned long folio_flags;
	unsigned long flags_1;
	u32 nr_pages;
	u32 order;

	/*
	 * Upstream v5.10-v6.2 uses page/page hooks and v6.3-v6.7 uses a
	 * folio/page pair, so the loader only enables this helper for v6.8+.
	 */
	/* All v6.8+ layouts keep flags at offset 0; its type changes in v6.18. */
	if (bpf_probe_read(&folio_flags, sizeof(folio_flags), folio))
		return 0;
	/* Every supported layout represents an order-0 folio as one page. */
	if (!(folio_flags & (1UL << COMPAT_PG_HEAD_BIT)))
		return 1;

	if (bpf_core_field_exists(folio->_nr_pages)) {
		/* v6.15-v7.2 with CONFIG_MEMCG or CONFIG_SLAB_OBJ_EXT. */
		nr_pages = BPF_CORE_READ(folio, _nr_pages);
	} else if (bpf_core_field_exists(folio->_folio_nr_pages)) {
		/* v6.8-v6.14 on 64-bit kernels. */
		nr_pages = BPF_CORE_READ(folio, _folio_nr_pages);
	} else {
		/*
		 * v6.8-v6.14 on 32-bit kernels, or v6.15-v7.2 without
		 * CONFIG_MEMCG and CONFIG_SLAB_OBJ_EXT.
		 */
		if (!bpf_core_field_exists(folio->_flags_1))
			return 0;

		flags_1 = BPF_CORE_READ(folio, _flags_1);
		order = flags_1 & 0xff;
		return 1ULL << order;
	}

	return nr_pages;
}

SEC("kprobe/page_add_new_anon_rmap")
int BPF_KPROBE(trace_page_alloc, void *page_or_folio)
{
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map __attribute__((unused));
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	u64 pid_tgid = bpf_get_current_pid_tgid();
	if (!profiler_should_trace(pid_tgid))
		return 0;

	if (!profiler_should_sample())
		return 0;

	SELECT_PROFILER_AB();

	struct profiler_event_base_t *event = profiler_prepare_event_base(
		&event_buf, pid_tgid, ctx, select_profiler_stack_map);
	if (!event)
		return 0;

	if (profiler_folio_npages) {
		event->value = profiler_folio_nr_pages(page_or_folio);
		if (event->value <= 0)
			return 0;
	} else {
		event->value = 1;
	}

	u64 page_addr = (u64)page_or_folio;
	/*
	 * The hash map stores a value copy, so later event_buf reuse does not
	 * overwrite the allocation stack saved for this page address.
	 */
	bpf_map_update_elem(&page_to_stackid, &page_addr, event, COMPAT_BPF_ANY);

	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));

	return 0;
}

SEC("kprobe/page_remove_rmap")
int BPF_KPROBE(trace_page_free, void *page_or_folio)
{
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map __attribute__((unused));
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	u64 pid_tgid = bpf_get_current_pid_tgid();
	if (!profiler_should_trace(pid_tgid))
		return 0;

	u64 page_addr = (u64)page_or_folio;
	struct profiler_event_base_t *stack_info =
		bpf_map_lookup_elem(&page_to_stackid, &page_addr);
	if (!stack_info)
		return 0;

	u32 idx = 0;
	struct profiler_event_base_t *event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	__builtin_memset(event, 0, sizeof(*event));

	profiler_copy_event_base(event, stack_info);
	if (profiler_folio_npages) {
		s64 remaining_pages = stack_info->value;
		s64 nr_pages = (s32)PT_REGS_PARM3(ctx);

		remaining_pages -= nr_pages;
		/*
		 * A large folio can be unmapped in multiple PTE batches. Keep
		 * its allocation stack until all base pages have been removed.
		 */
		if (remaining_pages == 0) {
			event->value = -nr_pages;
			bpf_map_delete_elem(&page_to_stackid, &page_addr);
		} else {
			event->value = remaining_pages;
			bpf_map_update_elem(&page_to_stackid, &page_addr, event, COMPAT_BPF_ANY);
			event->value = -nr_pages;
		}
	} else {
		event->value = -1;
		bpf_map_delete_elem(&page_to_stackid, &page_addr);
	}

	SELECT_PROFILER_AB();

	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));

	return 0;
}
