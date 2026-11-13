// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func parseCaptureURLs(opts *captureOptions) (*url.URL, []*url.URL, error) {
	base, err := url.Parse(opts.url)
	if err != nil {
		return nil, nil, err
	}

	isHTTP := base.Scheme == "http" || base.Scheme == "https"
	hasHost := base.Host != ""
	hasCredentials := base.User != nil
	if !isHTTP || !hasHost || hasCredentials {
		return nil, nil, errors.New("require an HTTP(S) URL without embedded credentials")
	}

	hasPprofPath := strings.HasSuffix(strings.TrimSuffix(base.Path, "/"), "/debug/pprof")
	hasQueryOrFragment := base.RawQuery != "" || base.Fragment != ""
	if !hasPprofPath || hasQueryOrFragment {
		return nil, nil, errors.New("URL must end in /debug/pprof without a query or fragment")
	}

	base.Path = strings.TrimSuffix(base.Path, "/") + "/"
	base.RawPath = ""

	if len(opts.endpoints) == 0 {
		return nil, nil, errors.New("require at least one --endpoint")
	}

	names := make(map[string]bool)
	var endpoints []*url.URL
	for _, value := range opts.endpoints {
		endpoint, err := parseEndpoint(value)
		if err != nil {
			return nil, nil, err
		}

		if names[endpoint.Path] {
			return nil, nil, fmt.Errorf("duplicate endpoint %q; use separate capture directories", endpoint.Path)
		}

		names[endpoint.Path] = true
		seconds := endpoint.Query().Get("seconds")
		requiresDuration := endpoint.Path == "profile" || endpoint.Path == "trace"
		if seconds == "" && requiresDuration {
			return nil, nil, fmt.Errorf("%s requires an explicit positive seconds parameter", endpoint.Path)
		}

		if endpoint.Query().Has("seconds") {
			duration, err := strconv.ParseFloat(seconds, 64)
			validDuration := duration > 0 && duration < opts.timeout.Seconds()
			if err != nil || !validDuration {
				return nil, nil, fmt.Errorf("%s seconds must be positive and below timeout %s", endpoint.Path, opts.timeout)
			}

			if endpoint.Path != "trace" {
				if _, err := strconv.ParseInt(seconds, 10, 64); err != nil {
					return nil, nil, fmt.Errorf("%s seconds must be an integer", endpoint.Path)
				}
			}
		}

		endpoints = append(endpoints, endpoint)
	}

	if opts.pid != 0 {
		isLocalhost := base.Hostname() == "localhost"
		if !isLocalhost && !net.ParseIP(base.Hostname()).IsLoopback() {
			return nil, nil, errors.New("--pid checks require a loopback URL and the same PID namespace")
		}
	}

	return base, endpoints, nil
}

// Endpoints are relative to the pprof base, never arbitrary URLs or file paths.
func parseEndpoint(value string) (*url.URL, error) {
	endpoint, err := url.Parse(value)
	if err != nil {
		return nil, err
	}

	name := endpoint.Path
	isEmptyOrDot := name == "" || name == "." || name == ".."
	hasPathSeparators := name != "/" && strings.ContainsAny(name, `/\`)
	if isEmptyOrDot || hasPathSeparators {
		return nil, fmt.Errorf("invalid endpoint name %q", name)
	}

	hasSchemeOrHost := endpoint.IsAbs() || endpoint.Host != ""
	hasFragmentOrOpaque := endpoint.Fragment != "" || endpoint.Opaque != ""
	if hasSchemeOrHost || hasFragmentOrOpaque {
		return nil, fmt.Errorf("endpoint must be a relative pprof name: %q", value)
	}

	query, err := url.ParseQuery(endpoint.RawQuery)
	if err != nil {
		return nil, err
	}

	for _, key := range []string{"seconds", "debug", "gc"} {
		if len(query[key]) > 1 {
			return nil, fmt.Errorf("duplicate %s parameter", key)
		}
	}

	for _, key := range []string{"debug", "gc"} {
		if query.Has(key) {
			n, err := strconv.Atoi(query.Get(key))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%s must be a nonnegative integer", key)
			}
		}
	}

	return endpoint, nil
}

func endpointFile(endpoint *url.URL) string {
	switch endpoint.Path {
	case "/":
		return "index.html"
	case "cmdline", "symbol":
		return endpoint.Path + ".out"
	case "trace":
		return "trace.bin"
	case "profile":
		return "profile.pprof"
	}

	if debug, _ := strconv.Atoi(endpoint.Query().Get("debug")); debug > 0 {
		return endpoint.Path + ".out"
	}

	return endpoint.Path + ".pprof"
}
