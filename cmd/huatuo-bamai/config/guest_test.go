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
	"testing"
)

func TestGuestTransportConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"disabled", "", true},
		{"guest", "Mode=\"sandbox\"\n[GuestTransport]\nEnabled=true", true},
		{"host", "[GuestBridge]\nEnabled=true", true},
		{"host sender", "[GuestTransport]\nEnabled=true", false},
		{"guest bridge", "Mode=\"sandbox\"\n[GuestBridge]\nEnabled=true", false},
		{"small queue", "Mode=\"sandbox\"\n[GuestTransport]\nEnabled=true\nMaxPendingBytes=1", false},
		{"small budget", "[GuestBridge]\nEnabled=true\nMaxInFlightBytes=1", false},
		{"unsafe root", "[GuestBridge]\nEnabled=true\nFirecrackerRoot=\"/\"", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), "agent.conf", tc.body)
			err := Load(path)
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && err == nil {
				t.Error("invalid configuration accepted")
			}
		})
	}
}

func TestGuestTransportRequiresRestart(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "agent.conf", "")
	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	if err := Update(map[string]any{"GuestBridge.Enabled": true}); !errors.Is(err, ErrInvalidUpdate) {
		t.Errorf("enable bridge=%v", err)
	}
	if Get().GuestBridge.Enabled {
		t.Error("rejected update was published")
	}
}
