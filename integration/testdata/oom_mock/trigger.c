// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/ioctl.h>
#include <unistd.h>

#define HUATUO_OOM_MOCK_RUN _IO('H', 1)

int main(int argc, char **argv)
{
	char *end;
	unsigned long pages;
	int fd;

	if (argc != 2) {
		fprintf(stderr, "usage: %s <total-pages>\n", argv[0]);
		return 2;
	}

	errno = 0;
	pages = strtoul(argv[1], &end, 0);
	if (errno || *end || !pages) {
		fprintf(stderr, "invalid total-pages: %s\n", argv[1]);
		return 2;
	}

	fd = open("/dev/huatuo_oom_mock", O_RDONLY);
	if (fd < 0) {
		perror("open OOM mock device");
		return 1;
	}
	if (ioctl(fd, HUATUO_OOM_MOCK_RUN, pages) < 0) {
		perror("trigger OOM mock");
		close(fd);
		return 1;
	}
	close(fd);
	return 0;
}
