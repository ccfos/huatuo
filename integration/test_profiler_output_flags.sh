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

# Verify output flags fail before either command starts collection.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

assert_output_flags_rejected() {
	local expected=$1
	shift
	local result="${HUATUO_BAMAI_TEST_TMPDIR}/output-flags.log"
	if "${output_flags_command[@]}" "$@" > "${result}" 2>&1; then
		fatal "expected output validation failure for ${output_flags_command[*]} $*"
	fi
	grep -qF -- "${expected}" "${result}" \
		|| fatal "expected '${expected}' for ${output_flags_command[*]} $*: $(cat "${result}")"
}

for output_flags_tool in profiler irqtracing; do
	output_flags_command=("${ROOT_DIR}/_output/bin/${output_flags_tool}")
	if [[ "${output_flags_tool}" == profiler ]]; then
		output_flags_command+=(--type cpu --language c --pid "$$")
	else
		output_flags_command+=(--bpf-path unused.o)
	fi
	assert_output_flags_rejected 'must both be specified'
	assert_output_flags_rejected 'must both be specified' --output-path profile
	assert_output_flags_rejected 'must both be specified' --output-format collapsed
	assert_output_flags_rejected 'must not be empty' --output-format collapsed --output-path ''
	assert_output_flags_rejected 'flag provided but not defined' --output-storage unused.sock
	assert_output_flags_rejected 'flag provided but not defined' --output collapsed
	assert_output_flags_rejected 'unsupported output format' --output-format pprof --output-path profile
	assert_output_flags_rejected 'unsupported output format' --output-format remote --output-path unused.sock --task-id task
done
