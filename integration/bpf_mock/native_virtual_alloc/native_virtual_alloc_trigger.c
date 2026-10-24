// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include "../common/task.h"
#include "native_virtual_alloc_mock.h"
int main(int argc, char **argv)
{
	const struct {
		const char *name;
		enum native_virtual_alloc_scenario scenario;
	} scenarios[] = {
	    {"mixed", NATIVE_VIRTUAL_ALLOC_MIXED},
	    {"file-only", NATIVE_VIRTUAL_ALLOC_FILE_ONLY},
	};
	struct native_virtual_alloc_request request;
	unsigned int i;
	for (i = 0; i < sizeof(scenarios) / sizeof(scenarios[0]); i++)
		if (argc >= 2 && !strcmp(argv[1], scenarios[i].name))
			break;
	if (i == sizeof(scenarios) / sizeof(scenarios[0])) {
		fprintf(stderr, "unknown or missing memory scenario\n");
		return 2;
	}
	request.scenario = scenarios[i].scenario;
	return mock_profile_trigger(argc - 1, argv + 1,
				    "/dev/native_virtual_alloc_mock",
				    NATIVE_VIRTUAL_ALLOC_RUN, &request);
}
