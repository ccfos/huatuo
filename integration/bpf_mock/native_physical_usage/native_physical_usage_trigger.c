// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#include "../common/task.h"
#include "native_physical_usage_mock.h"
int main(int argc, char **argv)
{
	const struct {
		const char *name;
		enum native_physical_usage_scenario scenario;
	} scenarios[] = {
	    {"retained", NATIVE_PHYSICAL_USAGE_RETAINED},
	    {"balanced", NATIVE_PHYSICAL_USAGE_BALANCED},
	    {"partial", NATIVE_PHYSICAL_USAGE_PARTIAL},
	    {"untracked", NATIVE_PHYSICAL_USAGE_UNTRACKED},
	};
	struct native_physical_usage_request request;
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
				    "/dev/native_physical_usage_mock",
				    NATIVE_PHYSICAL_USAGE_RUN, &request);
}
