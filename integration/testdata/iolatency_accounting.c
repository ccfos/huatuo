// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include <limits.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

/* Replace the kernel/helper boundary; all accounting stays in production. */
#define __BPF_HELPERS__
#define __BPF_CORE_READ_H__
#define __BPF_TRACING_H__
typedef uint64_t u64;
typedef uint32_t u32;
typedef uint8_t u8;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define SEC(name)
#define __uint(name, value) int (*name)[value]
#define __type(name, value) value *name
#define BPF_MAP_TYPE_HASH 1
#define PT_REGS_PARM1(ctx) ((ctx)->arg1)
#define PT_REGS_PARM2(ctx) ((ctx)->arg2)
#define READ_ONE(src, field) ((src)->field)
#define READ_TWO(src, field, next) ((src)->field->next)
#define READ_SELECT(_1, _2, NAME, ...) NAME
#define BPF_CORE_READ(src, ...) \
	READ_SELECT(__VA_ARGS__, READ_TWO, READ_ONE)(src, __VA_ARGS__)
#define BPF_CORE_READ_INTO(dst, src, ...) \
	(*(dst) = (__typeof__(*(dst)))BPF_CORE_READ(src, __VA_ARGS__))
#define bpf_core_field_exists(field) true

struct gendisk {
	u32 major, minor;
};
struct block_device {
	struct gendisk *bd_disk;
};
struct blkcg_gq {
	void *blkcg;
};
struct bio {
	u64 bi_issue;
	struct bio *bi_next;
	struct blkcg_gq *bi_blkg;
	struct gendisk *bi_disk;
	u8 bi_partno;
};
struct request {
	struct bio *bio;
};
struct request_queue {
	struct blkcg_gq *root_blkg;
};
struct pt_regs {
	u64 arg1, arg2;
};

static u64 bpf_ktime_get_ns(void);
static long bpf_probe_read(void *dst, u32 size, const void *src);
static void *bpf_map_lookup_elem(const void *map, const void *key);
static long bpf_map_delete_elem(const void *map, const void *key);
static long bpf_map_update_elem(const void *map, const void *key,
			       const void *value, u64 flags);

#include "../../bpf/iolatency_tracing.c"

static struct gendisk disk = {8, 16};
static int css;
static struct blkcg_gq blkg = {&css};
static struct {
	u64 before;
	struct disk_entry value;
	u64 after;
} disk_slot;
static struct {
	u64 before;
	struct blkgq_entry value;
	u64 after;
} cg_slot;
static bool disk_present, cg_present, map_full;
static unsigned int updates, rejected_inserts, failures;
static pthread_mutex_t map_lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_barrier_t race_missed, race_inserted;
static _Thread_local struct bio current_bio;
static _Thread_local u64 now, start;
static _Thread_local bool start_present, issue_read_error, force_stale_miss;
static _Thread_local unsigned int deletes;

static u64 bpf_ktime_get_ns(void)
{
	return now;
}

static long bpf_probe_read(void *dst, u32 size, const void *src)
{
	if (issue_read_error && src == &current_bio.bi_issue)
		return -1;
	memcpy(dst, src, size);
	return 0;
}

static void *bpf_map_lookup_elem(const void *map, const void *key)
{
	if (map == &bio_start_time)
		return start_present && *(const u64 *)key == (u64)&current_bio
			? &start : NULL;
	if (map == &blkcg_map)
		return cg_present && *(const u64 *)key == (u64)&css
			? &cg_slot.value : NULL;
	if (map == &blkdisk_map && *(const u64 *)key == (u64)&disk) {
		pthread_mutex_lock(&map_lock);
		void *entry = disk_present ? &disk_slot.value : NULL;
		pthread_mutex_unlock(&map_lock);
		if (!entry && force_stale_miss) {
			force_stale_miss = false;
			pthread_barrier_wait(&race_missed);
			pthread_barrier_wait(&race_inserted);
		}
		return entry;
	}
	return NULL;
}

