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

package tool

import (
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/mcp/constant"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestRegionRouterPrecedenceAndFallback(t *testing.T) {
	router, err := newRegionRouter("default", []regionValue[string]{
		{name: "^cn-", value: "regex"},
		{name: "cn-prod", value: "exact"},
	})
	if err != nil {
		t.Fatalf("newRegionRouter() error = %v", err)
	}
	tests := map[string]string{
		"cn-prod": "exact",
		"cn-test": "regex",
		"unknown": "default",
		"":        "default",
	}
	for region, want := range tests {
		if got := router.forRegion(region); got != want {
			t.Errorf("forRegion(%q) = %q, want %q", region, got, want)
		}
	}
}

func TestRequiredRegionsValidatesAndDeduplicates(t *testing.T) {
	request := func(value any) mcpgo.CallToolRequest {
		return mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Arguments: map[string]any{
			constant.ArgumentRegion: value,
		}}}
	}
	regions, err := requiredRegions(request([]any{" cn ", "us", "cn"}), 3)
	if err != nil || !reflect.DeepEqual(regions, []string{"cn", "us"}) {
		t.Fatalf("requiredRegions() = %v, %v", regions, err)
	}
	for _, value := range []any{
		"cn",
		[]any{},
		[]any{""},
		[]any{"*"},
		[]any{"cn", "us", "eu"},
	} {
		if _, err := requiredRegions(request(value), 2); err == nil {
			t.Errorf("requiredRegions(%v) error = nil", value)
		}
	}
}
