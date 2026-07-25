#!/bin/sh
#
# Copyright 2025, 2026 The HuaTuo Authors
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

ELASTICSEARCH_HOST=${ELASTICSEARCH_HOST:-localhost}
ELASTIC_PASSWORD=${ELASTIC_PASSWORD:-huatuo-bamai}
RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}
CONFIG_FILE=${CONFIG_FILE:-${RUN_PATH}/conf/huatuo-bamai.conf}
HUATUO_MODE=${HUATUO_MODE:-profiling}
PYROSCOPE_ADDRESS=${PYROSCOPE_ADDRESS:-http://127.0.0.1:4040}
FOLDED_STACKS_DIR=${FOLDED_STACKS_DIR:-/var/lib/huatuo/autotracing-folded}
ELASTICSEARCH_WAIT_SECONDS=${ELASTICSEARCH_WAIT_SECONDS:-180}
ELASTICSEARCH_INIT_DELAY_SECONDS=${ELASTICSEARCH_INIT_DELAY_SECONDS:-5}

profile_enabled() {
	case ",${HUATUO_MODE}," in
	*,"$1",*) return 0 ;;
	*) return 1 ;;
	esac
}

prepare_runtime_config() {
	mode=$1
	if [ ! -f "$CONFIG_FILE" ]; then
		echo "huatuo-bamai config not found: $CONFIG_FILE" >&2
		return 1
	fi

	case "$PYROSCOPE_ADDRESS" in
	http://* | https://*) ;;
	*)
		echo "PYROSCOPE_ADDRESS must use http or https." >&2
		return 1
		;;
	esac
	case "$PYROSCOPE_ADDRESS" in
	*\"* | *\\*)
		echo "PYROSCOPE_ADDRESS must not contain quotes or backslashes." >&2
		return 1
		;;
	esac
	case "$FOLDED_STACKS_DIR" in
	*\"* | *\\*)
		echo "FOLDED_STACKS_DIR must not contain quotes or backslashes." >&2
		return 1
		;;
	esac

	runtime_config="${RUN_PATH}/conf/huatuo-bamai-${mode}.conf"
	temp_config="${runtime_config}.tmp.$$"
	disable_elasticsearch=false
	display_backend=apiserver
	if [ "$mode" = "profiling" ]; then
		disable_elasticsearch=true
		display_backend=pyroscope
	fi
	if ! awk \
		-v disable_elasticsearch="$disable_elasticsearch" \
		-v display_backend="$display_backend" \
		-v folded_stacks_dir="$FOLDED_STACKS_DIR" \
		-v pyroscope_address="$PYROSCOPE_ADDRESS" '
		/^[[:space:]]*\[Storage\.ES\][[:space:]]*$/ {
			section = "es"
			print
			next
		}
		/^[[:space:]]*\[Storage\.Pyroscope\][[:space:]]*$/ {
			section = "pyroscope"
			seen_pyroscope = 1
			print
			next
		}
		/^[[:space:]]*\[AutoTracing\.Display\][[:space:]]*$/ {
			section = "display"
			seen_display = 1
			print
			next
		}
		/^[[:space:]]*\[[^]]+\][[:space:]]*$/ {
			section = ""
			print
			next
		}
		section == "es" &&
			/^[[:space:]]*Address[[:space:]]*=/ {
			seen_es_address = 1
			if (disable_elasticsearch == "true") {
				print "        Address = \"\""
			} else {
				print
			}
			next
		}
		section == "pyroscope" &&
			/^[[:space:]]*#?[[:space:]]*Address[[:space:]]*=/ {
			printf "        Address = \"%s\"\n", pyroscope_address
			seen_pyroscope_address = 1
			next
		}
		section == "display" &&
			/^[[:space:]]*#?[[:space:]]*Backend[[:space:]]*=/ {
			printf "        Backend = \"%s\"\n", display_backend
			seen_display_backend = 1
			next
		}
		section == "display" &&
			/^[[:space:]]*#?[[:space:]]*FoldedStacksDir[[:space:]]*=/ {
			printf "        FoldedStacksDir = \"%s\"\n", folded_stacks_dir
			seen_folded_stacks_dir = 1
			next
		}
		{ print }
		END {
			if (!seen_es_address ||
				!seen_pyroscope ||
				!seen_pyroscope_address ||
				!seen_display ||
				!seen_display_backend ||
				!seen_folded_stacks_dir) {
				exit 42
			}
		}
	' "$CONFIG_FILE" > "$temp_config"; then
		rm -f "$temp_config"
		echo "Storage or AutoTracing.Display is missing from $CONFIG_FILE." >&2
		return 1
	fi

	chmod 600 "$temp_config"
	mv "$temp_config" "$runtime_config"
	CONFIG_FILE=$runtime_config
	if [ "$mode" = "profiling" ]; then
		echo "Profiling mode: AutoTracing uses Grafana and Pyroscope."
	else
		echo "Full mode: AutoTracing uses huatuo-apiserver."
	fi
}