static long bpf_map_delete_elem(const void *map, const void *key)
{
	if (map != &bio_start_time || *(const u64 *)key != (u64)&current_bio)
		return -1;
	start_present = false;
	deletes++;
	return 0;
}

static long bpf_map_update_elem(const void *map, const void *key,
			       const void *value, u64 flags)
{
	if (map != &blkdisk_map || *(const u64 *)key != (u64)&disk)
		return -1;
	pthread_mutex_lock(&map_lock);
	updates++;
	long result = 0;

	if (disk_present && flags == COMPAT_BPF_NOEXIST) {
		rejected_inserts++;
		result = -17;
	} else if (map_full) {
		result = -12;
	} else {
		disk_slot.value = *(const struct disk_entry *)value;
		disk_present = true;
	}
	pthread_mutex_unlock(&map_lock);
	return result;
}

static void expect(const char *name, const char *field, u64 actual, u64 want)
{
	if (actual == want)
		return;
	fprintf(stderr, "%s (%s): got %llu, want %llu\n", name, field,
		(unsigned long long)actual, (unsigned long long)want);
	failures++;
}

static void reset(bool existing)
{
	memset(&disk_slot, 0, sizeof(disk_slot));
	memset(&cg_slot, 0, sizeof(cg_slot));
	disk_slot.before = cg_slot.before = 0x12345678;
	disk_slot.after = cg_slot.after = 0x87654321;
	disk_present = existing;
	cg_present = true;
	map_full = false;
	updates = rejected_inserts = deletes = 0;
	force_stale_miss = false;
	cg_slot.value.blkgq = (u64)&blkg;
	cg_slot.value.disk = (u64)&disk;
	if (existing) {
		disk_slot.value.disk = (u64)&disk;
		disk_slot.value.major = 8;
		disk_slot.value.minor = 19;
		disk_slot.value.freeze_nr = 17;
	}
}

static void complete(u64 q_delta, u64 d_delta, bool read_error, bool missing)
{
	now = 1000000000;
	current_bio = (struct bio){
		.bi_issue = now - q_delta,
		.bi_blkg = &blkg,
		.bi_disk = &disk,
		.bi_partno = 3,
	};
	start = now - d_delta;
	start_present = !missing;
	issue_read_error = read_error;
	struct pt_regs ctx = {.arg2 = (u64)&current_bio};

	kprobe_done_bio(&ctx);
}

static void expect_state(const char *name, int q, int d, u64 count,
			 bool present, u64 freeze)
{
	expect(name, "disk present", disk_present, present);
	expect(name, "freeze preserved", disk_slot.value.freeze_nr, freeze);
	expect(name, "disk prefix canary", disk_slot.before, 0x12345678);
	expect(name, "disk suffix canary", disk_slot.after, 0x87654321);
	expect(name, "cgroup prefix canary", cg_slot.before, 0x12345678);
	expect(name, "cgroup suffix canary", cg_slot.after, 0x87654321);
	expect(name, "cgroup identity", cg_slot.value.blkgq, (u64)&blkg);
	expect(name, "cgroup disk identity", cg_slot.value.disk, (u64)&disk);
	if (present) {
		expect(name, "disk identity", disk_slot.value.disk, (u64)&disk);
		expect(name, "disk major", disk_slot.value.major, 8);
		expect(name, "disk minor", disk_slot.value.minor, 19);
	}
	if (cg_present && (q >= 0 || d >= 0)) {
		expect(name, "cgroup major", cg_slot.value.major, 8);
		expect(name, "cgroup minor", cg_slot.value.minor, 19);
	}
	for (int i = 0; i < LATENCY_ZONE_MAX; i++) {
		char field[40];

		snprintf(field, sizeof(field), "disk q2c[%d]", i);
		expect(name, field, disk_slot.value.q2c_zone[i],
		       present && i == q ? count : 0);
		snprintf(field, sizeof(field), "disk d2c[%d]", i);
		expect(name, field, disk_slot.value.d2c_zone[i],
		       present && i == d ? count : 0);
		snprintf(field, sizeof(field), "cgroup q2c[%d]", i);
		expect(name, field, cg_slot.value.q2c_zone[i],
		       cg_present && i == q ? count : 0);
		snprintf(field, sizeof(field), "cgroup d2c[%d]", i);
		expect(name, field, cg_slot.value.d2c_zone[i],
		       cg_present && i == d ? count : 0);
	}
}

