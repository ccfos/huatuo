#!/bin/sh

# Copyright 2026 The HuaTuo Authors
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

set -eu

fail() {
	echo "cpu-load: $*" >&2
	exit 1
}
[ "$#" -eq 3 ] && [ "$2" = --control ] || fail 'use load|host --control <command file>'
scope=$1
case "$scope" in load | host) ;; *) fail 'use load|host --control <command file>' ;; esac
control=$3
stress_ng=${STRESS_NG_BIN:-stress-ng}
"$stress_ng" --version
previous=
load_pid=

stop_load() {
	if [ -n "$load_pid" ]; then
		# Older stress-ng may leave CPU workers running when only its parent gets TERM.
		# Its parent can reap workers before all signals are delivered.
		kill -ALRM "$load_pid" $children 2> /dev/null || true
		wait "$load_pid"
		load_pid=
	fi
}
trap stop_load EXIT
trap 'exit 0' INT TERM

while :; do
	if [ -n "$load_pid" ] && ! kill -0 "$load_pid" 2> /dev/null; then
		wait "$load_pid" || exit "$?"
		fail 'stress-ng exited before the next command; inspect the worker log'
	fi
	IFS= read -r command < "$control"
	if [ "$command" != "$previous" ]; then
		read -r mode milli count generation << EOF
$command
EOF
		case "$mode" in idle | user | system | mixed | burst) ;; *) fail "unsupported mode: $mode" ;; esac
		case "$count" in 1 | 2 | 3) ;; *) fail "workers must be 1, 2 or 3: $count" ;; esac
		[ "$milli" -ge 0 ] && [ "$milli" -le 2000 ] || fail "millicpu must be between 0 and 2000: $milli"
		stop_load
		set -- --timeout 1h --abort --metrics-brief --temp-path /tmp --keep-name
		case "$mode" in
		user)
			percent=100
			[ "$milli" -eq 0 ] || percent=$((milli / 10 / count))
			slice=10
			# Older host versions miscalculate sleep with positive CPU slices.
			[ "$scope" != host ] || slice=-64
			set -- "$@" --cpu "$count" --cpu-method int64 --cpu-load "$percent" --cpu-load-slice "$slice"
			;;
		system | mixed)
			if [ "$scope" = host ]; then
				# Older distro packages lack --workload; /dev/null avoids disk I/O.
				[ "$milli" -eq 0 ] || [ "$milli" -eq $((1000 * count)) ] \
					|| fail 'host syscall workloads require one full CPU per worker'
				set -- "$@" --null "$count"
			else
				percent=100
				[ "$milli" -eq 0 ] || percent=$((milli / 10 / count))
				set -- "$@" --workload "$count" --workload-method getpid --workload-load "$percent" \
					--workload-threads 0 --workload-dist even --workload-quanta-us 10000 --workload-slice-us 100000
			fi
			;;
		burst)
			[ "$milli" -eq 0 ] || fail 'burst requires millicpu=0'
			set -- "$@" --syncload "$count" --syncload-msbusy 200 --syncload-mssleep 300
			;;
		esac
		if [ "$mode" != idle ]; then
			"$stress_ng" "$@" &
			load_pid=$!
			# The host reference must measure the worker, not its supervisor.
			children=
			attempt=0
			while [ -z "$children" ] && [ "$attempt" -lt 50 ]; do
				read -r children < "/proc/$load_pid/task/$load_pid/children" || true
				[ -n "$children" ] || sleep 0.1
				attempt=$((attempt + 1))
			done
			[ -n "$children" ] || fail 'stress-ng created no workers; inspect its diagnostics'
			printf 'workers %s %s\n' "$generation" "$children"
		fi
		previous=$command
		printf 'ready %s\n' "$generation"
	fi
	sleep 0.5
done
