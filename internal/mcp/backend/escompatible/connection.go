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

package escompatible

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/ccfos/huatuo/internal/mcp/constant"
)

// Connection contains only ES-compatible protocol settings, not event policy.
type Connection struct {
	AuthType string
	Address  string
	Username string
	Password string
	Index    string
}

func (input *Connection) Inherit(defaults *Connection) Connection {
	c := *input
	if defaults != nil {
		if c.AuthType == "" {
			c.AuthType = defaults.AuthType
		}
		if c.Address == "" {
			c.Address = defaults.Address
		}
		if c.Username == "" {
			c.Username = defaults.Username
		}
		if c.Password == "" {
			c.Password = defaults.Password
		}
		if c.Index == "" {
			c.Index = defaults.Index
		}
	}
	if c.AuthType == constant.AuthTypeNone {
		c.Username, c.Password = "", ""
	}
	return c
}

func (c *Connection) Enabled() bool {
	return strings.TrimSpace(c.Address) != "" && strings.TrimSpace(c.Index) != ""
}

func (c *Connection) Validate() error {
	if c.AuthType != "" && c.AuthType != constant.AuthTypeNone && c.AuthType != constant.AuthTypeBasic {
		return errors.New("ES-compatible auth type must be empty, none, or basic")
	}
	address, username, password := strings.TrimSpace(c.Address), strings.TrimSpace(c.Username), strings.TrimSpace(c.Password)
	if address == "" {
		if username != "" || password != "" {
			return errors.New("ES-compatible address is required when credentials are configured")
		}
		return nil
	}
	if strings.TrimSpace(c.Index) == "" {
		return errors.New("ES-compatible index is required")
	}
	if (username == "") != (password == "") {
		return errors.New("ES-compatible username and password must be configured together")
	}
	if c.AuthType == constant.AuthTypeBasic && username == "" {
		return errors.New("ES-compatible basic authentication requires username and password")
	}
	for _, value := range strings.Split(address, ",") {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("invalid ES-compatible address %q", value)
		}
	}
	return nil
}
