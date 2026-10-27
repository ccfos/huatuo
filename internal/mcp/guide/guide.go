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
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	mcptool "github.com/ccfos/huatuo/internal/mcp/tool"
)

const (
	// ToolDiagnosticGuide is the fixed MCP name of the diagnostic workflow Tool.
	ToolDiagnosticGuide          = "diagnostic_guide"
	GuideStageOverview           = "overview"
	GuideStageSubsystem          = "subsystem"
	GuideStageCorrelation        = "correlation"
	GuideStageProfiler           = "profiler"
	SubsystemCPU                 = "cpu"
	SubsystemMemory              = "memory"
	SubsystemIO                  = "io"
	SubsystemNetwork             = "network"
	SubsystemHardware            = "hardware"
	ResourceURIPrefix            = "huatuo://"
	ResourcePathPrefix           = "resources/"
	AutoDiagnosisPromptPath      = "prompts/auto-diagnosis.md"
	SubsystemDiagnosisPromptPath = "prompts/subsystem-diagnosis.md"
	StorageModelPath             = "resources/storage_model.md"
	PlaybookPath                 = "resources/playbook/guide.md"
	CrossDomainPath              = "resources/cross_domain.md"
	ProfilingMethodologyPath     = "resources/profiling/methodology.md"
	ProfilingSignaturesPath      = "resources/profiling/kernel_signatures.md"
	SubsystemResourcesPrefix     = "resources/subsystems/"
	TracerResourceSuffix         = "/tracers.yaml"
	PatternResourceSuffix        = "/patterns.md"
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
		ReadOnlyHint:    boolPointer(mcptool.ReadOnlyHint),
		DestructiveHint: boolPointer(mcptool.DestructiveHint),
		IdempotentHint:  boolPointer(mcptool.IdempotentHint),
	}
	handler := makeGuideHandler(dataFS)
	if decorate != nil {
		handler = decorate(ToolDiagnosticGuide, handler)
	}
	server.AddTool(mcpgo.NewTool(
		ToolDiagnosticGuide,
		mcpgo.WithDescription("Call before diagnosis to load the server-maintained HuaTuo guide workflow."+mcptool.ToolResultUsage),
		mcpgo.WithString(mcptool.ArgumentStage, mcpgo.Description("Guide stage: overview, subsystem, correlation, or profiler; default overview.")),
		mcpgo.WithString(mcptool.ArgumentSubsystem, mcpgo.Description("Required for subsystem stage: cpu, memory, io, network, or hardware.")),
		mcpgo.WithToolAnnotation(annotation),
	), handler)
}

func makeGuideHandler(dataFS fs.FS) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		stage := strings.TrimSpace(request.GetString(mcptool.ArgumentStage, GuideStageOverview))
		if !validStage(stage) {
			return mcpgo.NewToolResultError("stage must be overview, subsystem, correlation, or profiler"), nil
		}
		subsystem := strings.TrimSpace(request.GetString(mcptool.ArgumentSubsystem, ""))
		if stage == GuideStageSubsystem && subsystem == "" {
			return mcpgo.NewToolResultError("subsystem is required for subsystem stage"), nil
		}
		if stage == GuideStageSubsystem && !validSubsystem(subsystem) {
			return mcpgo.NewToolResultError("subsystem must be cpu, memory, io, network, or hardware"), nil
		}
		if stage != GuideStageSubsystem {
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
			ErrorHandling: strings.TrimSpace(mcptool.ToolResultUsage),
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
	case GuideStageCorrelation:
		return "", []string{CrossDomainPath}, nil
	case GuideStageProfiler:
		return "", []string{
			ProfilingMethodologyPath,
			ProfilingSignaturesPath,
		}, nil
	}

	var paths []string
	var promptPath string
	if stage == GuideStageOverview {
		promptPath = AutoDiagnosisPromptPath
		paths = []string{
			StorageModelPath,
			PlaybookPath,
		}
	} else {
		promptPath = SubsystemDiagnosisPromptPath
		paths = []string{
			SubsystemResourcesPrefix + subsystem + TracerResourceSuffix,
			SubsystemResourcesPrefix + subsystem + PatternResourceSuffix,
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
	case SubsystemCPU, SubsystemMemory, SubsystemIO, SubsystemNetwork, SubsystemHardware:
		return true
	default:
		return false
	}
}

func validStage(stage string) bool {
	switch stage {
	case GuideStageOverview, GuideStageSubsystem, GuideStageCorrelation, GuideStageProfiler:
		return true
	default:
		return false
	}
}

func nextStages(stage string) []string {
	switch stage {
	case GuideStageOverview:
		return []string{GuideStageSubsystem, GuideStageCorrelation, GuideStageProfiler}
	case GuideStageSubsystem:
		return []string{GuideStageCorrelation, GuideStageProfiler}
	default:
		return nil
	}
}

func resourceURI(path string) string {
	path = strings.TrimPrefix(path, ResourcePathPrefix)
	path = strings.TrimSuffix(path, filepath.Ext(path))
	return ResourceURIPrefix + strings.ReplaceAll(path, "_", "-")
}

func boolPointer(value bool) *bool {
	return &value
}
