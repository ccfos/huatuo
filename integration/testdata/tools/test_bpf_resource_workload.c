// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/* Generate real BPF resources; the calling Shell script owns all assertions. */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <linux/bpf.h>
#include <pthread.h>
#include <sched.h>
#include <signal.h>
#include <spawn.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

static const char *directory;
static volatile sig_atomic_t stopped;
static atomic_bool race_stop;
extern char **environ;

static int bpf_call(int command, union bpf_attr *attributes)
{
	return syscall(SYS_bpf, command, attributes, sizeof(*attributes));
}

static int create_map(void)
{
	union bpf_attr attributes = {
		.map_type = BPF_MAP_TYPE_ARRAY,
		.key_size = sizeof(uint32_t),
		.value_size = sizeof(uint64_t),
		.max_entries = 1,
		.map_name = "leak_workload",
	};

	return bpf_call(BPF_MAP_CREATE, &attributes);
}

static int create_program(void)
{
	struct bpf_insn instructions[] = {
		{.code = BPF_ALU64 | BPF_MOV | BPF_K, .dst_reg = BPF_REG_0},
		{.code = BPF_JMP | BPF_EXIT},
	};
	union bpf_attr attributes = {
		.prog_type = BPF_PROG_TYPE_RAW_TRACEPOINT,
		.insn_cnt = sizeof(instructions) / sizeof(instructions[0]),
		.insns = (uint64_t)(uintptr_t)instructions,
		.license = (uint64_t)(uintptr_t)"GPL",
		.prog_name = "leak_workload",
	};

	return bpf_call(BPF_PROG_LOAD, &attributes);
}

static int pin_fd(int fd, const char *name)
{
	char path[PATH_MAX];
	union bpf_attr attributes = {.bpf_fd = fd};

	if (snprintf(path, sizeof(path), "%s/%s", directory, name) >=
	    (int)sizeof(path)) {
		errno = ENAMETOOLONG;
		return -1;
	}
	attributes.pathname = (uint64_t)(uintptr_t)path;
	return bpf_call(BPF_OBJ_PIN, &attributes);
}

static int create_pinned_map(void)
{
	int fd;
	int status;

	fd = create_map();
	if (fd < 0)
		return -1;
	status = pin_fd(fd, "map");
	close(fd);
	return status;
}

static void on_term(int signal)
{
	(void)signal;
	stopped = 1;
}

static int wait_for_term(const char *ready_file)
{
	struct sigaction action = {.sa_handler = on_term};
	sigset_t blocked;
	sigset_t previous;
	int fd;

	sigemptyset(&blocked);
	sigaddset(&blocked, SIGTERM);
	if (sigprocmask(SIG_BLOCK, &blocked, &previous))
		return -1;
	sigemptyset(&action.sa_mask);
	if (sigaction(SIGTERM, &action, NULL))
		return -1;
	fd = open(ready_file, O_WRONLY | O_CREAT | O_CLOEXEC, 0600);
	if (fd < 0)
		return -1;
	close(fd);
	while (!stopped)
		sigsuspend(&previous);
	return sigprocmask(SIG_SETMASK, &previous, NULL);
}

static void *close_shared_fd(void *unused)
{
	(void)unused;
	while (!atomic_load_explicit(&race_stop, memory_order_relaxed))
		close(3);
	return NULL;
}

static void *spawn_children(void *unused)
{
	char *arguments[] = {"true", NULL};
	pid_t child;

	(void)unused;
	while (!atomic_load_explicit(&race_stop, memory_order_relaxed)) {
		if (posix_spawn(&child, "/bin/true", NULL, NULL, arguments,
				environ))
			return (void *)1;
		while (waitpid(child, NULL, 0) < 0)
			if (errno != EINTR)
				return (void *)1;
	}
	return NULL;
}

int main(int argc, char **argv)
{
	const struct rlimit memlock = {RLIM_INFINITY, RLIM_INFINITY};
	union bpf_attr attributes = {0};
	pthread_t closer;
	pid_t parent;
	pid_t child;
	int status = -1;
	void *thread_status;
	int fd;
	int link;
	int i;

	if (argc < 3) {
		fprintf(stderr, "usage: %s MODE PIN_DIRECTORY [BORROWED_PIN]\n",
			argv[0]);
		return 2;
	}
	/* Older kernels charge BPF memory to the UID, including the running
	 * Agent. The workload needs its own limit raised as well. */
	if (setrlimit(RLIMIT_MEMLOCK, &memlock)) {
		perror("raise BPF workload memlock limit");
		return 1;
	}
	directory = argv[2];
	if (!strcmp(argv[1], "map")) {
		status = create_pinned_map();
	} else if (!strcmp(argv[1], "late")) {
		if (argc == 4 && wait_for_term(argv[3]) == 0)
			status = create_pinned_map();
	} else if (!strcmp(argv[1], "orphan")) {
		parent = getpid();
		child = fork();
		if (child < 0)
			return 1;
		if (child)
			return 0;
		while (getppid() == parent)
			sched_yield();
		status = create_pinned_map();
	} else if (!strcmp(argv[1], "prog") || !strcmp(argv[1], "link") ||
		   !strcmp(argv[1], "raw")) {
		fd = create_program();
		if (fd >= 0) {
			if (!strcmp(argv[1], "prog")) {
				status = pin_fd(fd, "prog");
			} else {
				attributes.raw_tracepoint.name =
					(uint64_t)(uintptr_t)"sys_enter";
				attributes.raw_tracepoint.prog_fd = fd;
				link = bpf_call(BPF_RAW_TRACEPOINT_OPEN,
						&attributes);
				if (link >= 0) {
					status = !strcmp(argv[1], "raw")
							 ? 0
							 : pin_fd(link, "link");
					close(link);
				}
			}
			close(fd);
		}
	} else if (!strcmp(argv[1], "borrowed") && argc == 4) {
		attributes.pathname = (uint64_t)(uintptr_t)argv[3];
		fd = bpf_call(BPF_OBJ_GET, &attributes);
		if (fd >= 0) {
			status = pin_fd(fd, "borrowed");
			close(fd);
		}
	} else if (!strcmp(argv[1], "race") || !strcmp(argv[1], "fork_race")) {
		if (pthread_create(&closer, NULL,
				   !strcmp(argv[1], "race") ? close_shared_fd
							    : spawn_children,
				   NULL))
			return 1;
		status = 0;
		for (i = 0; i < 100; i++) {
			fd = create_map();
			if (fd < 0) {
				status = -1;
				break;
			}
			close(fd);
		}
		atomic_store_explicit(&race_stop, true, memory_order_relaxed);
		pthread_join(closer, &thread_status);
		if (thread_status)
			status = -1;
	} else if (!strcmp(argv[1], "unsupported")) {
		status = bpf_call(BPF_PROG_ATTACH, &attributes);
	} else {
		fprintf(stderr, "unsupported BPF workload: %s\n", argv[1]);
		return 2;
	}
	if (status < 0) {
		perror("create BPF workload resource");
		return 1;
	}
	return 0;
}
