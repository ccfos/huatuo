#!/usr/bin/env bash

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

# Shared adapters for the upstream reference collector; assertions stay in each case.
# Pin cAdvisor 0.55.1 to keep its API and rate calculation consistent across VMs.
CPU_REFERENCE_IMAGE=${CPU_REFERENCE_IMAGE:-ghcr.io/google/cadvisor@sha256:e02855207ec07bb45ee41f720645945251f093f24c28f8c29cf718490f784922}
cpu_reference_container=

cpu_reference_start() {
	require_commands docker curl jq pidstat
	CPU_REFERENCE_DIR="$1/reference"
	mkdir -p "${CPU_REFERENCE_DIR}"
	pidstat -V > "${CPU_REFERENCE_DIR}/sysstat-version.txt"
	docker image inspect "${CPU_REFERENCE_IMAGE}" > /dev/null 2>&1 \
		|| timeout 300 docker pull "${CPU_REFERENCE_IMAGE}"
	local port
	port=$(allocate_available_port) || fatal "cannot allocate cAdvisor reference port"
	CPU_REFERENCE_URL="http://127.0.0.1:${port}"
	cpu_reference_container="huatuo-cpu-reference-${BASHPID}-${RANDOM}"
	docker run -d --name "${cpu_reference_container}" --privileged --pid host --network host \
		-v /:/rootfs:ro -v /sys:/sys:ro -v /var/run:/var/run:ro \
		"${CPU_REFERENCE_IMAGE}" --listen_ip=127.0.0.1 --port="${port}" \
		--housekeeping_interval=1s --max_housekeeping_interval=1s --allow_dynamic_housekeeping=false \
		--enable_metrics=cpu,cpuset --docker_only=true \
		--raw_cgroup_prefix_whitelist=/kubepods,/kubepods.slice \
		> "${CPU_REFERENCE_DIR}/container-id"
	local deadline=$((SECONDS + 30))
	while ((SECONDS < deadline)); do
		if [[ $(docker inspect --format '{{.State.Running}}' "${cpu_reference_container}") != true ]]; then
			docker logs "${cpu_reference_container}" > "${CPU_REFERENCE_DIR}/cadvisor.log" 2>&1
			cat "${CPU_REFERENCE_DIR}/cadvisor.log" >&2
			fatal "cAdvisor exited before readiness; inspect ${CPU_REFERENCE_DIR}/cadvisor.log"
		fi
		if cpu_reference_ready; then return 0; fi
		sleep 1
	done
	fatal "cAdvisor did not become ready; inspect ${CPU_REFERENCE_DIR}"
}

cpu_reference_ready() {
	curl --noproxy '*' -fsS --max-time 3 "${CPU_REFERENCE_URL}/healthz" > /dev/null 2> "${CPU_REFERENCE_DIR}/ready.err"
}

cpu_reference_stop() {
	[[ -n ${cpu_reference_container:-} ]] || return 0
	local status=0
	docker logs "${cpu_reference_container}" > "${CPU_REFERENCE_DIR}/cadvisor.log" 2>&1 || status=1
	docker stats --no-stream "${cpu_reference_container}" > "${CPU_REFERENCE_DIR}/resources.txt" || true
	docker rm -f "${cpu_reference_container}" > /dev/null || status=1
	cpu_reference_container=
	return "${status}"
}

cpu_reference_name() {
	local path=$1 mountpoint
	mountpoint=$(findmnt -n -o TARGET -T "${path}") || return 1
	[[ ${path} != "${mountpoint}" ]] || {
		echo /
		return
	}
	printf '%s\n' "${path#"${mountpoint}"}"
}

cpu_reference_stats() {
	local path=$1 output=$2 name attempt
	name=$(cpu_reference_name "${path}") || return 1
	for ((attempt = 0; attempt < 30; attempt++)); do
		if curl --noproxy '*' -fsS --max-time 5 \
			"${CPU_REFERENCE_URL}/api/v2.0/stats${name}?type=name&recursive=false&count=64&max_age=0s" \
			-o "${output}.response" 2> "${output}.err" \
			&& jq -e --arg name "${name}" '.[$name] | select(length >= 2) | sort_by(.timestamp)' \
				"${output}.response" > "${output}"; then
			return 0
		fi
		sleep 1
	done
	echo "cAdvisor has no CPU samples for ${name}; inspect ${output}.response and ${output}.err" >&2
	return 1
}

