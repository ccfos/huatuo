// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

/* Replace only the kernel/helper boundary, not the production arithmetic. */
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
#define bpf_core_field_exists(field) has_bi_issue

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
	/* Distinct offsets expose accidentally selecting the wrong field path. */
	u64 issue_time_ns;
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

static bool has_bi_issue;
static int read_error;
static const void *read_address;
static bool start_present;
static u64 start_value, start_key;
static unsigned int deletes;

static u64 bpf_ktime_get_ns(void);
static long bpf_probe_read(void *dst, u32 size, const void *src);
static void *bpf_map_lookup_elem(const void *map, const void *key);
static long bpf_map_delete_elem(const void *map, const void *key);
static long bpf_map_update_elem(const void *map, const void *key,
			       const void *value, u64 flags);

#include "../../bpf/iolatency_tracing.c"

static u64 bpf_ktime_get_ns(void)
{
	return 0;
}

static long bpf_probe_read(void *dst, u32 size, const void *src)
{
	read_address = src;
	if (read_error)
		return read_error;
	memcpy(dst, src, size);
	return 0;
}

static void *bpf_map_lookup_elem(const void *map, const void *key)
{
	if (map == &bio_start_time && start_present &&
	    *(const u64 *)key == start_key)
		return &start_value;
	return NULL;
}

static long bpf_map_delete_elem(const void *map, const void *key)
{
	if (map != &bio_start_time || *(const u64 *)key != start_key)
		return -1;
	deletes++;
	start_present = false;
	return 0;
}

static long bpf_map_update_elem(const void *map, const void *key,
			       const void *value, u64 flags)
{
	(void)map;
	(void)key;
	(void)value;
	(void)flags;
	return 0;
}

static unsigned int failures;

static void expect(const char *name, const char *path, int actual, int want)
{
	if (actual == want)
		return;
	fprintf(stderr, "%s (%s): got %d, want %d\n", name, path, actual, want);
	failures++;
}

static void check_latency(const char *name, u64 start, u64 now, int want)
{
	struct bio bio = {};

	for (int legacy = 0; legacy < 2; legacy++) {
		has_bi_issue = legacy;
		/* Old issue words pack flags; new kernels retain the full clock. */
		bio.bi_issue = start | (5ULL << 51);
		bio.issue_time_ns = start + 2 * (TIMESTAMP_MASK + 1);
		expect(name, legacy ? "q2c packed issue" : "q2c plain issue",
		       q2c_latency_index(&bio, now), want);
		expect(name, "q2c field address",
		       read_address == (legacy ? &bio.bi_issue : &bio.issue_time_ns), 1);
	}
	start_key = (u64)&bio;
	start_value = start;
	start_present = true;
	deletes = 0;
	expect(name, "d2c", d2c_latency_index(&bio, now), want);
	expect(name, "d2c deletes timestamp once", deletes, 1);
	expect(name, "d2c removes timestamp", start_present, 0);
	expect(name, "d2c repeated completion", d2c_latency_index(&bio, now), -1);
	expect(name, "d2c absent timestamp is not deleted", deletes, 1);
}

static void check_failures(void)
{
	struct bio bio = {};

	read_error = -1;
	for (int legacy = 0; legacy < 2; legacy++) {
		has_bi_issue = legacy;
		expect("read failure", legacy ? "q2c packed issue" : "q2c plain issue",
		       q2c_latency_index(&bio, 5000000000ULL), -1);
	}
	read_error = 0;
	for (int legacy = 0; legacy < 2; legacy++) {
		has_bi_issue = legacy;
		expect("zero issue timestamp", legacy ? "q2c packed issue" : "q2c plain issue",
		       q2c_latency_index(&bio, 100000000ULL), 2);
	}
	start_present = false;
	deletes = 0;
	expect("map miss", "d2c", d2c_latency_index(&bio, 5000000000ULL), -1);
	expect("map miss", "no delete", deletes, 0);
}

