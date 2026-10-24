// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#define _GNU_SOURCE

#include <fcntl.h>
#include <string.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <unistd.h>

#include "system_hungtask_mock.h"
#include "../common/task.h"

int main(int argc, char **argv)
{
	struct hungtask_mock_request request = {
	    .scenario = HUNGTASK_MOCK_CURRENT_TASK,
	};
	struct mock_task_options options = {
	    .scenario = "current-task",
	    .pin_cpu = 1,
	};
	int fd, result, threaded = argc == 2 && !strcmp(argv[1], "thread");

	if (argc != 1 && !threaded) {
		fprintf(stderr, "usage: %s [thread]\n", argv[0]);
		return 2;
	}

	if (set_mock_task_name()) {
		perror("set mock task name");
		return 1;
	}
	fd = open("/dev/system_hungtask_mock", O_RDONLY);
	if (fd < 0) {
		perror("open hung task mock device");
		return 1;
	}
	options.threaded = threaded;
	result =
	    mock_task_trigger(fd, HUATUO_HUNGTASK_MOCK_RUN, &request, &options);
	close(fd);
	return result;
}