# Timestamp conversion preserves the fractional part used by subsecond samples.
CPU_REFERENCE_JQ='
def epoch:
 split(".") as $p | if ($p|length)==1 then fromdateiso8601
 else ($p[0]+"Z"|fromdateiso8601) + ("0."+($p[1]|rtrimstr("Z"))|tonumber) end;
def average($start; $window_end; $cores):
 [range(1; length) as $i | .[$i] as $s | (.[$i-1].timestamp|epoch) as $a |
  ($s.timestamp|epoch) as $b | ([([$b,$window_end]|min)-([$a,$start]|max),0]|max) as $dt |
  select($dt > 0) | if $s.cpu_inst.usage == null then error("missing cAdvisor cpu_inst") else
  {dt: $dt, User: ($s.cpu_inst.usage.user / 1e7 / $cores),
   Sys: ($s.cpu_inst.usage.system / 1e7 / $cores), Total: ($s.cpu_inst.usage.total / 1e7 / $cores)} end] as $rows |
 ($rows|map(.dt)|add // 0) as $seconds |
 if $seconds < ($window_end-$start)*0.99 then error("cAdvisor samples do not cover the measurement window")
 else reduce $rows[] as $r ({User:0,Sys:0,Total:0};
  .User += $r.User*$r.dt/$seconds | .Sys += $r.Sys*$r.dt/$seconds | .Total += $r.Total*$r.dt/$seconds) end;
'

cpu_reference_capacity() {
	local target=$1 cpus quota=-1 period path root online count
	online=$(getconf _NPROCESSORS_ONLN)
	if [[ $(jq -r '.Pod // ""' <<< "${target}") == "" ]]; then
		jq -n --argjson online "${online}" '{Cores:$online,QuotaMilli:-1}'
		return
	fi
	path=$(jq -r '.CPUPath' <<< "${target}")
	root=$(jq -r '.CpusetPath' <<< "${target}")
	if [[ -f ${path}/cpu.max ]]; then
		read -r quota period < "${path}/cpu.max"
		[[ ${quota} != max ]] || quota=-1
		cpus=$(< "${root}/cpuset.cpus.effective")
	else
		quota=$(< "${path}/cpu.cfs_quota_us")
		period=$(< "${path}/cpu.cfs_period_us")
		cpus=$(< "${root}/cpuset.cpus")
	fi
	count=$(awk -v list="${cpus}" 'BEGIN { n=split(list,a,","); for(i=1;i<=n;i++) {
	 m=split(a[i],b,"-"); if(a[i]!="") count+=(m==2 ? b[2]-b[1]+1 : 1) } print count+0 }')
	jq -n --argjson online "${online}" --argjson count "${count}" \
		--argjson quota "${quota}" --argjson period "${period}" '
	 (if $quota < 0 then -1 else $quota/$period*1000 end) as $milli |
	 {QuotaMilli:$milli,Cores:([ (if $count==0 then $online else $count end),
	  (if $milli<0 then $online else $milli/1000 end)]|min)} | select(.Cores>0)'
}

cpu_reference_pidstat() {
	local pids=$1 duration=$2 output=$3
	LC_ALL=C S_TIME_FORMAT=ISO pidstat -u -p "${pids}" 1 "${duration}" > "${output}"
	awk '
	 /%usr/ && /UID/ { for(i=1;i<=NF;i++) col[$i]=i; next }
	 /^Average:/ && NF>3 { if (!col["%CPU"]) exit 1; user+=$(col["%usr"]); sys+=$(col["%system"]); cpu+=$(col["%CPU"]); found++ }
	 END { if(!found) exit 1; printf "{\"User\":%.6f,\"Sys\":%.6f,\"Total\":%.6f}\n",user,sys,cpu }
	' "${output}" > "${output}.json"
}

cpu_reference_sample() {
	local path=$1 cores=$2 duration=$3 output=$4 start end
	cpu_reference_stats "${path}" "${output}.before"
	start=$(date +%s.%N)
	sleep "${duration}"
	end=$(date +%s.%N)
	cpu_reference_stats "${path}" "${output}.after"
	jq --argjson start "${start}" --argjson window_end "${end}" --argjson cores "${cores}" \
		"${CPU_REFERENCE_JQ}"'
	 average($start;$window_end;$cores) as $p |
	 {at:$window_end,seconds:($window_end-$start),cores:$cores,percent:{user:$p.User,system:$p.Sys,total:$p.Total}}' \
		"${output}.after" > "${output}"
}

