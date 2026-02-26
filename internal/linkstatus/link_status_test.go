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

package linkstatus

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTypes_String(t *testing.T) {
	tests := []struct {
		name string
		in   Types
		want string
	}{
		{"Unknown", Unknown, "linkstatus_unknown"},
		{"AdminUp", AdminUp, "linkstatus_adminup"},
		{"AdminDown", AdminDown, "linkstatus_admindown"},
		{"CarrierUp", CarrierUp, "linkstatus_carrierup"},
		{"CarrierDown", CarrierDown, "linkstatus_carrierdown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChanged(t *testing.T) {
	tests := []struct {
		name   string
		flags  uint32
		change uint32
		want   []Types
	}{
		{
			name:   "no_change",
			flags:  0,
			change: 0,
			want:   nil,
		},
		{
			name:   "admin_up",
			flags:  unix.IFF_UP,
			change: unix.IFF_UP,
			want:   []Types{AdminUp},
		},
		{
			name:   "admin_down",
			flags:  0,
			change: unix.IFF_UP,
			want:   []Types{AdminDown},
		},
		{
			name:   "carrier_up",
			flags:  unix.IFF_LOWER_UP,
			change: unix.IFF_LOWER_UP,
			want:   []Types{CarrierUp},
		},
		{
			name:   "carrier_down",
			flags:  0,
			change: unix.IFF_LOWER_UP,
			want:   []Types{CarrierDown},
		},
		{
			name:   "admin_and_carrier_up",
			flags:  unix.IFF_UP | unix.IFF_LOWER_UP,
			change: unix.IFF_UP | unix.IFF_LOWER_UP,
			want:   []Types{AdminUp, CarrierUp},
		},
		{
			name:   "admin_down_carrier_up",
			flags:  unix.IFF_LOWER_UP,
			change: unix.IFF_UP | unix.IFF_LOWER_UP,
			want:   []Types{AdminDown, CarrierUp},
		},
		{
			name:   "ignore_irrelevant_change_bits",
			flags:  0,
			change: unix.IFF_PROMISC,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Changed(tt.flags, tt.change)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Changed(%#x, %#x) = %#v, want %#v", tt.flags, tt.change, got, tt.want)
			}
		})
	}
}

func TestFlagsRaw(t *testing.T) {
	tests := []struct {
		name  string
		flags uint32
		want  []string
	}{
		{
			name:  "invalid_zero",
			flags: 0,
			want:  []string{"unknown"},
		},
		{
			name:  "admin_up_carrier_up",
			flags: unix.IFF_UP | unix.IFF_LOWER_UP,
			want:  []string{"linkstatus_adminup", "linkstatus_carrierup"},
		},
		{
			name:  "admin_down_carrier_down",
			flags: unix.IFF_BROADCAST, // 非 0，且不含 IFF_UP/IFF_LOWER_UP
			want:  []string{"linkstatus_admindown", "linkstatus_carrierdown"},
		},
		{
			name:  "admin_up_carrier_down",
			flags: unix.IFF_UP,
			want:  []string{"linkstatus_adminup", "linkstatus_carrierdown"},
		},
		{
			name:  "admin_down_carrier_up",
			flags: unix.IFF_LOWER_UP,
			want:  []string{"linkstatus_admindown", "linkstatus_carrierup"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FlagsRaw(tt.flags)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FlagsRaw(%#x) = %#v, want %#v", tt.flags, got, tt.want)
			}
		})
	}
}
