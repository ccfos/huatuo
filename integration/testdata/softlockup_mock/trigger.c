// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

#include "softlockup_mock.h"

int main(int argc, char **argv)
{
	unsigned long scenario;
	int fd;

	if (argc != 2) {
		fprintf(stderr, "usage: %s <softlockup|other>\n", argv[0]);
		return 2;
	}
	if (!strcmp(argv[1], "softlockup"))
		scenario = SOFTLOCKUP_MOCK_TAINT_SOFTLOCKUP;
	else if (!strcmp(argv[1], "other"))
		scenario = SOFTLOCKUP_MOCK_TAINT_USER;
	else {
		fprintf(stderr, "unknown scenario: %s; use softlockup or other\n",
			argv[1]);
		return 2;
	}

	fd = open("/dev/huatuo_softlockup_mock", O_RDONLY);
	if (fd < 0) {
		perror("open softlockup mock device");
		return 1;
	}
	if (ioctl(fd, HUATUO_SOFTLOCKUP_MOCK_RUN, scenario) < 0) {
		perror("trigger softlockup mock");
		close(fd);
		return 1;
	}
	close(fd);
	return 0;
}
