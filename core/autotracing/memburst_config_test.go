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

package autotracing

import "testing"

func TestValidateMemBurst(t *testing.T) {
	valid := MemBurstConfig{
		DeltaMemoryBurst:    100,
		DeltaAnonThreshold:  70,
		Interval:            10,
		IntervalTracing:     1800,
		SlidingWindowLength: 60,
		DumpProcessMaxNum:   10,
	}

	cases := []struct {
		name    string
		modify  func(*MemBurstConfig)
		wantErr bool
	}{
		{name: "valid", modify: func(*MemBurstConfig) {}},
		{name: "zero delta memory burst", modify: func(c *MemBurstConfig) { c.DeltaMemoryBurst = 0 }, wantErr: true},
		{name: "negative delta memory burst", modify: func(c *MemBurstConfig) { c.DeltaMemoryBurst = -1 }, wantErr: true},
		{name: "anon threshold below range", modify: func(c *MemBurstConfig) { c.DeltaAnonThreshold = -1 }, wantErr: true},
		{name: "anon threshold above range", modify: func(c *MemBurstConfig) { c.DeltaAnonThreshold = 101 }, wantErr: true},
		{name: "anon threshold at zero", modify: func(c *MemBurstConfig) { c.DeltaAnonThreshold = 0 }},
		{name: "anon threshold at hundred", modify: func(c *MemBurstConfig) { c.DeltaAnonThreshold = 100 }},
		{name: "zero interval", modify: func(c *MemBurstConfig) { c.Interval = 0 }, wantErr: true},
		{name: "zero interval tracing", modify: func(c *MemBurstConfig) { c.IntervalTracing = 0 }, wantErr: true},
		{name: "zero sliding window", modify: func(c *MemBurstConfig) { c.SlidingWindowLength = 0 }, wantErr: true},
		{name: "zero dump process max num", modify: func(c *MemBurstConfig) { c.DumpProcessMaxNum = 0 }, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.modify(&c)
			err := validateMemBurst(&c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateMemBurst() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
