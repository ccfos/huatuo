#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Call directly in one shell. The caller owns its EXIT trap and target process.
# Sourcing this library neither builds nor changes shell options.
if [[ ${__HUATUO_RESOURCES_LOADED:-} == 1 ]]; then
	return 0
fi
__HUATUO_RESOURCES_LOADED=1

_resources_error() {
	printf 'resources: %s\n' "$*" >&2
	return 1
}

# resource_build: call before starting a short-lived target, not inside capture.
resource_build() {
	[[ $# == 0 && ${BASH_SUBSHELL} == 0 && -z ${_resources_pid:-} ]] \
		|| {
			_resources_error 'build directly with no active capture'
			return 1
		}
	_resources_binary=''
	command -v go > /dev/null || {
		_resources_error 'go command is unavailable'
		return 1
	}
	local workspace binary build_log
	workspace=$(realpath -e -- "${HUATUO_BAMAI_TEST_TMPDIR:?}") || return 1
	[[ ${workspace} != / ]] || {
		_resources_error 'invalid test workspace'
		return 1
	}
	binary="${workspace}/resource-sampler"
	build_log="${workspace}/resource-sampler-build.log"
	if ! (cd "${ROOT_DIR:?}" && go build -mod=vendor -o "${binary}" ./integration/testdata/resource_sampler) \
		> "${build_log}" 2>&1; then
		_resources_error "build failed; see ${build_log}"
		return 1
	fi
	_resources_binary=${binary}
}

# Match only running or stopped jobs belonging to the creating shell.
_resources_owned_running() {
	[[ ${_resources_owner:-} == "${BASHPID}" && -n ${_resources_pid:-} ]] || return 1
	local job_pid
	while read -r job_pid; do
		[[ ${job_pid} != "${_resources_pid}" ]] || return 0
	done < <(
		jobs -pr
		jobs -ps
	)
	return 1
}

# A timed-out stop remains a failure even if the subsequent TERM is successful.
_resources_reap() {
	local attempt status=0 timed_out=false
	for ((attempt = 0; attempt < 100; attempt++)); do
		_resources_owned_running || break
		sleep 0.05
	done
	if _resources_owned_running; then
		timed_out=true
		kill -TERM "${_resources_pid}" 2> /dev/null || true
		sleep 0.1
		if _resources_owned_running; then
			kill -KILL "${_resources_pid}" 2> /dev/null || true
		fi
	fi
	wait "${_resources_pid}" || status=$?
	_resources_pid=''
	_resources_directory=''
	_resources_owner=''
	if ${timed_out}; then
		_resources_error 'sampler stop timed out'
		return 1
	fi
	return "${status}"
}

# resource_start PID NEW_ABSOLUTE_DIRECTORY: waits for the first valid sample.
resource_start() {
	[[ $# == 2 && ${BASH_SUBSHELL} == 0 && -z ${_resources_pid:-} ]] \
		|| {
			_resources_error 'start directly with PID and a new directory, without an active capture'
			return 1
		}
	[[ -n ${_resources_binary:-} && -x ${_resources_binary} ]] \
		|| {
			_resources_error 'call resource_build before starting the target'
			return 1
		}
	local workspace directory attempt
	workspace=$(realpath -e -- "${HUATUO_BAMAI_TEST_TMPDIR:?}") || return 1
	directory=$(realpath -m -- "$2") || return 1
	[[ $2 == /* && ${workspace} != / && ${directory} == "${workspace}/"* && ! -L $2 ]] \
		|| {
			_resources_error 'output must be a new directory beneath the test workspace'
			return 1
		}
	mkdir -m 700 -- "${directory}" || return 1
	"${_resources_binary}" --pid "$1" --output-dir "${directory}" \
		< /dev/null > "${directory}/sampler.log" 2>&1 &
	_resources_pid=$!
	_resources_owner=${BASHPID}
	# Use the caller's absolute path as the handle, even if it is later replaced.
	_resources_directory=$2
	for ((attempt = 0; attempt < 100; attempt++)); do
		_resources_owned_running || break
		[[ ! -f ${directory}/ready.json ]] || return 0
		sleep 0.05
	done
	resource_cleanup "$2"
	_resources_error "capture failed to start; see ${directory}/sampler.log"
}

# resource_stop DIRECTORY: Go owns all resource calculations and quality checks.
resource_stop() {
	[[ $# == 1 && ${_resources_owner:-} == "${BASHPID}" ]] \
		|| {
			_resources_error 'capture is not owned by this shell'
			return 1
		}
	local directory status=0
	directory=$1
	[[ ${directory} == "${_resources_directory:-}" ]] \
		|| {
			_resources_error 'capture directory does not match'
			return 1
		}
	if _resources_owned_running; then
		kill -USR1 "${_resources_pid}" 2> /dev/null || status=1
	fi
	_resources_reap || status=1
	if ((status != 0)) || [[ ! -s ${directory}/result.json ]]; then
		_resources_error "capture failed; see ${directory}/result.json and sampler.log"
		return 1
	fi
}

# resource_cleanup DIRECTORY: abort only this sampler; preserve evidence/target.
resource_cleanup() {
	[[ $# == 1 && ${_resources_owner:-} == "${BASHPID}" && -n ${_resources_pid:-} ]] || return 0
	[[ $1 == "${_resources_directory:-}" ]] || return 0
	if _resources_owned_running; then
		kill -TERM "${_resources_pid}" 2> /dev/null || true
	fi
	_resources_reap || true
}
