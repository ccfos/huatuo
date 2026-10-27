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

package guide

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	mcpevent "github.com/ccfos/huatuo/internal/mcp/backend/event"
	mcptool "github.com/ccfos/huatuo/internal/mcp/tool"
	mcputils "github.com/ccfos/huatuo/internal/mcp/utils"
)

func TestGuideExposesErrorHandlingInEveryStage(t *testing.T) {
	handler := makeGuideHandler(NewFS(""))
	for _, stage := range []string{"overview", "subsystem", "correlation", "profiler"} {
		t.Run(stage, func(t *testing.T) {
			request := mcpgo.CallToolRequest{}
			request.Params.Arguments = map[string]any{"stage": stage, "subsystem": "cpu"}
			result, err := handler(t.Context(), request)
			if err != nil || result.IsError {
				t.Fatalf("guide failed: %+v, %v", result, err)
			}
			var payload guideResult
			if err := json.Unmarshal([]byte(result.Content[0].(mcpgo.TextContent).Text), &payload); err != nil {
				t.Fatal(err)
			}
			for _, rule := range []string{"invalid_argument", "retryable=false", "status=partial", "not an empty result"} {
				if !strings.Contains(payload.ErrorHandling, rule) {
					t.Errorf("stage %s omitted %q: %+v", stage, rule, payload)
				}
			}
		})
	}
}

func TestAutoDiagnosisPromptRequiresRegionAndTimeRange(t *testing.T) {
	handler := handleAutoDiagnosis(fstest.MapFS{
		"prompts/auto-diagnosis.md": {Data: []byte("diagnose {target} in {region}")},
	}, 0, 5)
	base := map[string]string{mcptool.ArgumentHost: "node-1"}
	if _, err := handler(t.Context(), promptRequest(base)); err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("missing region error = %v", err)
	}
	base[mcputils.ArgumentRegion] = "cn"
	if _, err := handler(t.Context(), promptRequest(base)); err == nil || !strings.Contains(err.Error(), "time_range") {
		t.Fatalf("missing time range error = %v", err)
	}
	base[mcptool.ArgumentTimeRange] = "1h"
	result, err := handler(t.Context(), promptRequest(base))
	if err != nil {
		t.Fatal(err)
	}
	text := result.Messages[0].Content.(mcpgo.TextContent).Text
	if !strings.Contains(text, "**Region**: cn") || !strings.Contains(text, "diagnose node-1 in cn") {
		t.Fatalf("rendered prompt = %s", text)
	}
}

func promptRequest(arguments map[string]string) mcpgo.GetPromptRequest {
	return mcpgo.GetPromptRequest{Params: mcpgo.GetPromptParams{Arguments: arguments}}
}

func TestPromptsRejectInvalidArguments(t *testing.T) {
	handler := handleSubsystemDiagnosis(fstest.MapFS{
		"prompts/subsystem-diagnosis.md": {Data: []byte("diagnose {target}")},
	}, 0, 5)
	for _, mutation := range []struct{ key, value string }{
		{mcptool.ArgumentSubsystem, "cpus"},
		{mcptool.ArgumentHost, "  "},
		{mcptool.ArgumentHost, "node\ninstruction"},
		{mcptool.ArgumentHost, strings.Repeat("x", mcpevent.MaxTargetValueLength+1)},
		{mcputils.ArgumentRegion, "*"},
		{mcptool.ArgumentTimeRange, "invalid"},
	} {
		t.Run(mutation.key+mutation.value[:min(len(mutation.value), 20)], func(t *testing.T) {
			arguments := map[string]string{
				mcptool.ArgumentSubsystem: "cpu", mcptool.ArgumentHost: "node-1",
				mcputils.ArgumentRegion: "cn", mcptool.ArgumentTimeRange: "1h",
			}
			arguments[mutation.key] = mutation.value
			if _, err := handler(t.Context(), promptRequest(arguments)); err == nil {
				t.Fatal("invalid prompt accepted")
			}
		})
	}
}