/* Runtime inputs prevent folding away the production arithmetic at -O2. */
static volatile u64 benchmark_now = 5000000000ULL;
static volatile u64 benchmark_delays[][2] = {
	{100000000ULL, 500000000ULL},
	{100000000ULL, 4400000000ULL},
};
static volatile int benchmark_sink;

static __attribute__((noinline)) int benchmark_sample(struct bio *bio, u64 now)
{
	start_value = bio->bi_issue;
	start_present = true;
	return q2c_latency_index(bio, now) + d2c_latency_index(bio, now);
}

static void benchmark(void)
{
	const unsigned int iterations = 100000000;
	const char *names[] = {"normal", "long-tail"};
	struct bio bio = {};

	start_key = (u64)&bio;
	has_bi_issue = true;
	for (unsigned int group = 0; group < 2; group++) {
		clock_t begin = clock();
		int sum = 0;

		for (unsigned int i = 0; i < iterations; i++) {
			u64 now = benchmark_now + i;

			bio.bi_issue = now - benchmark_delays[group][i % 2];
			sum += benchmark_sample(&bio, now);
		}
		benchmark_sink = sum;
		printf("%s q2c+d2c: %.3f ns/pair (%u iterations; native helper stubs)\n",
		       names[group],
		       1e9 * (double)(clock() - begin) / CLOCKS_PER_SEC / iterations,
		       iterations);
	}
}

int main(int argc, char **argv)
{
	static const struct {
		u64 delta;
		int zone;
	} cases[] = {
		{0, -1}, {19999999, -1}, {20000000, 0}, {20000001, 0},
		{29999999, 0}, {30000000, 0}, {30000001, 1},
		{49999999, 1}, {50000000, 1}, {50000001, 2},
		{99999999, 2}, {100000000, 2}, {100000001, 3},
		{199999999, 3}, {200000000, 3}, {200000001, 4},
		{399999999, 4}, {400000000, 4},
		{400000001, 5}, {2147483647ULL, 5}, {2147483648ULL, 5},
		{2147483649ULL, 5}, {4294967295ULL, 5}, {4294967296ULL, 5},
		{4294967297ULL, 5}, {4300000000ULL, 5},
		{4314967296ULL, 5}, {4319967296ULL, 5}, {4394967296ULL, 5},
		{4400000000ULL, 5}, {4694967296ULL, 5}, {8599934592ULL, 5},
		{30000000000ULL, 5},
	};
	char name[80];

	if (argc == 2 && strcmp(argv[1], "--benchmark") == 0) {
		benchmark();
		return 0;
	}
	if (argc != 1) {
		fprintf(stderr, "usage: %s [--benchmark]\n", argv[0]);
		return 2;
	}
	for (unsigned int i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
		u64 start = TIMESTAMP_MASK - 9999999;

		snprintf(name, sizeof(name), "delta=%llu ns",
			 (unsigned long long)cases[i].delta);
		check_latency(name, 0, cases[i].delta, cases[i].zone);
		snprintf(name, sizeof(name), "wrapped delta=%llu ns",
			 (unsigned long long)cases[i].delta);
		check_latency(name, start,
			      (start + cases[i].delta) & TIMESTAMP_MASK, cases[i].zone);
	}
	check_latency("nonzero start at 100 ms", 500000000, 600000000, 2);
	check_latency("large start at 100 ms",
		      1000000000000000ULL, 1000000100000000ULL, 2);
	check_latency("51-bit wrap at now=0", TIMESTAMP_MASK - 19999999, 0, 0);
	check_latency("51-bit wrap below 20 ms",
		      TIMESTAMP_MASK - 9999999, 9999999, -1);
	check_latency("51-bit wrap at 20 ms",
		      TIMESTAMP_MASK - 9999999, 10000000, 0);
	check_latency("51-bit wrap above 200 ms",
		      TIMESTAMP_MASK - 100000000, 100000000, 4);
	check_latency("51-bit wrap tail",
		      TIMESTAMP_MASK - 3000000000ULL, 1400000000ULL, 5);
	check_failures();
	if (failures)
		return 1;
	puts("I/O latency production-function regression passed");
	return 0;
}
