// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#define _GNU_SOURCE

#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

#include "system_softlockup_mock.h"
#include "../common/task.h"

int main(int argc, char **argv)
{
	struct softlockup_mock_request request = {};
	struct mock_task_options options = {
	    .scenario = "current-task",
	    .pin_cpu = 1,
	};
	int fd, result, threaded = argc == 3 && !strcmp(argv[2], "thread");

	if (argc != 2 && !threaded) {
		fprintf(stderr, "usage: %s <softlockup|other> [thread]\n",
			argv[0]);
		return 2;
	}
	if (!strcmp(argv[1], "softlockup"))
		request.scenario = SOFTLOCKUP_MOCK_TAINT_SOFTLOCKUP;
	else if (!strcmp(argv[1], "other"))
		request.scenario = SOFTLOCKUP_MOCK_TAINT_USER;
	else {
		fprintf(stderr,
			"unknown scenario: %s; use softlockup or other\n",
			argv[1]);
		return 2;
	}

	if (set_mock_task_name()) {
		perror("set mock task name");
		return 1;
	}
	fd = open("/dev/system_softlockup_mock", O_RDONLY);
	if (fd < 0) {
		perror("open softlockup mock device");
		return 1;
	}
	options.scenario = argv[1];
	options.threaded = threaded;
	result = mock_task_trigger(fd, HUATUO_SOFTLOCKUP_MOCK_RUN, &request,
				   &options);
	close(fd);
	return result;
}
