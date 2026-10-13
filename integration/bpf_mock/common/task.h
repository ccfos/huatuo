// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef HUATUO_MOCK_TASK_H
#define HUATUO_MOCK_TASK_H
#define _GNU_SOURCE

#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <pthread.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <time.h>
#include <unistd.h>

#include "task_identity.h"

struct mock_task_options {
	const char *scenario, *host_proc, *thread_name;
	int threaded, pin_cpu, gated;
};

struct mock_task_ioctl {
	int fd, error, cpu;
	unsigned long command;
	const void *request;
	const struct mock_task_options *options;
	sigset_t signals;
	pid_t pid, tid;
	char comm[16];
	uint64_t elapsed_ns;
};

static void mock_task_print(const struct mock_task_ioctl *task)
{
	printf("{\"scenario\":\"%s\",\"pid\":%d,\"tid\":%d,\"cpu\":%d,\"comm\":"
	       "\"%s\",\"elapsed_ns\":%" PRIu64 "}\n",
	       task->options->scenario, task->pid, task->tid, task->cpu,
	       task->comm, task->elapsed_ns);
	fflush(stdout);
}

static int mock_task_wait(struct mock_task_ioctl *task, int expected)
{
	int received, error = sigwait(&task->signals, &received);
	return error ? error : received == expected ? 0 : EINVAL;
}

static void *mock_task_invoke(void *argument)
{
	struct mock_task_ioctl *task = argument;
	const struct mock_task_options *options = task->options;
	struct timespec before, after;
	task->cpu = sched_getcpu();
	if (task->cpu < 0)
		goto failure;
	if (options->pin_cpu) {
		size_t size = CPU_ALLOC_SIZE(task->cpu + 1);
		cpu_set_t *affinity = CPU_ALLOC(task->cpu + 1);
		if (!affinity) {
			task->error = ENOMEM;
			return NULL;
		}
		CPU_ZERO_S(size, affinity);
		CPU_SET_S(task->cpu, size, affinity);
		if (sched_setaffinity(0, size, affinity))
			task->error = errno;
		CPU_FREE(affinity);
		if (task->error)
			return NULL;
	}
	if ((options->threaded && options->thread_name &&
	     prctl(PR_SET_NAME, options->thread_name)) ||
	    prctl(PR_GET_NAME, task->comm) ||
	    read_mock_task_ids(options->host_proc, &task->pid, &task->tid))
		goto failure;
	if (options->gated) {
		mock_task_print(task);
		task->error = mock_task_wait(task, SIGUSR1);
		if (task->error)
			return NULL;
	}
	if (clock_gettime(CLOCK_MONOTONIC, &before) ||
	    ioctl(task->fd, task->command, task->request) ||
	    clock_gettime(CLOCK_MONOTONIC, &after))
		goto failure;
	task->elapsed_ns =
	    (uint64_t)(after.tv_sec - before.tv_sec) * 1000000000 +
	    after.tv_nsec - before.tv_nsec;
	if (!options->gated) {
		mock_task_print(task);
	} else {
		puts("{\"triggered\":true}");
		fflush(stdout);
		/* Keep mappings alive until userspace symbolization finishes.
		 */
		task->error = mock_task_wait(task, SIGUSR2);
	}
	return NULL;
failure:
	task->error = errno;
	return NULL;
}

static int mock_task_trigger(int fd, unsigned long command, const void *request,
			     const struct mock_task_options *options)
{
	struct mock_task_ioctl task = {
	    .fd = fd,
	    .command = command,
	    .request = request,
	    .options = options,
	};
	sigset_t previous;
	pthread_t worker;
	int result;
	if (options->gated) {
		sigemptyset(&task.signals);
		sigaddset(&task.signals, SIGUSR1);
		sigaddset(&task.signals, SIGUSR2);
		task.error =
		    pthread_sigmask(SIG_BLOCK, &task.signals, &previous);
		if (task.error)
			goto done;
	}
	if (options->threaded) {
		result = pthread_create(&worker, NULL, mock_task_invoke, &task);
		if (!result)
			result = pthread_join(worker, NULL);
		if (result)
			task.error = result;
	} else {
		mock_task_invoke(&task);
	}
	if (options->gated)
		pthread_sigmask(SIG_SETMASK, &previous, NULL);
done:
	if (task.error) {
		errno = task.error;
		perror("trigger mock task");
		return 1;
	}
	return 0;
}

/* Profiler triggers share the same gated lifetime and command line. */
static inline int mock_profile_trigger(int argc, char **argv,
				       const char *device,
				       unsigned long command,
				       const void *request)
{
	struct mock_task_options options = {
	    .scenario = argv[0],
	    .thread_name = "mock-worker",
	    .gated = 1,
	};
	int fd, result;
	if ((argc != 2 && argc != 3) ||
	    (strcmp(argv[1], "main") && strcmp(argv[1], "thread"))) {
		fprintf(stderr,
			"expected <scenario> <main|thread> [host-proc]\n");
		return 2;
	}
	options.threaded = !strcmp(argv[1], "thread");
	options.host_proc = argc == 3 ? argv[2] : NULL;
	fd = open(device, O_RDONLY);
	if (fd < 0) {
		perror("open mock device");
		return 1;
	}
	result = mock_task_trigger(fd, command, request, &options);
	close(fd);
	return result;
}
#endif
