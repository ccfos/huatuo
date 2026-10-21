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
	"fmt"
	"io/fs"
	"strings"
	"time"
	"unicode"

	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/utils"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func RegisterPrompts(dataFS fs.FS, mcpServer *mcpserver.MCPServer, maxQueryRange time.Duration, maxRegions int) {
	subsystemDiag := mcpgo.NewPrompt(constant.SubsystemDiagnosisPrompt,
		mcpgo.WithPromptDescription("Single-dimension deep diagnosis flow. Guides the agent through a structured analysis of a specific subsystem (cpu/memory/io/network) anomaly."),
		mcpgo.WithArgument(constant.ArgumentSubsystem,
			mcpgo.ArgumentDescription("Subsystem to diagnose: cpu | memory | io | network | hardware"),
			mcpgo.RequiredArgument(),
		),
		mcpgo.WithArgument(constant.ArgumentHost,
			mcpgo.ArgumentDescription("Host hostname for host-level diagnosis"),
		),
		mcpgo.WithArgument(constant.ArgumentContainerHost,
			mcpgo.ArgumentDescription("Container hostname for container-level diagnosis"),
		),
		mcpgo.WithArgument(constant.ArgumentRegion,
			mcpgo.ArgumentDescription("Exact region or comma-separated exact region list"),
			mcpgo.RequiredArgument(),
		),
		mcpgo.WithArgument(constant.ArgumentTimeRange,
			mcpgo.ArgumentDescription("User-specified time range for analysis"),
			mcpgo.RequiredArgument(),
		),
	)
	mcpServer.AddPrompt(subsystemDiag, handleSubsystemDiagnosis(dataFS, maxQueryRange, maxRegions))

	autoDiag := mcpgo.NewPrompt(constant.AutoDiagnosisPrompt,
		mcpgo.WithPromptDescription("Multi-dimensional automatic diagnosis flow. Performs comprehensive cross-subsystem analysis when the problem is unclear."),
		mcpgo.WithArgument(constant.ArgumentHost,
			mcpgo.ArgumentDescription("Host hostname for diagnosis"),
		),
		mcpgo.WithArgument(constant.ArgumentContainerHost,
			mcpgo.ArgumentDescription("Container hostname for diagnosis"),
		),
		mcpgo.WithArgument(constant.ArgumentRegion,
			mcpgo.ArgumentDescription("Exact region or comma-separated exact region list"),
			mcpgo.RequiredArgument(),
		),
		mcpgo.WithArgument(constant.ArgumentTimeRange,
			mcpgo.ArgumentDescription("User-specified time range for analysis"),
			mcpgo.RequiredArgument(),
		),
	)
	mcpServer.AddPrompt(autoDiag, handleAutoDiagnosis(dataFS, maxQueryRange, maxRegions))
}

func handleSubsystemDiagnosis(dataFS fs.FS, maxQueryRange time.Duration, maxRegions int) func(ctx context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
	return func(ctx context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
		startTime := time.Now()
		arguments, err := validatePromptArguments(req.Params.Arguments, true, maxQueryRange, maxRegions)
		if err != nil {
			return nil, err
		}
		subsystem := arguments[constant.ArgumentSubsystem]
		hostname := arguments[constant.ArgumentHost]
		containerHostname := arguments[constant.ArgumentContainerHost]
		region := arguments[constant.ArgumentRegion]
		timeRange := arguments[constant.ArgumentTimeRange]

		infof("prompt get: name=subsystem-diagnosis subsystem=%s host=%s container=%s region=%s range=%s", subsystem, hostname, containerHostname, region, timeRange)

		template, err := fs.ReadFile(dataFS, constant.SubsystemDiagnosisPromptPath)
		if err != nil {
			return nil, fmt.Errorf("read subsystem-diagnosis.md: %w", err)
		}

		target := hostname
		if target == "" {
			target = containerHostname
		}

		// Replace placeholders in the template body so the agent sees concrete
		// values instead of {subsystem}/{host}/{container_host}/{target}.
		rendered := string(template)
		rendered = strings.ReplaceAll(rendered, "{subsystem}", subsystem)
		rendered = strings.ReplaceAll(rendered, "{host}", hostname)
		rendered = strings.ReplaceAll(rendered, "{container_host}", containerHostname)
		rendered = strings.ReplaceAll(rendered, "{target}", target)
		rendered = strings.ReplaceAll(rendered, "{region}", region)

		enriched := fmt.Sprintf(
			"# Subsystem Diagnosis: %s\n\n"+
				"**Host**: %s\n"+
				"**Container Host**: %s\n"+
				"**Region**: %s\n"+
				"**Time Range**: %s\n\n"+
				"---\n\n"+
				"%s\n\n"+
				"---\n\n"+
				"## Quick Start\n"+
				"1. Read `huatuo://storage-model` for storage model and core principles\n"+
				"2. Read `huatuo://subsystems/%s/tracers` to get tracer metadata for %s\n"+
				"3. Follow the steps above, calling the tools as instructed\n"+
				"4. Read `huatuo://subsystems/%s/patterns` for root cause matching\n"+
				"5. If automatic profile data is available, read `huatuo://profiling/methodology` and `huatuo://profiling/kernel-signatures`\n",
			subsystem, hostname, containerHostname, region, timeRange, rendered, subsystem, subsystem, subsystem,
		)

		infof("prompt get result: name=subsystem-diagnosis subsystem=%s target=%s dur=%s", subsystem, target, time.Since(startTime).Round(time.Millisecond))

		return mcpgo.NewGetPromptResult(
			fmt.Sprintf("Deep diagnosis for %s subsystem on %s", subsystem, target),
			[]mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(
					mcpgo.RoleUser,
					mcpgo.NewTextContent(enriched),
				),
			},
		), nil
	}
}

