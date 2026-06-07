#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"

// defaultly, we use task_group address as key to operate map.
#define TG_ADDR_KEY

#define TASK_RUNNING 0
#define TASK_ON_RQ_QUEUED 1

#define _(P)                                                                   \
	({                                                                     \
		typeof(P) val = 0;                                             \
		bpf_probe_read(&val, sizeof(val), &(P));                       \
		val;                                                           \
	})

char __license[] SEC("license") = "Dual MIT/GPL";

struct stat_t {
	unsigned long nvcsw;  // task_group counts of voluntary context switch
	unsigned long nivcsw; // task_group counts of involuntary context switch
	unsigned long
	    nlat_01; // task_group counts of sched latency range [0, 10)ms
	unsigned long
	    nlat_02; // task_group counts of sched latency range [10, 20)ms
	unsigned long
	    nlat_03; // task_group counts of sched latency range [20, 50)ms
	unsigned long
	    nlat_04; // task_group counts of sched latency range [50, inf)ms
};

struct g_stat_t {
	unsigned long g_nvcsw;	// global counts of voluntary context switch
	unsigned long g_nivcsw; // global counts of involuntary context switch
	unsigned long
	    g_nlat_01; // global counts of sched latency range [0, 10)ms
	unsigned long
	    g_nlat_02; // global counts of sched latency range [10, 20)ms
	unsigned long
	    g_nlat_03; // global counts of sched latency range [20, 50)ms
	unsigned long
	    g_nlat_04; // global counts of sched latency range [50, inf)ms
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u32);
	__type(value, u64);
	// FIXME: is 10000 enough or too large?
	__uint(max_entries, 10000);
} latency SEC(".maps");

struct stat_t;
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
#ifdef TG_ADDR_KEY
	__type(key, u64);
#else
	__type(key, u32);
#endif
	__type(value, struct stat_t);
	__uint(max_entries, 10000);
} cpu_tg_metric SEC(".maps");

struct g_stat_t;
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, struct g_stat_t);
	// all global counts are integrated in one g_stat_t struct
	__uint(max_entries, 1);
} cpu_host_metric SEC(".maps");

// record enqueue timestamp
static int trace_enqueue(u32 pid)
{
	// u64 *valp;
	u64 ts;

	if (pid == 0)
		return 0;

	ts = bpf_ktime_get_ns();
	bpf_map_update_elem(&latency, &pid, &ts, COMPAT_BPF_ANY);

	return 0;
}

SEC("tracepoint/sched/sched_wakeup_new")
int sched_wakeup_new_entry(struct trace_event_raw_sched_wakeup_template *ctx)
{
	return trace_enqueue(ctx->pid);
}

struct sched_wakeup_args {
	unsigned long long pad;
	char comm[16];
	int pid;
	int prio;
	int success;
	int target_cpu;
};

SEC("tracepoint/sched/sched_wakeup")
int sched_wakeup_entry(struct trace_event_raw_sched_wakeup_template *ctx)
{
	return trace_enqueue(ctx->pid);
}

struct task_struct___5_14 {
	unsigned int	__state;
} __attribute__((preserve_access_index));

long get_task_state(struct task_struct *task)
{
	long state;

	if (task == NULL)
		return -1;

	if (bpf_core_field_exists(task->state))
		state = BPF_CORE_READ(task, state);
	else {
		struct task_struct___5_14 *task_new = (struct task_struct___5_14 *)task;
		state = (long)BPF_CORE_READ(task_new, __state);
	}

	return state;
}

