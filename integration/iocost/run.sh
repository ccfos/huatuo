#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This file dispatches the explicit IOCOST OETest qualification modes.

set -euo pipefail

readonly IOCOST_SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly IOCOST_REPOSITORY_ROOT=$(cd "${IOCOST_SCRIPT_DIR}/../.." && pwd)
readonly IOCOST_MODES=(functional lifecycle faults pressure compat)

usage() {
	printf 'usage: TEST_IOCOST_REQUIRED=1 %s {functional|lifecycle|faults|pressure|compat|all}\n' "$0" >&2
}

[[ $# -eq 1 ]] || {
	usage
	exit 2
}
if [[ ${TEST_IOCOST_REQUIRED+x} != x || ${TEST_IOCOST_REQUIRED} != 1 ]]; then
	printf '[IOCOST OETEST][FAIL] TEST_IOCOST_REQUIRED must be set to exactly 1\n' >&2
	exit 1
fi

mode=$1
declare -a selected_modes=()
if [[ ${mode} == all ]]; then
	selected_modes=("${IOCOST_MODES[@]}")
else
	known=0
	for candidate in "${IOCOST_MODES[@]}"; do
		if [[ ${candidate} == "${mode}" ]]; then
			known=1
			break
		fi
	done
	((known == 1)) || {
		usage
		exit 2
	}
	selected_modes=("${mode}")
fi

# shellcheck source=integration/iocost/lib.sh
source "${IOCOST_SCRIPT_DIR}/lib.sh"
[[ -f ${IOCOST_SCRIPT_DIR}/cases.sh && ! -L ${IOCOST_SCRIPT_DIR}/cases.sh ]] || {
	printf '[IOCOST OETEST][FAIL] qualification cases are unavailable\n' >&2
	exit 1
}
# shellcheck source=integration/iocost/cases.sh
source "${IOCOST_SCRIPT_DIR}/cases.sh"

# Resolve the complete request before acquiring the lock or creating fixtures.
for selected in "${selected_modes[@]}"; do
	case_function=iocost_run_${selected}
	declare -F "${case_function}" > /dev/null \
		|| iocost_die "qualification mode is not implemented: ${selected}"
done

iocost_install_traps
iocost_runner_init "${IOCOST_REPOSITORY_ROOT}"

for selected in "${selected_modes[@]}"; do
	iocost_log "running mode: ${selected}"
	"iocost_run_${selected}"
done

iocost_log "requested IOCOST qualification modes completed successfully"
