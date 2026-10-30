/*
	* Copyright 2026 The HuaTuo Authors
	*
	* Licensed under the Apache License, Version 2.0 (the "License");
	* you may not use this file except in compliance with the License.
	* You may obtain a copy of the License at
	*
	*     http://www.apache.org/licenses/LICENSE-2.0
	*
	* Unless required by applicable law or agreed to in writing, software
	* distributed under the License is distributed on an "AS IS" BASIS,
	* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
	* See the License for the specific language governing permissions and
	* limitations under the License.
	*/



#include <stdlib.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <unistd.h>

int main(int argc, char **argv)
{
	if (argc != 3 || prctl(PR_SET_NAME, argv[1], 0, 0, 0))
		return 1;
	int count = atoi(argv[2]);
	if (count < 1 || count > 32)
		return 1;
	for (int i = 0; i < count; i++)
		if (syscall(SYS_getpid) < 1)
		 return 1;
	return 0;
}