static void completion_cases(void)
{
	static const struct {
		const char *name;
		u64 q, d;
		bool read_error, missing;
		int q_zone, d_zone;
	} cases[] = {
		{"queue slow, device fast", 100000000, 1000000, false, false, 2, -1},
		{"queue read fails, device slow", 100000000, 50000000, true, false, -1, 1},
		{"device missing, queue slow", 100000000, 50000000, false, true, 2, -1},
		{"both slow", 100000000, 50000000, false, false, 2, 1},
		{"both fast", 1000000, 1000000, false, false, -1, -1},
		{"both unavailable", 100000000, 50000000, true, true, -1, -1},
		{"first and last buckets", 500000000, 20000000, false, false, 5, 0},
	};
	for (int existing = 0; existing < 2; existing++) {
		for (unsigned int i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
			bool slow = cases[i].q_zone >= 0 || cases[i].d_zone >= 0;
			char name[100];

			snprintf(name, sizeof(name), "%s (%s disk)", cases[i].name,
				 existing ? "existing" : "new");
			reset(existing);
			complete(cases[i].q, cases[i].d,
				 cases[i].read_error, cases[i].missing);
			expect_state(name, cases[i].q_zone, cases[i].d_zone, 1,
				     existing || slow, existing ? 17 : 0);
			expect(name, "start timestamp consumed", start_present, 0);
			expect(name, "timestamp deletes", deletes, !cases[i].missing);
			expect(name, "disk insert attempts", updates, !existing && slow);
		}
	}
	reset(false);
	for (unsigned int i = 0; i < 32; i++)
		complete(100000000, 50000000, false, false);
	expect_state("32 completions", 2, 1, 32, true, 0);
	expect("32 completions", "one insert", updates, 1);
	reset(false);
	map_full = true;
	complete(100000000, 50000000, false, false);
	expect_state("disk map full", 2, 1, 1, false, 0);
	expect("disk map full", "one failed insert", updates, 1);
	reset(false);
	cg_present = false;
	complete(100000000, 50000000, false, false);
	expect_state("cgroup absent", 2, 1, 1, true, 0);
}

static void index_bounds(void)
{
	const int invalid[] = {-1, LATENCY_ZONE_MAX, INT_MIN, INT_MAX};
	const int valid[] = {0, LATENCY_ZONE_MAX - 1};

	for (unsigned int i = 0; i < sizeof(invalid) / sizeof(invalid[0]); i++) {
		for (unsigned int j = 0; j < sizeof(valid) / sizeof(valid[0]); j++) {
			for (int invalid_q = 0; invalid_q < 2; invalid_q++) {
				int q = invalid_q ? invalid[i] : valid[j];
				int d = invalid_q ? valid[j] : invalid[i];
				char name[80];

				snprintf(name, sizeof(name), "direct indices q=%d d=%d", q, d);
				reset(true);
				complete(1000000, 1000000, false, false);
				blkcg_latency_account(&current_bio, q, d);
				blkdisk_latency_account(&current_bio, q, d);
				expect_state(name, invalid_q ? -1 : q, invalid_q ? d : -1,
					     1, true, 17);
			}
		}
	}
}

static void *competing_cpu(void *unused)
{
	(void)unused;
	pthread_barrier_wait(&race_missed);
	for (unsigned int i = 0; i < 3; i++)
		complete(100000000, 50000000, false, false);
	/* Existing freeze state must survive; freeze probe keying is out of scope. */
	__sync_fetch_and_add(&disk_slot.value.freeze_nr, 17);
	pthread_barrier_wait(&race_inserted);
	return NULL;
}

