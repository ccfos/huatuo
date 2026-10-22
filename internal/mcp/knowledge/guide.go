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

package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ccfos/huatuo/internal/mcp/constant"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type guideResource struct {
	URI     string `json:"uri"`
	Content string `json:"content"`
}

type guideResult struct {
	Stage         string          `json:"stage"`
	Subsystem     string          `json:"subsystem,omitempty"`
	Prompt        string          `json:"prompt,omitempty"`
	Resources     []guideResource `json:"resources"`
	NextStages    []string        `json:"next_stages,omitempty"`
	Rule          string          `json:"rule"`
	ErrorHandling string          `json:"error_handling"`
}

func registerGuideTool(
	dataFS fs.FS,
	server *mcpserver.MCPServer,
	decorate ToolDecorator,
) {
	annotation := mcpgo.ToolAnnotation{
		ReadOnlyHint:    boolPointer(constant.ReadOnlyHint),
		DestructiveHint: boolPointer(constant.DestructiveHint),
		IdempotentHint:  boolPointer(constant.IdempotentHint),
	}
	handler := makeGuideHandler(dataFS)
	if decorate != nil {
		handler = decorate(constant.ToolDiagnosticGuide, handler)
	}
	server.AddTool(mcpgo.NewTool(
		constant.ToolDiagnosticGuide,
		mcpgo.WithDescription("Call before diagnosis to load the server-maintained HuaTuo knowledge workflow."+constant.ToolResultUsage),
		mcpgo.WithString(constant.ArgumentStage, mcpgo.Description("Knowledge stage: overview, subsystem, correlation, or profiler; default overview.")),
		mcpgo.WithString(constant.ArgumentSubsystem, mcpgo.Description("Required for subsystem stage: cpu, memory, io, network, or hardware.")),
		mcpgo.WithToolAnnotation(annotation),
	), handler)
}

func makeGuideHandler(dataFS fs.FS) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		stage := strings.TrimSpace(request.GetString(constant.ArgumentStage, constant.GuideStageOverview))
		if !validStage(stage) {
			return mcpgo.NewToolResultError("stage must be overview, subsystem, correlation, or profiler"), nil
		}
		subsystem := strings.TrimSpace(request.GetString(constant.ArgumentSubsystem, ""))
		if stage == constant.GuideStageSubsystem && subsystem == "" {
			return mcpgo.NewToolResultError("subsystem is required for subsystem stage"), nil
		}
		if stage == constant.GuideStageSubsystem && !validSubsystem(subsystem) {
			return mcpgo.NewToolResultError("subsystem must be cpu, memory, io, network, or hardware"), nil
		}
		if stage != constant.GuideStageSubsystem {
			subsystem = ""
		}

		prompt, paths, err := renderGuideStage(
			ctx,
			dataFS,
			stage,
			subsystem,
		)
		if err != nil {
			return nil, err
		}
		resources := make([]guideResource, 0, len(paths))
		for _, path := range paths {
			content, err := fs.ReadFile(dataFS, path)
			if err != nil {
				return nil, fmt.Errorf("read diagnostic guide resource %s: %w", path, err)
			}
			resources = append(resources, guideResource{
				URI:     resourceURI(path),
				Content: string(content),
			})
		}

		payload, err := json.Marshal(guideResult{
			Stage:         stage,
			Subsystem:     subsystem,
			Prompt:        prompt,
			Resources:     resources,
			NextStages:    nextStages(stage),
			Rule:          "Use only tools advertised by the current MCP server.",
			ErrorHandling: strings.TrimSpace(constant.ToolResultUsage),
		})
		if err != nil {
			return nil, fmt.Errorf("encode diagnostic guide: %w", err)
		}
		return mcpgo.NewToolResultText(string(payload)), nil
	}
}

func renderGuideStage(
	_ context.Context,
	dataFS fs.FS,
	stage string,
	subsystem string,
) (string, []string, error) {
	switch stage {
	case constant.GuideStageCorrelation:
		return "", []string{constant.CrossDomainPath}, nil
	case constant.GuideStageProfiler:
		return "", []string{
			constant.ProfilingMethodologyPath,
			constant.ProfilingSignaturesPath,
		}, nil
	}

	var paths []string
	var promptPath string
	if stage == constant.GuideStageOverview {
		promptPath = constant.AutoDiagnosisPromptPath
		paths = []string{
			constant.StorageModelPath,
			constant.PlaybookPath,
		}
	} else {
		promptPath = constant.SubsystemDiagnosisPromptPath
		paths = []string{
			constant.SubsystemResourcesPrefix + subsystem + constant.TracerResourceSuffix,
			constant.SubsystemResourcesPrefix + subsystem + constant.PatternResourceSuffix,
		}
	}
	template, err := fs.ReadFile(dataFS, promptPath)
	if err != nil {
		return "", nil, fmt.Errorf("read diagnostic guide prompt %s: %w", promptPath, err)
	}
	prompt := strings.ReplaceAll(string(template), "{subsystem}", subsystem)
	return prompt, paths, nil
}

func validSubsystem(subsystem string) bool {
	switch subsystem {
	case constant.SubsystemCPU, constant.SubsystemMemory, constant.SubsystemIO, constant.SubsystemNetwork, constant.SubsystemHardware:
		return true
	default:
		return false
	}
}

func validStage(stage string) bool {
	switch stage {
	case constant.GuideStageOverview, constant.GuideStageSubsystem, constant.GuideStageCorrelation, constant.GuideStageProfiler:
		return true
	default:
		return false
	}
}

func nextStages(stage string) []string {
	switch stage {
	case constant.GuideStageOverview:
		return []string{constant.GuideStageSubsystem, constant.GuideStageCorrelation, constant.GuideStageProfiler}
	case constant.GuideStageSubsystem:
		return []string{constant.GuideStageCorrelation, constant.GuideStageProfiler}
	default:
		return nil
	}
}

func resourceURI(path string) string {
	path = strings.TrimPrefix(path, constant.ResourcePathPrefix)
	path = strings.TrimSuffix(path, filepath.Ext(path))
	return constant.ResourceURIPrefix + strings.ReplaceAll(path, "_", "-")
}

func boolPointer(value bool) *bool {
	return &value
}
