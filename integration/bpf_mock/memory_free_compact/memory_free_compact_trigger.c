// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include "../common/task.h"
#include "memory_free_compact_mock.h"

int main(int argc, char **argv)
{
	const struct {
		const char *name;
		enum memory_free_compact_scenario scenario;
	} scenarios[] = {
	    {"reclaim", FREE_COMPACT_RECLAIM},
	    {"compaction", FREE_COMPACT_COMPACTION},
	    {"unmatched-end", FREE_COMPACT_UNMATCHED_END},
	};
	struct memory_free_compact_request request;
	struct mock_task_options options = {.scenario =
						argc == 2 ? argv[1] : ""};
	int fd, i;
	for (i = 0; i < 3; i++)
		if (argc == 2 && !strcmp(argv[1], scenarios[i].name))
			break;
	if (i == 3) {
		fprintf(stderr,
			"usage: %s <reclaim|compaction|unmatched-end>\n",
			argv[0]);
		return 2;
	}
	request.scenario = scenarios[i].scenario;
	fd = open("/dev/memory_free_compact_mock", O_RDONLY);
	if (fd < 0) {
		perror("open compact mock");
		return 1;
	}
	i = mock_task_trigger(fd, MEMORY_FREE_COMPACT_RUN, &request, &options);
	close(fd);
	return i;
}
