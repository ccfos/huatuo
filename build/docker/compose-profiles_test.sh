#!/bin/sh
#
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
#

set -eu

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
project_dir=$(CDPATH= cd -- "${script_dir}/../.." && pwd)

assert_services() {
	profile=$1
	expected=$2
	actual=$(
		COMPOSE_PROFILES=$profile docker compose \
			--project-directory "$script_dir" \
			config --services \
			| sort \
			| tr '\n' ' ' \
			| sed 's/[[:space:]]*$//'
	)
	if [ "$actual" != "$expected" ]; then
		echo "$profile services: got '$actual', want '$expected'." >&2
		return 1
	fi
}

assert_services profiling "grafana huatuo-bamai pyroscope"
assert_services full \
	"elasticsearch grafana huatuo-apiserver huatuo-bamai prometheus pyroscope"
default_services=$(
	env -u COMPOSE_PROFILES docker compose \
		--project-directory "$script_dir" \
		config --services \
		| sort \
		| tr '\n' ' ' \
		| sed 's/[[:space:]]*$//'
)
if [ "$default_services" != "grafana huatuo-bamai pyroscope" ]; then
	echo "default services: got '$default_services', want profiling stack." >&2
	exit 1
fi

profiling_compose=$(
	COMPOSE_PROFILES=profiling docker compose \
		--project-directory "$script_dir" config
)
printf '%s\n' "$profiling_compose" \
	| grep -q 'HUATUO_MODE: profiling'

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/run/bin" "$test_dir/run/conf"
cp "$project_dir/huatuo-bamai.conf" "$test_dir/source.conf"

printf '%s\n' \
	'#!/bin/sh' \
	'config_dir=' \
	'config_file=' \
	'while [ "$#" -gt 0 ]; do' \
	'	case "$1" in' \
	'	--config-dir) config_dir=$2; shift 2 ;;' \
	'	--config) config_file=$2; shift 2 ;;' \
	'	--disable-kubelet) disable_kubelet=true; shift ;;' \
	'	*) shift ;;' \
	'	esac' \
	'done' \
	'test "${disable_kubelet:-}" = true' \
	'cat "${config_dir}/${config_file}"' \
	> "$test_dir/run/bin/huatuo-bamai"
chmod +x "$test_dir/run/bin/huatuo-bamai"

mkdir -p "$test_dir/fake-bin"
printf '%s\n' \
	'#!/bin/sh' \
	': >"${CURL_MARKER:?}"' \
	'printf "HTTP/1.1 200 OK\\r\\n\\r\\n200"' \
	> "$test_dir/fake-bin/curl"
chmod +x "$test_dir/fake-bin/curl"

PATH="$test_dir/fake-bin:$PATH" \
	CURL_MARKER="$test_dir/curl-called" \
	RUN_PATH="$test_dir/run" \
	CONFIG_FILE="$test_dir/source.conf" \
	HUATUO_MODE=profiling \
	PYROSCOPE_ADDRESS=http://127.0.0.1:4040 \
	"$script_dir/run.sh" > "$test_dir/rendered.conf"

test ! -e "$test_dir/curl-called"
profiling_config="$test_dir/run/conf/huatuo-bamai-profiling.conf"
grep -q 'Address = ""' "$profiling_config"
grep -q 'Username = ""' "$profiling_config"
grep -q 'Password = ""' "$profiling_config"
grep -q 'Address = "http://127.0.0.1:4040"' "$profiling_config"
grep -q 'Backend = "pyroscope"' "$profiling_config"
grep -q 'FoldedStacksDir = "/var/lib/huatuo/autotracing-folded"' \
	"$profiling_config"
cmp "$project_dir/huatuo-bamai.conf" "$test_dir/source.conf"

printf '%s\n' \
	'package main' \
	'import (' \
	'	"fmt"' \
	'	"os"' \
	'	"huatuo-bamai/cmd/huatuo-bamai/config"' \
	')' \
	'func main() {' \
	'	if err := config.Load(os.Args[1]); err != nil {' \
	'		panic(fmt.Errorf("load rendered config: %w", err))' \
	'	}' \
	'}' \
	> "$test_dir/validate-config.go"
(
	cd "$project_dir"
	go run "$test_dir/validate-config.go" "$profiling_config"
)

PATH="$test_dir/fake-bin:$PATH" \
	CURL_MARKER="$test_dir/curl-called" \
	RUN_PATH="$test_dir/run" \
	CONFIG_FILE="$test_dir/source.conf" \
	HUATUO_MODE=full \
	PYROSCOPE_ADDRESS=http://127.0.0.1:4040 \
	ELASTICSEARCH_INIT_DELAY_SECONDS=0 \
	"$script_dir/run.sh" > "$test_dir/full.conf"

test -e "$test_dir/curl-called"
grep -q 'Address = "http://127.0.0.1:9200"' "$test_dir/full.conf"
grep -q 'Address = "http://127.0.0.1:4040"' "$test_dir/full.conf"
grep -q 'Backend = "apiserver"' "$test_dir/full.conf"
grep -q 'FoldedStacksDir = "/var/lib/huatuo/autotracing-folded"' \
	"$test_dir/full.conf"
cmp "$project_dir/huatuo-bamai.conf" "$test_dir/source.conf"

if RUN_PATH="$test_dir/run" \
	CONFIG_FILE="$test_dir/source.conf" \
	HUATUO_MODE=unknown \
	"$script_dir/run.sh" > /dev/null 2>&1; then
	echo "run.sh accepted an unknown HUATUO_MODE." >&2
	exit 1
fi