wait_for_elasticsearch() {
	target_url="http://${ELASTICSEARCH_HOST}:9200/"

	if [ -f "$CONFIG_FILE" ]; then
		conf_addr=$(
			sed -n '/\[Storage\.ES\]/,/\[.*\]/p' "$CONFIG_FILE" \
				| grep '^[[:space:]]*Address' \
				| head -n 1 \
				| awk -F'"' '{print $2}'
		)

		if [ -n "$conf_addr" ]; then
			echo "Found Elasticsearch address in config: $conf_addr"
			target_url="${conf_addr}/"
		fi
	fi

	result=1
	output=""
	sec=1
	while [ "$sec" -le "$ELASTICSEARCH_WAIT_SECONDS" ]; do
		exit_code=0
		if [ -n "$ELASTIC_PASSWORD" ]; then
			output=$(
				curl -sS -D- -m15 -w '%{http_code}' \
					-u "elastic:${ELASTIC_PASSWORD}" "$target_url"
			) || exit_code=$?
		else
			output=$(curl -sS -D- -m15 -w '%{http_code}' "$target_url") \
				|| exit_code=$?
		fi
		if [ "$exit_code" -ne 0 ]; then
			result=$exit_code
		fi

		http_code=$(printf '%s' "$output" | tail -c 3)
		if [ "$http_code" = "200" ]; then
			result=0
			break
		fi

		echo "Waiting for Elasticsearch ready... ${sec}s"
		sleep 1
		sec=$((sec + 1))
	done

	if [ "$result" -ne 0 ] && [ "$http_code" != "000" ]; then
		printf '%s' "$output" | head -c -3
	fi

	if [ "$result" -ne 0 ]; then
		case "$result" in
		6)
			echo 'Could not resolve host. Is Elasticsearch running?'
			;;
		7)
			echo 'Failed to connect to host. Is Elasticsearch healthy?'
			;;
		28)
			echo 'Timeout connecting to host. Is Elasticsearch healthy?'
			;;
		*)
			echo "Connection to Elasticsearch failed. Exit code: ${result}"
			;;
		esac

		return "$result"
	fi
}

if profile_enabled profiling; then
	if profile_enabled full; then
		echo "HUATUO_MODE must select either full or profiling, not both." >&2
		exit 1
	fi
	prepare_runtime_config profiling
elif profile_enabled full; then
	prepare_runtime_config full
	wait_for_elasticsearch
	sleep "$ELASTICSEARCH_INIT_DELAY_SECONDS"
	echo "Elasticsearch is ready."
else
	echo "HUATUO_MODE must select full or profiling." >&2
	exit 1
fi

cd "$RUN_PATH"
exec ./bin/huatuo-bamai \
	--region example \
	--config-dir "$(dirname "$CONFIG_FILE")" \
	--config "$(basename "$CONFIG_FILE")" \
	--disable-kubelet
