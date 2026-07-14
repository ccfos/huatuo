#!/bin/sh
#
# Copyright 2025 The HuaTuo Authors
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

ELASTICSEARCH_HOST=${ELASTICSEARCH_HOST:-localhost}
ELASTIC_PASSWORD=${ELASTIC_PASSWORD:-huatuo-bamai}

RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}
CONFIG_FILE=${CONFIG_FILE:-${RUN_PATH}/conf/huatuo-bamai.conf}
HUATUO_MODE=${HUATUO_MODE:-full}
ELASTICSEARCH_ENABLED=false

is_profiling_mode() {
	case ",${HUATUO_MODE}," in
	*,profiling,*) return 0 ;;
	*) return 1 ;;
	esac
}

prepare_config() {
	if ! is_profiling_mode; then
		return 0
	fi
	if [ ! -f "$CONFIG_FILE" ]; then
		echo "Config file $CONFIG_FILE not found." >&2
		return 1
	fi

	profiling_config="/tmp/huatuo-bamai-profiling.conf"
	cp "$CONFIG_FILE" "$profiling_config"
	if grep -q '^[[:space:]]*\[Storage\.ES\]' "$profiling_config"; then
		sed -i '/\[Storage\.ES\]/,/^[[:space:]]*\[/ s/^[[:space:]]*#*[[:space:]]*Address[[:space:]]*=.*/        Address = ""/' "$profiling_config"
		if ! sed -n '/\[Storage\.ES\]/,/^[[:space:]]*\[/p' "$profiling_config" | grep -q '^[[:space:]]*Address'; then
			sed -i '/\[Storage\.ES\]/a\        Address = ""' "$profiling_config"
		fi
	else
		printf '\n[Storage.ES]\nAddress = ""\n' >>"$profiling_config"
	fi

	# Profiling-only deployments must also work on bare-metal hosts that do not
	# have kubelet client certificates. Remove configured values before adding
	# the disabled ports so the generated TOML never contains duplicate keys.
	if grep -q '^[[:space:]]*\[Pod\][[:space:]]*$' "$profiling_config"; then
		sed -i \
			-e '/^[[:space:]]*\[Pod\][[:space:]]*$/,/^[[:space:]]*\[/ { /^[[:space:]]*KubeletReadOnlyPort[[:space:]]*=/d; }' \
			-e '/^[[:space:]]*\[Pod\][[:space:]]*$/,/^[[:space:]]*\[/ { /^[[:space:]]*KubeletAuthorizedPort[[:space:]]*=/d; }' \
			"$profiling_config"
		sed -i '/^[[:space:]]*\[Pod\][[:space:]]*$/a\
        KubeletReadOnlyPort = 0\
        KubeletAuthorizedPort = 0' "$profiling_config"
	else
		printf '\n[Pod]\nKubeletReadOnlyPort = 0\nKubeletAuthorizedPort = 0\n' >>"$profiling_config"
	fi
	CONFIG_FILE=$profiling_config
	echo "Profiling mode: Elasticsearch storage and kubelet pod discovery are disabled."
}

# Wait for Elasticsearch to be ready
wait_for_elasticsearch() {
	target_url="http://${ELASTICSEARCH_HOST}:9200/"

	# Try to extract Elasticsearch address from config file
	if [ -f "$CONFIG_FILE" ]; then
		# Extract Address from [Storage.ES] section
		# sed: range from [Storage.ES] to next section start [
		# grep: find Address line
		# awk: extract text between double quotes
		conf_line=$(sed -n '/\[Storage\.ES\]/,/\[.*\]/p' "$CONFIG_FILE" | grep '^[[:space:]]*Address' | head -n 1)
		conf_addr=$(printf '%s\n' "$conf_line" | awk -F'"' '{print $2}')

		if [ -n "$conf_addr" ]; then
			ELASTICSEARCH_ENABLED=true
			echo "Found Elasticsearch address in config: $conf_addr"
			target_url="${conf_addr}/"
		elif [ -n "$conf_line" ]; then
			echo "Elasticsearch storage is disabled; skipping readiness check."
			return 0
		else
			ELASTICSEARCH_ENABLED=true
			echo "Using the default Elasticsearch address: $target_url"
		fi
	else
		echo "Config file $CONFIG_FILE not found; skipping Elasticsearch readiness check."
		return 0
	fi

	args="-s -D- -m15 -w '%{http_code}' ${target_url}"
	if [ -n "${ELASTIC_PASSWORD}" ]; then
		args="$args -u elastic:${ELASTIC_PASSWORD}"
	fi

	result=1
	output=""

	# retry for up to 180 seconds
	for sec in $(seq 1 180); do
		exit_code=0
		output=$(eval "curl $args") || exit_code=$?
		# echo "exec curl $args, exit code: $exit_code, output: $output"
		if [ $exit_code -ne 0 ]; then
			result=$exit_code
		fi

		# Extract the last three characters of the output to check the HTTP status code
		http_code=$(echo "$output" | tail -c 4)
		if [ "$http_code" -eq 200 ]; then
			result=0
			break
		fi

		echo "Waiting for Elasticsearch ready... ${sec}s"
		sleep 1
	done

	if [ $result -ne 0 ] && [ "$http_code" -ne 000 ]; then
		echo "$output" | head -c -3
	fi

	if [ $result -ne 0 ]; then
		case $result in
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

		exit $result
	fi
}

prepare_config || exit $?
wait_for_elasticsearch
if [ "$ELASTICSEARCH_ENABLED" = "true" ]; then
	sleep 5 # Waiting for initialization of Elasticsearch built-in users
	echo "Elasticsearch is ready."
fi

# Run huatuo-bamai
cd $RUN_PATH
exec ./bin/huatuo-bamai \
    --region example \
    --config-dir "$(dirname "$CONFIG_FILE")" \
    --config "$(basename "$CONFIG_FILE")" \
    --disable-kubelet