func handleAutoDiagnosis(dataFS fs.FS, maxQueryRange time.Duration, maxRegions int) func(ctx context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
	return func(ctx context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
		startTime := time.Now()
		arguments, err := validatePromptArguments(req.Params.Arguments, false, maxQueryRange, maxRegions)
		if err != nil {
			return nil, err
		}
		hostname := arguments[constant.ArgumentHost]
		containerHostname := arguments[constant.ArgumentContainerHost]
		region := arguments[constant.ArgumentRegion]
		timeRange := arguments[constant.ArgumentTimeRange]

		infof("prompt get: name=auto-diagnosis host=%s container=%s region=%s range=%s", hostname, containerHostname, region, timeRange)

		template, err := fs.ReadFile(dataFS, constant.AutoDiagnosisPromptPath)
		if err != nil {
			return nil, fmt.Errorf("read auto-diagnosis.md: %w", err)
		}

		target := hostname
		if target == "" {
			target = containerHostname
		}

		// Replace placeholders in the template body so the agent sees concrete
		// values instead of {host}/{container_host}/{target}.
		rendered := string(template)
		rendered = strings.ReplaceAll(rendered, "{host}", hostname)
		rendered = strings.ReplaceAll(rendered, "{container_host}", containerHostname)
		rendered = strings.ReplaceAll(rendered, "{target}", target)
		rendered = strings.ReplaceAll(rendered, "{region}", region)

		enriched := fmt.Sprintf(
			"# Auto Diagnosis: %s\n\n"+
				"**Host**: %s\n"+
				"**Container Host**: %s\n"+
				"**Region**: %s\n"+
				"**Time Range**: %s\n\n"+
				"---\n\n"+
				"%s\n\n"+
				"---\n\n"+
				"## Quick Start\n"+
				"1. Read `huatuo://storage-model` for storage model and core principles\n"+
				"2. Read `huatuo://playbook/guide` for MCP usage, result handling, and error handling\n"+
				"3. Read `huatuo://subsystems/cpu/tracers`, `huatuo://subsystems/memory/tracers`, `huatuo://subsystems/io/tracers`, and `huatuo://subsystems/network/tracers` to identify applicable tracers\n"+
				"4. Follow the phases above for multi-dimensional scanning\n"+
				"5. For each anomaly dimension, read `huatuo://subsystems/{subsystem}/patterns` for root cause matching\n"+
				"6. Read `huatuo://cross-domain` for cross-subsystem correlation analysis\n"+
				"7. If automatic profile data is available, read `huatuo://profiling/methodology` and `huatuo://profiling/kernel-signatures`\n",
			target, hostname, containerHostname, region, timeRange, rendered,
		)

		infof("prompt get result: name=auto-diagnosis host=%s container=%s dur=%s", hostname, containerHostname, time.Since(startTime).Round(time.Millisecond))

		return mcpgo.NewGetPromptResult(
			fmt.Sprintf("Auto diagnosis for %s", target),
			[]mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(
					mcpgo.RoleUser,
					mcpgo.NewTextContent(enriched),
				),
			},
		), nil
	}
}

func validatePromptArguments(input map[string]string, subsystemRequired bool, maxQueryRange time.Duration, maxRegions int) (map[string]string, error) {
	arguments := make(map[string]string, len(input))
	for key, value := range input {
		if len(value) > constant.MaxTargetValueLength || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, fmt.Errorf("%s must be at most %d bytes and contain no control characters", key, constant.MaxTargetValueLength)
		}
		arguments[key] = strings.TrimSpace(value)
	}
	if subsystemRequired && !validSubsystem(arguments[constant.ArgumentSubsystem]) {
		return nil, fmt.Errorf("subsystem must be cpu, memory, io, network, or hardware")
	}
	if arguments[constant.ArgumentHost] == "" && arguments[constant.ArgumentContainerHost] == "" {
		return nil, fmt.Errorf("at least one of host or container_host must be provided")
	}
	regions := strings.Split(arguments[constant.ArgumentRegion], ",")
	if maxRegions <= 0 {
		maxRegions = constant.MaxRegions
	}
	if len(regions) > maxRegions {
		return nil, fmt.Errorf("region must contain at most %d values", maxRegions)
	}
	for _, region := range regions {
		region = strings.TrimSpace(region)
		if region == "" || region == "*" || strings.EqualFold(region, "all") || strings.EqualFold(region, "_all") || len(region) > constant.MaxRegionNameLength {
			return nil, fmt.Errorf("region must contain bounded exact region names")
		}
	}
	if arguments[constant.ArgumentTimeRange] == "" {
		return nil, fmt.Errorf("time_range argument is required")
	}
	if _, _, err := utils.ParseTimeRange(arguments[constant.ArgumentTimeRange], maxQueryRange); err != nil {
		return nil, fmt.Errorf("invalid time_range: %w", err)
	}
	return arguments, nil
}
