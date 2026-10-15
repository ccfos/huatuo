// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#define _GNU_SOURCE

#include <fcntl.h>
#include <string.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <unistd.h>

#include "hungtask_mock.h"
#include "../common/task.h"

int main(int argc, char **argv)
{
	int fd, result, threaded = argc == 2 && !strcmp(argv[1], "thread");

	if (argc != 1 && !threaded) {
		fprintf(stderr, "usage: %s [thread]\n", argv[0]);
		return 2;
	}

	fd = open("/dev/huatuo_hungtask_mock", O_RDONLY);
	if (fd < 0) {
		perror("open hung task mock device");
		return 1;
	}
	result = mock_task_trigger(fd, HUATUO_HUNGTASK_MOCK_RUN, 0UL, threaded);
	close(fd);
	return result;
}
