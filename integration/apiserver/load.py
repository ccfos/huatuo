#!/usr/bin/env python3
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
"""Bounded-memory query load and profiling soak against the suite-owned server."""
import argparse
import collections
import concurrent.futures
import csv
import http.client
import json
import math
import os
from pathlib import Path
import re
import signal
import threading
import time
from urllib.parse import urlsplit


def main():
    def interrupted_signal(signum, frame):
        raise KeyboardInterrupt(f"signal {signum}")

    signal.signal(signal.SIGTERM, interrupted_signal)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mode', choices=['load', 'soak'], required=True)
    parser.add_argument('--url', required=True)
    parser.add_argument('--job', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--seconds', type=int, required=True)
    parser.add_argument('--clients', type=int, default=100)
    parser.add_argument('--rps', type=int, default=100)
    parser.add_argument('--max-rss-growth-mib', type=float, default=256)
    parser.add_argument('--max-fd-growth', type=int, default=32)
    parser.add_argument('--max-goroutine-growth', type=int, default=64)
    args = parser.parse_args()
    if min(args.seconds, args.clients, args.rps) <= 0 or args.rps > 150 or args.clients > 1000:
        parser.error('seconds/clients/rps must be positive; rps must be <=150 and clients <=1000 (server limit: 200 RPS)')
    if min(args.max_rss_growth_mib, args.max_fd_growth, args.max_goroutine_growth) < 0 or not math.isfinite(args.max_rss_growth_mib):
        parser.error('resource growth limits must be finite and nonnegative')
    url = urlsplit(args.url)
    if url.scheme != 'http' or url.hostname not in ('127.0.0.1', 'localhost'):
        parser.error('only the local suite-owned HTTP server is supported')
    token = os.environ['API_TOKEN']
    stop = threading.Event()
    lock = threading.Lock()
    latencies = collections.Counter()
    statuses = collections.Counter()
    errors = []
    jobs = []
    traffic = collections.Counter()
    totals = collections.Counter()

    def request(method, path, body=None):
        connection = http.client.HTTPConnection(url.hostname, url.port, timeout=10)
        headers = {'Authorization': 'Bearer ' + token}
        if body is not None:
            headers['Content-Type'] = 'application/json'
            body = json.dumps(body)
        started = time.monotonic()
        try:
            connection.request(method, path, body=body, headers=headers)
            response = connection.getresponse()
            payload = response.read(16 * 1024 * 1024 + 1)
            if len(payload) > 16 * 1024 * 1024:
                raise ValueError('response exceeds load client 16 MiB limit')
            return response.status, payload, time.monotonic() - started
        finally:
            connection.close()

    def fail(error):
        with lock:
            if len(errors) < 20:
                errors.append(str(error))
        stop.set()

    def get_json(method, path, expected=200, body=None):
        status, data, _ = request(method, path, body)
        if status != expected:
            raise ValueError(f'{method} {path}: HTTP {status}: {data[:500]!r}')
        return json.loads(data)['data']

    def resources():
        status, payload, _ = request('GET', '/metrics')
        if status != 200:
            raise ValueError(f'/metrics: HTTP {status}')
        found = {}
        names = {
            'process_resident_memory_bytes': 'rss',
            'process_open_fds': 'fds',
            'go_goroutines': 'goroutines',
            'process_cpu_seconds_total': 'cpu_seconds',
        }
        for line in payload.decode().splitlines():
            match = re.match(r'^huatuo_apiserver_(\w+)(?:\{.*\})? ([^ ]+)$', line)
            if match and match[1] in names:
                value = float(match[2])
                if not math.isfinite(value) or value < 0:
                    raise ValueError(f'invalid metric: {line}')
                found[names[match[1]]] = value
        if len(found) != len(names):
            raise ValueError(f'resource metrics missing: {found}')
        return found

    # Warm the same routes before comparing process resources.
    for _ in range(10):
        get_json('GET', '/v1/profiling?limit=10&offset=0')
        get_json('GET', '/v1/profiling/' + args.job)
    baseline = resources()
    deadline = time.monotonic() + args.seconds
    started = time.monotonic()

    def query_worker(index):
        sequence = index
        next_request = started + index / args.rps
        while not stop.is_set() and time.monotonic() < deadline:
            if stop.wait(max(0, next_request - time.monotonic())):
                break
            if time.monotonic() >= deadline:
                break
            kind = 'list' if sequence % 10 < 7 else 'detail'
            path = '/v1/profiling?limit=10&offset=0' if kind == 'list' else '/v1/profiling/' + args.job
            try:
                status, payload, elapsed = request('GET', path)
                with lock:
                    statuses[status] += 1
                    traffic[kind] += 1
                    latencies[min(60000, round(elapsed * 1000))] += 1
                if status != 200:
                    raise ValueError(f'{path}: HTTP {status}: {payload[:500]!r}')
                data = json.loads(payload)['data']
                if kind == 'list':
                    if not isinstance(data['items'], list):
                        raise ValueError('list response has no items array')
                elif data['request_id'] != args.job:
                    raise ValueError('detail returned another job')
            except Exception as error:
                fail(error)
                break
            sequence += args.clients
            # Do not accumulate an unbounded backlog if the server is slow.
            next_request = max(next_request + args.clients / args.rps, time.monotonic())

    def sample_loop():
        with (args.output / 'resources.csv').open('w', newline='') as output:
            writer = csv.DictWriter(output, fieldnames=['elapsed', 'rss', 'fds', 'goroutines', 'cpu_seconds'])
            writer.writeheader()
            while not stop.is_set() and time.monotonic() < deadline:
                try:
                    writer.writerow({'elapsed': round(time.monotonic() - started, 3), **resources()})
                    output.flush()
                except Exception as error:
                    fail(error)
                stop.wait(min(10, max(0, deadline - time.monotonic())))

    def soak_loop():
        while not stop.is_set() and time.monotonic() < deadline:
            current = None
            try:
                data = get_json('POST', '/v1/profiling', 201, {
                    'hostname': '127.0.0.1', 'duration_seconds': 12,
                    'scope': 'host', 'type': 'cpu', 'language': 'c', 'mode': 'oncpu',
                })
                current = data['request_id']
                jobs.append(current)
                expiry = time.monotonic() + 90
                while time.monotonic() < expiry and not stop.is_set():
                    data = get_json('GET', '/v1/profiling/' + current)
                    if data['status'] == 'terminal':
                        if data['terminal']['outcome'] != 'completed':
                            raise ValueError(f'soak job failed: {data}')
                        raw = get_json('GET', f'/v1/profiling/{current}/raw?limit=2')
                        if len(raw['items']) != 2:
                            raise ValueError(f'soak job {current} has fewer than two windows')
                        totals['completed_jobs'] += 1
                        current = None
                        break
                    stop.wait(1)
                if current and not stop.is_set():
                    raise TimeoutError(f'soak job {current} did not finish')
            except Exception as error:
                fail(error)
            finally:
                if current:
                    try:
                        request('POST', f'/v1/profiling/{current}/stop')
                    except Exception:
                        pass

    interrupted = False
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.clients + 2) as pool:
        futures = [pool.submit(query_worker, index) for index in range(args.clients)]
        futures.append(pool.submit(sample_loop))
        if args.mode == 'soak':
            futures.append(pool.submit(soak_loop))
        try:
            for future in concurrent.futures.as_completed(futures):
                future.result()
        except (Exception, KeyboardInterrupt) as error:
            interrupted = isinstance(error, KeyboardInterrupt)
            fail(error or 'interrupted')
        finally:
            stop.set()

    elapsed = time.monotonic() - started
    final = None
    try:
        time.sleep(10)
        final = resources()
        for key, limit in [('rss', args.max_rss_growth_mib * 1024**2),
                           ('fds', args.max_fd_growth), ('goroutines', args.max_goroutine_growth)]:
            if final[key] - baseline[key] > limit:
                errors.append(f'{key} growth {final[key] - baseline[key]} exceeds {limit}')
    except (Exception, KeyboardInterrupt) as error:
        interrupted = interrupted or isinstance(error, KeyboardInterrupt)
        errors.append(str(error) or 'interrupted')
    count = sum(latencies.values())

    def percentile(fraction):
        cumulative = 0
        for value, number in sorted(latencies.items()):
            cumulative += number
            if cumulative >= math.ceil(count * fraction):
                return value
        return None

    if count == 0:
        errors.append('no query requests completed')
    if args.mode == 'soak' and totals['completed_jobs'] == 0:
        errors.append('no soak job completed')
    report = {
        'mode': args.mode, 'seconds': elapsed, 'clients': args.clients,
        'requests': count, 'requests_per_second': count / max(elapsed, .001),
        'traffic': dict(traffic), 'http_statuses': dict(statuses),
        'p95_ms': percentile(.95), 'p99_ms': percentile(.99),
        'completed_jobs': totals['completed_jobs'], 'jobs': jobs,
        'baseline': baseline, 'final': final, 'errors': errors,
        'resource_limits': {'rss_growth_mib': args.max_rss_growth_mib,
                            'fd_growth': args.max_fd_growth,
                            'goroutine_growth': args.max_goroutine_growth},
    }
    (args.output / 'load-summary.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))
    return 130 if interrupted else (1 if errors else 0)


if __name__ == '__main__':
    raise SystemExit(main())