SEC("raw_tracepoint/sched_switch")
int sched_switch_entry(struct bpf_raw_tracepoint_args *ctx)
{
	u32 prev_pid, next_pid, g_key = 0;
	u64 now, *tsp, delta;
	bool is_voluntary;
	long state;
	struct stat_t *entry;
	struct g_stat_t *g_entry;

	// TP_PROTO(bool preempt, struct task_struct *prev, struct task_struct
	// *next)
	struct task_struct *prev = (struct task_struct *)ctx->args[1];
	struct task_struct *next = (struct task_struct *)ctx->args[2];

#ifdef TG_ADDR_KEY
	// get task_group addr: task_struct->sched_task_group
	u64 key = (u64)_(prev->sched_task_group);
#else
	// get pid ns id: task_struct->nsproxy->pid_ns_for_children->ns.inum
	u32 key = BPF_CORE_READ(prev, nsproxy, pid_ns_for_children, ns.inum);
#endif

	state = get_task_state(prev);

	// ivcsw: treat like an enqueue event and store timestamp
	prev_pid = _(prev->pid);
	if (state == TASK_RUNNING) {
		if (prev_pid != 0) {
			now = bpf_ktime_get_ns();
			bpf_map_update_elem(&latency, &prev_pid, &now,
					    COMPAT_BPF_ANY);
		}
		is_voluntary = 0;
	} else {
		is_voluntary = 1;
	}

	g_entry = bpf_map_lookup_elem(&cpu_host_metric, &g_key);
	if (!g_entry) {
		// init global counts map
		struct g_stat_t g_new_stat = {
			.g_nvcsw   = 0,
			.g_nivcsw  = 0,
			.g_nlat_01 = 0,
			.g_nlat_02 = 0,
			.g_nlat_03 = 0,
			.g_nlat_04 = 0,
		};
		bpf_map_update_elem(&cpu_host_metric, &g_key, &g_new_stat,
				    COMPAT_BPF_NOEXIST);
		g_entry = bpf_map_lookup_elem(&cpu_host_metric, &g_key);
		if (!g_entry)
			return 0;
	}

	// When use pid namespace id as key, sometimes we would encounter
	// null id because task->nsproxy is freed, usually means that this
	// task is almost dead (zombie), so ignore it.
	if (key && prev_pid) {
		entry = bpf_map_lookup_elem(&cpu_tg_metric, &key);
		if (!entry) {
			struct stat_t new_stat = {
				.nvcsw	 = 0,
				.nivcsw	 = 0,
				.nlat_01 = 0,
				.nlat_02 = 0,
				.nlat_03 = 0,
				.nlat_04 = 0,
			};
			bpf_map_update_elem(&cpu_tg_metric, &key, &new_stat,
					    COMPAT_BPF_NOEXIST);
			entry = bpf_map_lookup_elem(&cpu_tg_metric, &key);
			if (!entry)
				return 0;
		}

		if (is_voluntary) {
			__sync_fetch_and_add(&entry->nvcsw, 1);
			__sync_fetch_and_add(&g_entry->g_nvcsw, 1);
		} else {
			__sync_fetch_and_add(&entry->nivcsw, 1);
			__sync_fetch_and_add(&g_entry->g_nivcsw, 1);
		}
	}

	// trace_sched_switch is called under prev != next, no need to check
	// again.

	next_pid = _(next->pid);
	// ignore idle
	if (next_pid == 0)
		return 0;

	// fetch timestamp and calculate delta
	tsp = bpf_map_lookup_elem(&latency, &next_pid);
	if (tsp == 0 || *tsp == 0) {
		return 0; // missed enqueue
	}

	now   = bpf_ktime_get_ns();
	delta = now - *tsp;
	bpf_map_delete_elem(&latency, &next_pid);

#ifdef TG_ADDR_KEY
	key = (u64)_(next->sched_task_group);
#else
	key = BPF_CORE_READ(next, nsproxy, pid_ns_for_children, ns.inum);
#endif

	if (key) {
		entry = bpf_map_lookup_elem(&cpu_tg_metric, &key);
		if (!entry) {
			struct stat_t new_stat = {
				.nvcsw	 = 0,
				.nivcsw	 = 0,
				.nlat_01 = 0,
				.nlat_02 = 0,
				.nlat_03 = 0,
				.nlat_04 = 0,
			};
			bpf_map_update_elem(&cpu_tg_metric, &key, &new_stat,
					    COMPAT_BPF_NOEXIST);
			entry = bpf_map_lookup_elem(&cpu_tg_metric, &key);
			if (!entry)
				return 0;
		}

		if (delta < 10 * NSEC_PER_MSEC) {
			__sync_fetch_and_add(&entry->nlat_01, 1);
			__sync_fetch_and_add(&g_entry->g_nlat_01, 1);
		} else if (delta < 20 * NSEC_PER_MSEC) {
			__sync_fetch_and_add(&entry->nlat_02, 1);
			__sync_fetch_and_add(&g_entry->g_nlat_02, 1);
		} else if (delta < 50 * NSEC_PER_MSEC) {
			__sync_fetch_and_add(&entry->nlat_03, 1);
			__sync_fetch_and_add(&g_entry->g_nlat_03, 1);
		} else {
			__sync_fetch_and_add(&entry->nlat_04, 1);
			__sync_fetch_and_add(&g_entry->g_nlat_04, 1);
		}
	}

	return 0;
}

SEC("raw_tracepoint/sched_process_exit")
int sched_process_exit_entry(struct bpf_raw_tracepoint_args *ctx)
{
	u32 pid;

	// TP_PROTO(struct task_struct *tsk)
	struct task_struct *p = (struct task_struct *)ctx->args[0];

	pid = _(p->pid);
	/*
	 * check latency table to fix latency table overflow in below scenario:
	 * when wake up the target task, but the target task always running in
	 * the other cpu, the target cpu will never be the next pid, because the
	 * target task will be exiting, the latency item never delete.
	 * To avoid latency table overflow, we should delete the latency item in
	 * exit process.
	 */

	if (bpf_map_lookup_elem(&latency, &pid)) {
		bpf_map_delete_elem(&latency, &pid);
	}

	return 0;
}

#ifdef TG_ADDR_KEY
// When cgroup is removed, the record should be deleted.
SEC("kprobe/free_fair_sched_group")
int free_fair_sched_group_entry(struct pt_regs *ctx)
{
	struct task_group *tg = (void *)PT_REGS_PARM1(ctx);
	struct stat_t *entry;

	entry = bpf_map_lookup_elem(&cpu_tg_metric, &tg);
	if (entry)
		bpf_map_delete_elem(&cpu_tg_metric, &tg);

	return 0;
}
#else
// When pid namespace is destroyed, the record should be deleted.
SEC("kprobe/destroy_pid_namespace")
int destroy_pid_namespace_entry(struct pt_regs *ctx)
{
	struct pid_namespace *ns = (void *)PT_REGS_PARM1(ctx);
	struct stat_t *entry;

	// ns->ns.inum
	u32 pidns = BPF_CORE_READ(ns, ns.inum);
	entry	  = bpf_map_lookup_elem(&cpu_tg_metric, &pidns);
	if (entry)
		bpf_map_delete_elem(&cpu_tg_metric, &pidns);

	return 0;
}
#endif
