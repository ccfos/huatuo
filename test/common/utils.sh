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

set -euo pipefail

log_prefix() {
	echo "[${TEST_LOG_TAG}]"
}

log_info() {
	echo "$(log_prefix) $*"
}

log_warn() {
	echo "$(log_prefix)[WARN] $*" >&2
}

log_error() {
	echo "$(log_prefix)[ERROR] $*" >&2
}

fatal() {
	echo "$(log_prefix)[FAIL] $*" >&2
	exit 1
}

assert_eq() {
	local actual=$1 expect=$2 msg=${3:-""}
	if [[ "$actual" == "$expect" ]]; then
		return 0
	fi

	log_info "assert_eq: ${msg} actual=${actual}, expect=${expect}"
	return 1
}

# wait_until <timeout> <interval> <description> <function> [args...]
# Example:
# wait_until 10 1 "check ready" my_check_func "arg1" "arg2"
wait_until() {
	local timeout=$1 interval=$2 desc=$3
	shift 3
	local func=$1
	shift

	if ! declare -f "$func" >/dev/null 2>&1; then
		fatal "❌ wait_until expects function or command: \"$func\""
	fi

	local end=$(($(date +%s) + timeout))
	local attempt=0
	while (($(date +%s) < end)); do
		attempt=$((attempt + 1))
		log_info "wait attempt #${attempt}: ${desc}"
		if "$func" "$@"; then
			return 0
		fi
		sleep "$interval"
	done

	fatal "❌ timeout waiting for: ${desc} after ${timeout}s"
}
