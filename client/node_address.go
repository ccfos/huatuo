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

package client

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// NodeAddress identifies one Node Agent HTTP endpoint. HostPort must use the
// host:port form accepted by net.SplitHostPort, including brackets around IPv6
// addresses. An empty Scheme selects HTTP; HTTPS is also supported.
type NodeAddress struct {
	HostPort string
	Scheme   string
}

func (a NodeAddress) serverURL() (string, error) {
	scheme := a.Scheme
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return "", invalidNodeAddress(
			fmt.Sprintf("node address scheme %q is unsupported; use http or https", a.Scheme),
		)
	}
	if a.HostPort == "" {
		return "", invalidNodeAddress("node address host:port is required")
	}
	if strings.TrimSpace(a.HostPort) != a.HostPort {
		return "", invalidNodeAddress("node address host:port must not contain surrounding whitespace")
	}

	host, portText, err := net.SplitHostPort(a.HostPort)
	if err != nil {
		return "", invalidNodeAddress(
			fmt.Sprintf("node address %q must use host:port: %v", a.HostPort, err),
		)
	}
	if host == "" {
		return "", invalidNodeAddress("node address host is required")
	}
	if strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return "", invalidNodeAddress(
			fmt.Sprintf("node address host %q must not contain whitespace", host),
		)
	}
	if strings.IndexFunc(host, unicode.IsControl) >= 0 {
		return "", invalidNodeAddress(
			fmt.Sprintf("node address host %q must not contain control characters", host),
		)
	}
	if strings.ContainsAny(host, "/?#@[]\\") {
		return "", invalidNodeAddress(
			fmt.Sprintf("node address host %q must not contain URL delimiters", host),
		)
	}
	if strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(host); err != nil {
			return "", invalidNodeAddress(
				fmt.Sprintf("node address host %q must be a valid IPv6 address", host),
			)
		}
	}

	if strings.IndexFunc(portText, func(r rune) bool {
		return r < '0' || r > '9'
	}) >= 0 {
		return "", invalidNodeAddress(
			fmt.Sprintf("node address port %q must be numeric", portText),
		)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", invalidNodeAddress(
			fmt.Sprintf("node address port %q is outside 1..65535", portText),
		)
	}

	return (&url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.FormatUint(port, 10)),
	}).String(), nil
}

func invalidNodeAddress(message string) error {
	return &NodeError{
		Code:    NodeErrorCodeInvalidArgument,
		Message: message,
	}
}
