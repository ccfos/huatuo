// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/wait.h>
#include <unistd.h>

#include "oom_mock.h"

static int parse_pages(const char *text, unsigned long *pages, int allow_zero)
{
	char *end;

	errno = 0;
	*pages = strtoul(text, &end, 0);
	return errno || text[0] < '0' || text[0] > '9' || *end ||
		(!allow_zero && !*pages);
}

int main(int argc, char **argv)
{
	const char *names[] = {
		"same-task", "other-task", "memcg", "other-task-memcg",
		"null-control", "null-victim", "pointer-chain",
	};
	struct oom_mock_request request = {};
	unsigned long pages, usage = 0;
	pid_t child = -1;
	int fd, result = 1, ready[2];
	char signal_byte, comm[16] = {};

	if (argc != 3 && argc != 4)
		goto usage;
	for (request.scenario = 0; request.scenario < sizeof(names) / sizeof(names[0]); request.scenario++)
		if (!strcmp(argv[1], names[request.scenario]))
			break;
	if (request.scenario == sizeof(names) / sizeof(names[0]) ||
	    parse_pages(argv[2], &pages, 0) ||
	    (argc == 4 && parse_pages(argv[3], &usage, 1)))
		goto usage;
	request.total_pages = pages;
	request.usage_pages = usage;

	fd = open("/dev/huatuo_oom_mock", O_RDONLY);
	if (fd < 0) {
		perror("open OOM mock device");
		return 1;
	}

	if (request.scenario == OOM_MOCK_OTHER_TASK ||
	    request.scenario == OOM_MOCK_OTHER_TASK_MEMCG) {
		if (pipe(ready) < 0) {
			perror("create victim readiness pipe");
			goto out;
		}
		child = fork();
		if (child == 0) {
			close(ready[0]);
			if (prctl(PR_SET_NAME, "oom-mock-victim") < 0 ||
			    write(ready[1], "R", 1) != 1)
				_exit(1);
			close(ready[1]);
			for (;;)
				pause();
		}
		close(ready[1]);
		if (child < 0 || read(ready[0], &signal_byte, 1) != 1) {
			perror("start mock victim");
			close(ready[0]);
			goto out;
		}
		close(ready[0]);
		request.victim_pid = child;
	}
	if (ioctl(fd, HUATUO_OOM_MOCK_SCENARIO, &request) < 0) {
		perror("trigger OOM scenario");
		goto out;
	}
	if (prctl(PR_GET_NAME, comm) < 0) {
		perror("read trigger command name");
		goto out;
	}
	printf("{\"trigger_pid\":%u,\"victim_pid\":%u,\"trigger_css\":%" PRIu64
	       ",\"victim_css\":%" PRIu64 ",\"comm\":\"%s\"}\n",
	       request.trigger_tgid, request.victim_tgid,
	       (uint64_t)request.trigger_css, (uint64_t)request.victim_css, comm);
	result = 0;
out:
	if (child > 0) {
		kill(child, SIGTERM);
		waitpid(child, NULL, 0);
	}
	close(fd);
	return result;
usage:
	fprintf(stderr, "usage: %s <scenario> <total-pages> [usage-pages]\n", argv[0]);
	return 2;
}
