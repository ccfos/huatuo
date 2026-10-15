// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef HUATUO_MOCK_TASK_H
#define HUATUO_MOCK_TASK_H

#include <errno.h>
#include <pthread.h>
#include <sched.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <unistd.h>

struct mock_task_ioctl {
	int fd;
	unsigned long command;
	unsigned long argument;
	int threaded;
	int error;
	int cpu;
	pid_t pid;
	pid_t tid;
	char comm[16];
};

static void *mock_task_invoke(void *argument)
{
	struct mock_task_ioctl *task = argument;
	cpu_set_t *affinity;
	size_t affinity_size;

	task->cpu = sched_getcpu();
	if (task->cpu < 0) {
		task->error = errno;
		return NULL;
	}
	/* Keep the expected CPU stable until the synchronous hook returns. */
	affinity_size = CPU_ALLOC_SIZE(task->cpu + 1);
	affinity = CPU_ALLOC(task->cpu + 1);
	if (!affinity) {
		task->error = ENOMEM;
		return NULL;
	}
	CPU_ZERO_S(affinity_size, affinity);
	CPU_SET_S(task->cpu, affinity_size, affinity);
	if (sched_setaffinity(0, affinity_size, affinity) < 0)
		task->error = errno;
	CPU_FREE(affinity);
	if (task->error)
		return NULL;
	if ((task->threaded && prctl(PR_SET_NAME, "mock-thread-123456") < 0) ||
	    prctl(PR_GET_NAME, task->comm) < 0) {
		task->error = errno;
		return NULL;
	}
	task->pid = getpid();
	task->tid = syscall(SYS_gettid);
	if (ioctl(task->fd, task->command, task->argument) < 0)
		task->error = errno;
	return NULL;
}

static int mock_task_trigger(int fd, unsigned long command,
			     unsigned long argument, int threaded)
{
	struct mock_task_ioctl task = {
		.fd = fd, .command = command, .argument = argument,
		.threaded = threaded,
	};
	pthread_t worker;
	int result;

	if (threaded) {
		result = pthread_create(&worker, NULL, mock_task_invoke, &task);
		if (result) {
			errno = result;
			perror("create mock worker");
			return 1;
		}
		result = pthread_join(worker, NULL);
		if (result) {
			errno = result;
			perror("join mock worker");
			return 1;
		}
	} else {
		mock_task_invoke(&task);
	}
	if (task.error) {
		errno = task.error;
		perror("trigger mock task");
		return 1;
	}
	printf("{\"pid\":%d,\"tid\":%d,\"cpu\":%d,\"comm\":\"%s\"}\n",
	       task.pid, task.tid, task.cpu, task.comm);
	return 0;
}

#endif
