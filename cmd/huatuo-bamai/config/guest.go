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

package config

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/runmode"
)

// GuestTransportConfig opts a sandbox into write-only Host persistence.
type GuestTransportConfig struct {
	Enabled          bool
	Port             uint32 `default:"19705"`
	MaxMessageBytes  int    `default:"16777216"`
	MaxPendingBytes  int    `default:"67108864"`
	IOTimeoutSeconds int    `default:"10"`
}

// GuestBridgeConfig controls authenticated Host listeners. It is off by default.
type GuestBridgeConfig struct {
	Enabled                 bool
	Port                    uint32 `default:"19705"`
	MaxConnections          int    `default:"512"`
	MaxMessageBytes         uint64 `default:"16777216"`
	MaxInFlightBytes        uint64 `default:"268435456"`
	HandshakeTimeoutSeconds int    `default:"10"`
	FirecrackerRoot         string `default:"/run/vc/firecracker"`
}

func (c *Config) validateGuestTransport() error {
	if c.GuestTransport.Enabled {
		g := c.GuestTransport
		if c.Mode != runmode.Sandbox {
			return errors.New("GuestTransport requires sandbox mode")
		}
		if g.Port <= 2 || g.Port == ^uint32(0) || g.MaxMessageBytes < 128 || g.MaxPendingBytes < g.MaxMessageBytes || g.IOTimeoutSeconds <= 0 {
			return errors.New("GuestTransport has invalid port, byte limits or timeout")
		}
	}
	if c.GuestBridge.Enabled {
		g := c.GuestBridge
		if c.Mode == runmode.Sandbox {
			return errors.New("GuestBridge requires Host mode")
		}
		if g.Port <= 2 || g.Port == ^uint32(0) || g.MaxConnections <= 0 || g.MaxMessageBytes < 128 || g.MaxInFlightBytes < g.MaxMessageBytes || g.MaxInFlightBytes > (^uint64(0)>>1) || g.HandshakeTimeoutSeconds <= 0 {
			return errors.New("GuestBridge has invalid port, byte limits or timeout")
		}
		if !filepath.IsAbs(g.FirecrackerRoot) || filepath.Clean(g.FirecrackerRoot) != g.FirecrackerRoot || g.FirecrackerRoot == "/" {
			return fmt.Errorf("GuestBridge.FirecrackerRoot must be a clean absolute runtime directory")
		}
	}
	return nil
}