cpu_util_snapshot() {
	local targets=$1 output=$2 target i=0
	mkdir -p "${output}"
	cpu_metrics_read "${output}/metrics.json"
	date +%s.%N > "${output}/at"
	while read -r target; do
		cpu_reference_capacity "${target}" > "${output}/capacity-${i}.json"
		i=$((i + 1))
	done < <(jq -c '.[]' "${targets}")
	jq -n --slurpfile targets "${targets}" --slurpfile f "${output}/metrics.json" \
		--argjson at "$(< "${output}/at")" --slurpfile capacities <(jq -s '.' "${output}"/capacity-*.json) '
	 {At:$at,Families:$f[0],Cores:($capacities[0]|map(.Cores)),QuotaMilli:($capacities[0]|map(.QuotaMilli)),
	 Metrics:[$targets[0][] | . as $t |
	  ("huatuo_bamai_cpu_util_"+(if (.Pod//"")=="" then "" else "container_" end)) as $prefix |
	  ["total","usr","sys"] | map(. as $field | {key:({total:"Total",usr:"User",sys:"Sys"}[$field]),
	   value: ([$f[0][$prefix+$field].samples[]? | select(($t.Pod//"")=="" or
	    (.labels.container_host==$t.Pod and .labels.container_name==$t.Container and .labels.container_hostnamespace==$t.Namespace))][0].value // 0)}) | from_entries]}' \
		> "${output}/sample.json"
}

cpu_util_collect() {
	local targets=$1 output=$2 i target path start end pids pidstat_pid=
	mkdir -p "${output}"
	for i in 0 1; do
		cpu_metrics_read "${output}/warmup-${i}.json"
		sleep 2
	done
	# Prime discovery and history before the first bamai sample.
	while read -r path; do cpu_reference_stats "${path}" "${output}/prime.json"; done < <(jq -r '.[].UsagePath' "${targets}")
	cpu_util_snapshot "${targets}" "${output}/00"
	pids=$(jq -r '[.[].WorkerPIDs[]?] | unique | map(tostring) | join(",")' "${targets}")
	if [[ -n ${pids} ]]; then
		cpu_reference_pidstat "${pids}" 10 "${output}/pidstat.txt" &
		pidstat_pid=$!
	fi
	for i in 1 2 3 4 5; do
		sleep 2
		cpu_util_snapshot "${targets}" "${output}/0${i}"
	done
	[[ -z ${pidstat_pid} ]] || wait "${pidstat_pid}"
	jq -n --slurpfile targets "${targets}" --slurpfile samples <(jq -s '.' "${output}"/0*/sample.json) \
		'{Targets:$targets[0],Samples:$samples[0]}' > "${output}/samples.json"
	start=$(jq '.Samples[0].At' "${output}/samples.json")
	end=$(jq '.Samples[-1].At' "${output}/samples.json")
	i=0
	while read -r target; do
		path=$(jq -r '.UsagePath' <<< "${target}")
		cpu_reference_stats "${path}" "${output}/reference-${i}.json"
		jq --argjson start "${start}" --argjson window_end "${end}" \
			--argjson cores "$(jq ".Samples[-1].Cores[${i}]" "${output}/samples.json")" \
			"${CPU_REFERENCE_JQ}"'average($start;$window_end;$cores)' "${output}/reference-${i}.json" > "${output}/average-${i}.json"
		i=$((i + 1))
	done < <(jq -c '.[]' "${targets}")
	[[ -f ${output}/pidstat.txt.json ]] || echo '{"Total":0,"User":0,"Sys":0}' > "${output}/pidstat.txt.json"
	jq --slurpfile refs <(jq -s '.' "${output}"/average-*.json) --slurpfile worker "${output}/pidstat.txt.json" '
	 . as $r | .Samples as $s | ($s[-1].At-$s[0].At) as $seconds |
	 [range(0; .Targets|length) as $i | $s[-1].Cores[$i] as $cores |
	 {Target:$r.Targets[$i],Cores:$cores,Seconds:$seconds,Reference:$refs[0][$i],
	  Worker:(if ($r.Targets[$i].WorkerPIDs|length)>0 then $worker[0] else {User:0,Sys:0,Total:0} end),
	  Metrics:(reduce range(1;$s|length) as $j ({User:0,Sys:0,Total:0};
	   .User += $s[$j].Metrics[$i].User*($s[$j].At-$s[$j-1].At)/$seconds |
	   .Sys += $s[$j].Metrics[$i].Sys*($s[$j].At-$s[$j-1].At)/$seconds |
	   .Total += $s[$j].Metrics[$i].Total*($s[$j].At-$s[$j-1].At)/$seconds))} |
	  .WorkerIncrement=(.Worker|with_entries(.value/=$cores))]' "${output}/samples.json" > "${output}/result.json"
}

cpu_reference_concurrent() {
	local kind=$1 targets=$2 output=$3 rounds=$4 workers=() worker round options='{}'
	if [[ ${kind} == stat ]]; then
		options=$(jq -n --argjson wait "${cpu_stat_wait}" '{Wait:$wait,CounterMode:"unthrottled"}')
	fi
	trap 'for worker in "${workers[@]}"; do kill -TERM "${worker}" 2>/dev/null || true; done; wait || true; exit 143' TERM
	trap 'for worker in "${workers[@]}"; do kill -TERM "${worker}" 2>/dev/null || true; done; wait || true; exit 130' INT
	mkdir -p "${output}"
	for worker in 0 1 2 3; do
		(
			for ((round = 0; round < rounds; round++)); do
				"cpu_${kind}_snapshot" "${targets}" "${output}/${worker}-${round}"
				sleep 0.5
			done
		) &
		workers+=("$!")
	done
	local status=0
	for worker in "${workers[@]}"; do wait "${worker}" || status=1; done
	workers=()
	trap - TERM INT
	[[ ${status} == 0 ]] || return 1
	jq -n --slurpfile targets "${targets}" --slurpfile samples <(jq -s '.' "${output}"/*/sample.json) \
		--argjson options "${options}" \
		'{Targets:$targets[0],Samples:($samples[0]|sort_by(.At)),Options:($options//{})}'
}

cpu_stat_reference() {
	local targets=$1 output=$2 target i=0 path
	"${CPU_WAIT_BIN}" sample --targets "${targets}" > "${output}.wait"
	while read -r target; do
		path=$(jq -r '.CPUPath' <<< "${target}")
		cpu_reference_stats "${path}" "${output}.${i}.cadvisor"
		if [[ -f ${path}/cpu.max ]]; then
			# cAdvisor reports zero even when the kernel does not export a counter.
			jq -e --rawfile native "${path}/cpu.stat" '
			 ($native | split("\n") | map(split(" ")[0])) as $keys | .[-1].cpu.cfs |
			 {nr_throttled:.throttled_periods,throttled_usec:.throttled_time,
			  nr_bursts:.bursts_periods,burst_usec:.burst_time} |
			 with_entries(select(.key as $key | $keys | index($key))) |
			 with_entries(.key = ({throttled_usec:"throttled_time",burst_usec:"burst_time"}[.key] // .key))
			' "${output}.${i}.cadvisor" > "${output}.${i}.fields"
		else
			# cAdvisor's v1 reader omits burst counters. Supplement only these
			# fields from one cpu.stat read; throttle counters still use cAdvisor.
			jq -e --rawfile native "${path}/cpu.stat" '
			 .[-1].cpu.cfs | {nr_throttled:.throttled_periods,throttled_time:.throttled_time} +
			 ($native | split("\n") |
			  map(capture("^(?<key>nr_bursts|burst_time)\\s+(?<value>[0-9]+)$") |
			      .value |= tonumber) | from_entries)
			' "${output}.${i}.cadvisor" > "${output}.${i}.fields"
		fi
		jq --argjson i "${i}" --slurpfile wait "${output}.wait" \
			'. as $fields | $wait[0][$i] | .Fields += $fields' "${output}.${i}.fields" > "${output}.${i}.json"
		i=$((i + 1))
	done < <(jq -c '.[]' "${targets}")
	jq -s '.' "${output}".*.json > "${output}"
}

cpu_stat_snapshot() {
	local targets=$1 output=$2
	mkdir -p "${output}"
	cpu_stat_reference "${targets}" "${output}/before.json"
	cpu_metrics_read "${output}/metrics.json"
	cpu_stat_reference "${targets}" "${output}/after.json"
	jq -n --slurpfile targets "${targets}" --slurpfile f "${output}/metrics.json" \
		--slurpfile before "${output}/before.json" --slurpfile after "${output}/after.json" \
		--argjson at "$(date +%s.%N)" '
	 {At:$at,Before:$before[0],Raw:$after[0],Families:$f[0],
	 Metrics:[$targets[0][] | . as $t |
	 ["nr_throttled","throttled_time","nr_bursts","burst_time","wait_sum_percent"] |
	 map(. as $name | {key:$name,value:([$f[0]["huatuo_bamai_cpu_stat_container_"+$name].samples[]? |
	  select(.labels.container_host==$t.Pod and .labels.container_name==$t.Container and .labels.container_hostnamespace==$t.Namespace)][0].value)}) |
	 map(select(.value!=null)) | from_entries]}' > "${output}/sample.json"
}

cpu_stat_collect() {
	local targets=$1 output=$2 mode=unthrottled counter_only=false worker=0 i count=6 warmups=2 options
	shift 2
	while (($#)); do
		case $1 in
		--counter-mode)
			mode=$2
			shift 2
			;;
		--counter-only)
			counter_only=true
			shift
			;;
		--worker-pid)
			worker=$2
			shift 2
			;;
		*) fatal "unsupported cpu_stat sampling option: $1" ;;
		esac
	done
	options=$(jq -n --argjson wait "${cpu_stat_wait}" --arg mode "${mode}" \
		--argjson only "${counter_only}" --argjson worker "${worker}" \
		'{Wait:$wait,CounterMode:$mode,CounterOnly:$only,WorkerPID:$worker}')
	mkdir -p "${output}"
	[[ ${mode} != settled ]] || {
		count=2
		warmups=1
	}
	for ((i = 0; i < warmups; i++)); do
		cpu_metrics_read "${output}/warmup-${i}.json"
		sleep 2
	done
	local pidstat_pid=
	cpu_stat_snapshot "${targets}" "${output}/00"
	if ((worker > 0)); then
		cpu_reference_pidstat "${worker}" 10 "${output}/pidstat.txt" &
		pidstat_pid=$!
	fi
	for ((i = 1; i < count; i++)); do
		sleep 2
		cpu_stat_snapshot "${targets}" "${output}/0${i}"
	done
	[[ -z ${pidstat_pid} ]] || wait "${pidstat_pid}"
	jq -n --slurpfile targets "${targets}" --slurpfile samples <(jq -s '.' "${output}"/0*/sample.json) \
		--argjson options "${options}" \
		'{Targets:$targets[0],Samples:$samples[0],Options:($options//{})}' > "${output}/samples.json"
	local start end target index=0
	start=$(jq '.Samples[0].At' "${output}/samples.json")
	end=$(jq '.Samples[-1].At' "${output}/samples.json")
	while read -r target; do
		cpu_reference_stats "$(jq -r '.UsagePath' <<< "${target}")" "${output}/reference-${index}.json"
		jq --argjson start "${start}" --argjson window_end "${end}" \
			"${CPU_REFERENCE_JQ}"'average($start;$window_end;1).Total' "${output}/reference-${index}.json" > "${output}/cpu-${index}.json"
		index=$((index + 1))
	done < <(jq -c '.[]' "${targets}")
	"${CPU_WAIT_BIN}" percent --before "${output}/00/after.json" --after "${output}/0$((count - 1))/after.json" > "${output}/wait.json"
	[[ -f ${output}/pidstat.txt.json ]] || echo '{"Total":0}' > "${output}/pidstat.txt.json"
	jq --slurpfile cpu <(jq -s '.' "${output}"/cpu-*.json) --slurpfile waits "${output}/wait.json" \
		--slurpfile worker "${output}/pidstat.txt.json" '
	 . as $r | .Samples as $s | ($s[-1].At-$s[0].At) as $seconds |
	 {Options:.Options,WorkerCPU:$worker[0].Total,Windows:[range(0;.Targets|length) as $i |
	  {Target:$r.Targets[$i],Seconds:$seconds,CPU:$cpu[0][$i],WaitReference:$waits[0][$i],
	   WaitMetric:(reduce range(1;$s|length) as $j (0;
	    .+($s[$j].Metrics[$i].wait_sum_percent//0)*($s[$j].At-$s[$j-1].At)/$seconds)),
	   MetricDeltas:($s[0].Metrics[$i] | del(.wait_sum_percent) | with_entries(.value=($s[-1].Metrics[$i][.key]-.value))),
	   Deltas:($s[0].Raw[$i].Fields | del(.wait_sum) | with_entries(.value=($s[-1].Raw[$i].Fields[.key]-.value))) }]}' \
		"${output}/samples.json" > "${output}/result.json"
}
