// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include <fcntl.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <unistd.h>

#include "hungtask_mock.h"

int main(int argc, char **argv)
{
	int fd;

	if (argc != 1) {
		fprintf(stderr, "usage: %s\n", argv[0]);
		return 2;
	}

	fd = open("/dev/huatuo_hungtask_mock", O_RDONLY);
	if (fd < 0) {
		perror("open hung task mock device");
		return 1;
	}
	if (ioctl(fd, HUATUO_HUNGTASK_MOCK_RUN, 0UL) < 0) {
		perror("trigger hung task mock");
		close(fd);
		return 1;
	}
	close(fd);
	return 0;
}
