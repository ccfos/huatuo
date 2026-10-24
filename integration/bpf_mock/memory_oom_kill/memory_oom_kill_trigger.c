// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include <errno.h>
#include <fcntl.h>
#include <getopt.h>
#include <inttypes.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/wait.h>
#include <unistd.h>

#include "memory_oom_kill_mock.h"
#include "../common/task_identity.h"

static int parse_number(const char *text, unsigned long *value, int allow_zero)
{
	char *end;

	errno = 0;
	*value = strtoul(text, &end, 0);
	return errno || text[0] < '0' || text[0] > '9' || *end ||
	       (!allow_zero && !*value);
}

static pid_t read_host_tgid(const char *host_proc)
{
	pid_t pid, tid;

	return read_mock_task_ids(host_proc, &pid, &tid) ? -1 : pid;
}

int main(int argc, char **argv)
{
	const char *names[] = {
	    "same-task",    "other-task",  "memcg",	    "other-task-memcg",
	    "null-control", "null-victim", "pointer-chain",
	};
	const struct option options[] = {
	    {"host-proc", required_argument, NULL, 'p'},
	    {"memory-css", required_argument, NULL, 'c'},
	    {NULL, 0, NULL, 0},
	};
	const char *host_proc = NULL;
	struct oom_mock_css_query css = {};
	struct oom_mock_request request = {};
	unsigned long pages, usage = 0, memory_css = 0;
	pid_t child = -1, trigger_tgid, victim_tgid;
	int fd, result = 1, ready[2], option;
	ssize_t received;
	char comm[16] = {};

	opterr = 0;
	while ((option = getopt_long(argc, argv, "", options, NULL)) != -1) {
		switch (option) {
		case 'p':
			host_proc = optarg;
			break;
		case 'c':
			if (parse_number(optarg, &memory_css, 0))
				goto usage;
			break;
		default:
			goto usage;
		}
	}
	if (argc - optind != 2 && argc - optind != 3)
		goto usage;
	for (request.scenario = 0;
	     request.scenario < sizeof(names) / sizeof(names[0]);
	     request.scenario++)
		if (!strcmp(argv[optind], names[request.scenario]))
			break;
	if (request.scenario == sizeof(names) / sizeof(names[0]) ||
	    parse_number(argv[optind + 1], &pages, 0) ||
	    (argc - optind == 3 && parse_number(argv[optind + 2], &usage, 1)) ||
	    (memory_css && request.scenario == OOM_MOCK_POINTER_CHAIN))
		goto usage;
	request.total_pages = pages;
	request.usage_pages = usage;

	if (set_mock_task_name()) {
		perror("set mock task name");
		return 1;
	}
	fd = open("/dev/memory_oom_kill_mock", O_RDONLY);
	if (fd < 0) {
		perror("open OOM mock device");
		return 1;
	}

	trigger_tgid = read_host_tgid(host_proc);
	if (trigger_tgid < 0) {
		perror("read trigger host TGID");
		goto out;
	}
	victim_tgid = trigger_tgid;
	if (request.scenario == OOM_MOCK_OTHER_TASK ||
	    request.scenario == OOM_MOCK_OTHER_TASK_MEMCG) {
		if (pipe(ready) < 0) {
			perror("create victim readiness pipe");
			goto out;
		}
		child = fork();
		if (child == 0) {
			close(ready[0]);
			victim_tgid = read_host_tgid(host_proc);
			if (victim_tgid < 0 ||
			    prctl(PR_SET_NAME, "oom-mock-victim") < 0 ||
			    write(ready[1], &victim_tgid,
				  sizeof(victim_tgid)) != sizeof(victim_tgid))
				_exit(1);
			close(ready[1]);
			for (;;)
				pause();
		}
		close(ready[1]);
		if (child < 0) {
			perror("fork mock victim");
			close(ready[0]);
			goto out;
		}
		received = read(ready[0], &victim_tgid, sizeof(victim_tgid));
		if (received != sizeof(victim_tgid)) {
			if (received >= 0)
				errno = EIO;
			perror("read mock victim host TGID");
			close(ready[0]);
			goto out;
		}
		close(ready[0]);
		request.victim_pid = child;
	}
	if (request.scenario == OOM_MOCK_NULL_VICTIM)
		victim_tgid = 0;
	else if (request.scenario == OOM_MOCK_POINTER_CHAIN)
		victim_tgid = OOM_MOCK_SYNTHETIC_PID;

	if (memory_css) {
		css.trigger_css = memory_css;
		css.victim_css = victim_tgid ? memory_css : 0;
	} else {
		css.scenario = request.scenario;
		css.victim_pid = request.victim_pid;
		if (ioctl(fd, HUATUO_OOM_MOCK_GET_CSS, &css) < 0) {
			perror("get OOM fixture CSS");
			goto out;
		}
	}
	if (prctl(PR_GET_NAME, comm) < 0) {
		perror("read trigger command name");
		goto out;
	}
	if (ioctl(fd, HUATUO_OOM_MOCK_RUN, &request) < 0) {
		perror("trigger OOM scenario");
		goto out;
	}
	printf("{\"trigger_pid\":%d,\"victim_pid\":%d,\"trigger_css\":%" PRIu64
	       ",\"victim_css\":%" PRIu64 ",\"trigger_comm\":\"%s\"}\n",
	       trigger_tgid, victim_tgid, (uint64_t)css.trigger_css,
	       (uint64_t)css.victim_css, comm);
	result = 0;
out:
	if (child > 0) {
		kill(child, SIGTERM);
		waitpid(child, NULL, 0);
	}
	close(fd);
	return result;
usage:
	fprintf(stderr,
		"usage: %s [--host-proc DIR] [--memory-css HEX] <scenario> "
		"<total-pages> [usage-pages]\n",
		argv[0]);
	return 2;
}
