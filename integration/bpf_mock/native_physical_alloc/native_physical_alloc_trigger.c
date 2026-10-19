// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include "../common/task.h"
#include "native_physical_alloc_mock.h"
int main(int argc, char **argv)
{
	const struct {
		const char *name;
		enum native_physical_alloc_scenario scenario;
	} scenarios[] = {
	    {"mixed", NATIVE_PHYSICAL_ALLOC_MIXED},
	};
	struct native_physical_alloc_request request;
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
				    "/dev/native_physical_alloc_mock",
				    NATIVE_PHYSICAL_ALLOC_RUN, &request);
}