static void check_thread(int error, const char *operation)
{
	if (!error)
		return;
	fprintf(stderr, "%s: %s\n", operation, strerror(error));
	exit(2);
}

static void insertion_race(void)
{
	pthread_t thread;

	reset(false);
	check_thread(pthread_barrier_init(&race_missed, NULL, 2),
		     "initialize missed-lookup barrier");
	check_thread(pthread_barrier_init(&race_inserted, NULL, 2),
		     "initialize completed-insert barrier");
	check_thread(pthread_create(&thread, NULL, competing_cpu, NULL),
		     "create competing insertion worker");
	force_stale_miss = true;
	complete(100000000, 50000000, false, false);
	check_thread(pthread_join(thread, NULL), "join competing insertion worker");
	pthread_barrier_destroy(&race_missed);
	pthread_barrier_destroy(&race_inserted);
	expect_state("concurrent first insertion", 2, 1, 4, true, 17);
	expect("concurrent first insertion", "existing entry rejects insert",
	       rejected_inserts, 1);
}

static void *parallel_cpu(void *unused)
{
	(void)unused;
	pthread_barrier_wait(&race_missed);
	for (unsigned int i = 0; i < 10000; i++)
		complete(100000000, 50000000, false, false);
	return NULL;
}

static void parallel_completions(void)
{
	pthread_t threads[4];

	reset(true);
	/* Metadata initialization is not the concurrent-counter contract under test. */
	cg_slot.value.major = 8;
	cg_slot.value.minor = 19;
	check_thread(pthread_barrier_init(&race_missed, NULL, 5),
		     "initialize concurrent completion barrier");
	for (unsigned int i = 0; i < 4; i++)
		check_thread(pthread_create(&threads[i], NULL, parallel_cpu, NULL),
			     "create concurrent completion worker");
	pthread_barrier_wait(&race_missed);
	for (unsigned int i = 0; i < 4; i++)
		check_thread(pthread_join(threads[i], NULL),
			     "join concurrent completion worker");
	pthread_barrier_destroy(&race_missed);
	expect_state("four concurrent completion workers", 2, 1, 40000, true, 17);
	expect("four concurrent completion workers", "no insert", updates, 0);
}

static volatile u64 benchmark_q = 100000000;

static void benchmark(void)
{
	const unsigned int iterations = 50000000;
	const char *names[] = {"both slow", "queue only", "device only", "both fast"};

	for (unsigned int group = 0; group < 4; group++) {
		reset(true);
		clock_t begin = clock();

		for (unsigned int i = 0; i < iterations; i++)
			complete(group == 3 ? 1000000 : benchmark_q,
				 group == 1 || group == 3 ? 1000000 : 50000000,
				 group == 2, false);
		printf("%s: %.3f ns/completion (%u iterations; native helper stubs)\n",
		       names[group],
		       1e9 * (double)(clock() - begin) / CLOCKS_PER_SEC / iterations,
		       iterations);
	}
}

int main(int argc, char **argv)
{
	if (argc == 2 && strcmp(argv[1], "--benchmark") == 0) {
		benchmark();
		return 0;
	}
	if (argc == 2 && strcmp(argv[1], "--bounds") == 0) {
		index_bounds();
		if (failures)
			return 1;
		puts("I/O latency direct-accounting index bounds passed");
		return 0;
	}
	if (argc != 1) {
		fprintf(stderr, "usage: %s [--benchmark|--bounds]\n", argv[0]);
		return 2;
	}
	/* A broken path must fail instead of leaving the barrier test hung in CI. */
	alarm(10);
	completion_cases();
	insertion_race();
	parallel_completions();
	alarm(0);
	if (failures)
		return 1;
	puts("I/O latency completion-path regression passed");
	return 0;
}
