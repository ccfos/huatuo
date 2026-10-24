// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include "../common/task.h"
#include "memory_reclaim_events_mock.h"

int main(int argc, char **argv)
{
	const struct reclaim_mock_request request = {
	    .scenario = RECLAIM_MOCK_CURRENT_TASK};
	struct mock_task_options options = {
	    .scenario = "current-task",
	};
	int fd, result;
	if ((argc != 2 && argc != 3) ||
	    (strcmp(argv[1], "main") && strcmp(argv[1], "thread"))) {
		fprintf(stderr, "usage: %s <main|thread> [host-proc]\n",
			argv[0]);
		return 2;
	}
	options.threaded = !strcmp(argv[1], "thread");
	options.host_proc = argc == 3 ? argv[2] : NULL;
	if (set_mock_task_name()) {
		perror("set mock task name");
		return 1;
	}
	fd = open("/dev/memory_reclaim_events_mock", O_RDONLY);
	if (fd < 0) {
		perror("open reclaim mock device");
		return 1;
	}
	result =
	    mock_task_trigger(fd, HUATUO_RECLAIM_MOCK_RUN, &request, &options);
	close(fd);
	return result;
}
