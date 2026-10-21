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
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/flamegraph"
	tracingstore "github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/utils"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type profileAnalysisResult struct {
	Status       string                     `json:"status"`
	Regions      []string                   `json:"regions"`
	TracerName   string                     `json:"tracer_name"`
	Targets      map[string][]string        `json:"targets"`
	TimeRange    string                     `json:"time_range"`
	AnalysisType string                     `json:"analysis_type"`
	TopFunctions []hotspotFunction          `json:"top_functions"`
	Sources      []profileSource            `json:"sources"`
	Summary      profileSummary             `json:"summary"`
	Errors       []regionFailure            `json:"errors,omitempty"`
	QueryStatus  []tracingstore.QueryStatus `json:"query_status,omitempty"`
}

type profileSource struct {
	Source            *tracingstore.SourceRef `json:"source,omitempty"`
	Region            string                  `json:"region"`
	TracerName        string                  `json:"tracer_name"`
	Hostname          string                  `json:"hostname,omitempty"`
	ContainerHostname string                  `json:"container_hostname,omitempty"`
	Timestamp         time.Time               `json:"timestamp"`
}

type hotspotFunction struct {
	Rank         int               `json:"rank"`
	Name         string            `json:"name"`
	SelfPercent  float64           `json:"self_percent"`
	TotalPercent float64           `json:"total_percent"`
	SampleCount  int64             `json:"sample_count"`
	CallPaths    []profileCallPath `json:"call_paths"`
}

type profileCallPath struct {
	Path         []string `json:"path"`
	SelfSamples  int64    `json:"self_samples"`
	TotalSamples int64    `json:"total_samples"`
	Percentage   float64  `json:"percentage"`
}

type profileSummary struct {
	SampleType           string   `json:"sample_type,omitempty"`
	SampleUnit           string   `json:"sample_unit,omitempty"`
	Truncated            bool     `json:"truncated"`
	TruncatedRegions     []string `json:"truncated_regions,omitempty"`
	TotalSamples         int64    `json:"total_samples"`
	UniqueFunctions      int      `json:"unique_functions"`
	Documents            int      `json:"documents"`
	UnsupportedDocuments int      `json:"unsupported_documents"`
	PayloadFormat        string   `json:"payload_format"`
	Coverage             float64  `json:"coverage"`
}

type frameFunctionStat struct {
	name         string
	selfValue    int64
	totalValue   int64
	selfPercent  float64
	totalPercent float64
	sampleCount  int64
	leafPaths    map[string]*framePathStat
}

type framePathStat struct {
	path          []string
	selfValue     int64
	totalValue    int64
	selfPathTotal int64
}

var errUnsupportedProfilePayload = errors.New("profiler documents contain no supported tracer_data.flamedata frame arrays or pprof samples")

func (r *eventTools) queryProfiles(
	ctx context.Context,
	request mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	tracer, err := request.RequireString(constant.ArgumentTracerName)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	tracer = strings.TrimSpace(tracer)
	if tracer == "" {
		return mcpgo.NewToolResultError("tracer_name is required"), nil
	}
	if len(tracer) > constant.MaxTracerNameLength {
		return mcpgo.NewToolResultError(fmt.Sprintf("tracer_name must not exceed %d bytes", constant.MaxTracerNameLength)), nil
	}
	regions, err := requiredRegions(request, r.maxRegions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	recordID := strings.TrimSpace(request.GetString(constant.ArgumentRecordID, ""))
	if _, present := request.GetArguments()[constant.ArgumentRecordID]; present && recordID == "" {
		return mcpgo.NewToolResultError("record_id must not be blank"), nil
	}
	if len(recordID) > constant.MaxRecordIDLength {
		return mcpgo.NewToolResultError(fmt.Sprintf("record_id must not exceed %d bytes", constant.MaxRecordIDLength)), nil
	}
	if recordID != "" && len(regions) != 1 {
		return mcpgo.NewToolResultError("record_id requires exactly one region"), nil
	}
	hostnames, err := boundedStringArray(request.GetArguments(), constant.ArgumentHost, false, constant.MaxTargetValues, constant.MaxTargetValueLength)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	containerHostnames, err := boundedStringArray(request.GetArguments(), constant.ArgumentContainerHost, false, constant.MaxTargetValues, constant.MaxTargetValueLength)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	namespaces, err := boundedStringArray(request.GetArguments(), constant.ArgumentContainerNamespace, false, constant.MaxTargetValues, constant.MaxTargetValueLength)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	if len(hostnames) == 0 && len(containerHostnames) == 0 && len(namespaces) == 0 {
		return mcpgo.NewToolResultError("at least one of host, container_host or container_hostnamespace must be specified"), nil
	}
	requestedRange, err := request.RequireString(constant.ArgumentTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError("time_range is required"), nil
	}
	start, end, err := utils.ParseTimeRange(requestedRange, r.maxQueryTimeRange)
	if err != nil {
		return mcpgo.NewToolResultError(fmt.Sprintf("invalid time_range: %v", err)), nil
	}
	topN := clampEventInt(request.GetInt(constant.ArgumentTopN, constant.DefaultProfileTopN), constant.MaxProfileTopN, constant.DefaultProfileTopN)
	maxCallPaths := clampEventInt(request.GetInt(constant.ArgumentMaxCallPaths, constant.DefaultProfileCallPaths), constant.MaxProfileCallPaths, constant.DefaultProfileCallPaths)
	maxStackDepth := clampEventInt(request.GetInt(constant.ArgumentMaxStackDepth, constant.DefaultProfileStackDepth), constant.MaxProfileStackDepth, constant.DefaultProfileStackDepth)
	analysisType := request.GetString(constant.ArgumentAnalysisType, constant.ProfileAnalysisSelfTime)
	if analysisType != constant.ProfileAnalysisSelfTime && analysisType != constant.ProfileAnalysisTotalTime {
		return mcpgo.NewToolResultError("analysis_type must be self_time or total_time"), nil
	}
	quotas, err := distributeLimit(r.queryLimit(), regions)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	if recordID != "" {
		quotas[regions[0]] = 1
	}
	outcomes := fanOutRegions(ctx, regions, r.regionWorkers, func(ctx context.Context, region string) (tracingstore.SearchResult, error) {
		if quotas[region] == 0 {
			return tracingstore.SearchResult{}, nil
		}
		return r.searchProfilerDocuments(ctx, r.stores.forRegion(region), &tracingstore.Query{
			Region:                  region,
			Hostnames:               hostnames,
			ContainerHostnames:      containerHostnames,
			ContainerHostNamespaces: namespaces,
			TracerNames:             []string{tracer},
			RecordID:                recordID,
			StartTime:               start,
			EndTime:                 end,
			ProfileOnly:             true,
			Limit:                   quotas[region],
		})
	})
	documents := make([]*tracingstore.Document, 0, r.queryLimit())
	failures := make([]regionFailure, 0)
	var truncatedRegions []string
	statuses := make([]tracingstore.QueryStatus, 0, len(outcomes))
	warnings := false
	profileFailure := func(class, message string) *mcpgo.CallToolResult {
		observeToolErrorClass(ctx, class)
		return explicitToolErrorResult(&toolErrorEnvelope{
			Status: "error", ErrorClass: class, Message: boundedLogValue(message, constant.MaxBackendErrorBodyBytes),
			Suggestion: "Inspect query_status warnings and errors for backend/payload causes; no complete profile analysis was obtained.",
			Errors:     failures, QueryStatus: statuses,
		})
	}
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			failures = append(failures, newRegionFailure(r.stores.forRegion(outcome.Region).BackendType(), constant.ToolQueryProfiles, outcome.Region, outcome.Err))
			statuses = append(statuses, outcome.Value.Status(outcome.Region))
			continue
		}
		warnings = warnings || len(outcome.Value.Warnings) > 0
		if recordID != "" && outcome.Value.Truncated {
			statuses = append(statuses, outcome.Value.Status(outcome.Region))
			return profileFailure(constant.ToolErrorInvalidArgument, "record_id matches multiple profile documents; narrow the index or time range"), nil
		}
		if outcome.Value.Truncated || len(outcome.Value.Documents) > quotas[outcome.Region] {
			truncatedRegions = append(truncatedRegions, outcome.Region)
			outcome.Value.Documents = outcome.Value.Documents[:min(len(outcome.Value.Documents), quotas[outcome.Region])]
			outcome.Value.Truncated = true
		}
		statuses = append(statuses, outcome.Value.Status(outcome.Region))
		for _, document := range outcome.Value.Documents {
			if document == nil {
				documents = append(documents, nil)
				continue
			}
			documents = append(documents, tracingstore.RoutedDocument(document, r.stores.forRegion(outcome.Region).BackendType(), outcome.Region))
		}
	}
	if len(documents) == 0 && len(failures) == len(regions) {
		return backendFailuresResult(ctx, "ES profiler query failed", failures, statuses...), nil
	}
	if recordID != "" && len(documents) == 0 && len(failures) == 0 {
		return profileFailure(constant.ToolErrorInvalidArgument, "no analyzable profile matches record_id in the requested region, time range and target"), nil
	}
	result, err := r.analyzeProfilerDocuments(
		ctx, documents, regions, tracer,
		map[string][]string{
			constant.ArgumentHost:               hostnames,
			constant.ArgumentContainerHost:      containerHostnames,
			constant.ArgumentContainerNamespace: namespaces,
		},
		fmt.Sprintf("%s/%s", start.Format(time.RFC3339), end.Format(time.RFC3339)),
		analysisType, topN, maxCallPaths, maxStackDepth,
	)
	if err != nil {
		class := constant.ToolErrorInvalidArgument
		if errors.Is(err, errUnsupportedProfilePayload) {
			class = constant.ToolErrorUnsupportedPayload
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			class = classifyToolError(err)
		}
		return profileFailure(class, err.Error()), nil
	}
	result.Errors = failures
	result.QueryStatus = statuses
	result.Summary.Truncated = len(truncatedRegions) > 0
	result.Summary.TruncatedRegions = truncatedRegions
	result.Status = collectionStatus(result.Summary.Truncated || warnings || result.Summary.UnsupportedDocuments > 0, failures)
	if len(failures) > 0 {
		observeCollectionResult(ctx, len(result.TopFunctions), failures)
	} else if result.Summary.UnsupportedDocuments > 0 {
		observeToolResult(ctx, constant.ToolResultPartial)
		observeToolErrorClass(ctx, constant.ToolErrorUnsupportedPayload)
	} else if result.Summary.Truncated || warnings {
		observeToolResult(ctx, constant.ToolResultPartial)
	} else if len(result.TopFunctions) == 0 {
		observeToolResult(ctx, constant.ToolResultEmpty)
	}
	return indentedJSONResult(result)
}

func (r *eventTools) searchProfilerDocuments(
	ctx context.Context,
	store tracingstore.Reader,
	filter *tracingstore.Query,
) (tracingstore.SearchResult, error) {
	return store.SearchEvents(ctx, filter)
}

func (r *eventTools) analyzeProfilerDocuments(
	ctx context.Context,
	documents []*tracingstore.Document,
	regions []string,
	tracer string,
	targets map[string][]string,
	timeRange, analysisType string,
	topN, maxCallPaths, maxStackDepth int,
) (*profileAnalysisResult, error) {
	stats := make(map[string]*frameFunctionStat)
	var totalSamples int64
	analyzedFrames := 0
	aggregatedPaths := 0
	parsedDocuments := 0
	unsupportedDocuments := 0
	sampleType, sampleUnit := "", ""
	defer func() { observeProfileDocuments(ctx, parsedDocuments, unsupportedDocuments) }()
	sources := make([]profileSource, 0, len(documents))
	for _, document := range documents {
		if document == nil {
			unsupportedDocuments++
			continue
		}
		if len(document.ProfileFrames) == 0 {
			unsupportedDocuments++
			continue
		}
		if parsedDocuments == 0 {
			sampleType, sampleUnit = document.ProfileSampleType, document.ProfileSampleUnit
		} else if sampleType != document.ProfileSampleType || sampleUnit != document.ProfileSampleUnit {
			return nil, fmt.Errorf("profile documents use incompatible sample types/units; narrow the query time range")
		}
		parsedDocuments++
		if document.RecordID != "" || document.Source != nil {
			sources = append(sources, profileSource{
				Source:            document.Source,
				Region:            document.Region,
				TracerName:        document.TracerName,
				Hostname:          document.Hostname,
				ContainerHostname: document.ContainerHostname,
				Timestamp:         document.QueryTimestamp(),
			})
		}
		if err := aggregateFrames(
			ctx, document.ProfileFrames, stats, &totalSamples, &analyzedFrames, &aggregatedPaths,
		); err != nil {
			return nil, err
		}
	}
	if len(documents) > 0 && parsedDocuments == 0 {
		return nil, errUnsupportedProfilePayload
	}
	result, err := buildProfileAnalysis(
		ctx,
		stats,
		totalSamples,
		tracer,
		timeRange,
		analysisType,
		topN,
		maxCallPaths,
		maxStackDepth,
	)
	if err != nil {
		return nil, err
	}
	result.Regions = append([]string(nil), regions...)
	result.Targets = targets
	result.Sources = sources
	result.Summary.Documents = parsedDocuments
	result.Summary.SampleType, result.Summary.SampleUnit = sampleType, sampleUnit
	result.Summary.UnsupportedDocuments = unsupportedDocuments
	result.Summary.PayloadFormat = constant.ProfilePayloadFormat
	return result, nil
}

func aggregateFrames(
	ctx context.Context,
	frames []flamegraph.FrameData,
	stats map[string]*frameFunctionStat,
	totalSamples *int64,
	analyzedFrames *int,
	aggregatedPaths *int,
) error {
	stack := make([]string, 0, 16)
	for _, frame := range frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if frame.Label == "" {
			continue
		}
		*analyzedFrames++
		if *analyzedFrames > constant.MaxAnalysisFrames {
			return fmt.Errorf("profiler analysis exceeds %d frames; reduce query scope", constant.MaxAnalysisFrames)
		}
		level := max(int(frame.Level), 0)
		if level == 0 {
			*totalSamples += frame.Value
		}
		if level > len(stack) {
			level = len(stack)
		}
		stack = stack[:level]
		stack = append(stack, frame.Label)

		stat := stats[frame.Label]
		if stat == nil {
			if len(stats) >= constant.MaxAnalysisFunctions {
				return fmt.Errorf("profiler analysis exceeds %d functions; reduce query scope", constant.MaxAnalysisFunctions)
			}
			stat = &frameFunctionStat{name: frame.Label, leafPaths: make(map[string]*framePathStat)}
			stats[frame.Label] = stat
		}
		stat.selfValue += frame.Self
		stat.totalValue += frame.Value
		if frame.Self <= 0 && frame.Value <= 0 {
			continue
		}
		stat.sampleCount += max(frame.Self, 0)
		// Inclusive hotspots need caller context even when all samples are in
		// child functions. Self-time analysis filters these paths below.
		path := make([]string, min(len(stack), constant.MaxProfileStackDepth))
		for index := range path {
			path[index] = stack[len(stack)-1-index]
		}
		key := strings.Join(path, "\x00")
		pathStat := stat.leafPaths[key]
		if pathStat == nil {
			if *aggregatedPaths >= constant.MaxAnalysisCallPaths {
				return fmt.Errorf("profiler analysis exceeds %d call paths; reduce query scope", constant.MaxAnalysisCallPaths)
			}
			pathStat = &framePathStat{path: path}
			stat.leafPaths[key] = pathStat
			*aggregatedPaths++
		}
		pathStat.selfValue += frame.Self
		pathStat.totalValue += frame.Value
		if frame.Self > 0 {
			pathStat.selfPathTotal += frame.Value
		}
	}
	return nil
}

func buildProfileAnalysis(
	ctx context.Context,
	stats map[string]*frameFunctionStat,
	totalSamples int64,
	tracer, timeRange, analysisType string,
	topN, maxCallPaths, maxStackDepth int,
) (*profileAnalysisResult, error) {
	result := &profileAnalysisResult{
		TracerName:   tracer,
		TimeRange:    timeRange,
		AnalysisType: analysisType,
		TopFunctions: make([]hotspotFunction, 0, min(len(stats), topN)),
		Summary: profileSummary{
			TotalSamples:    totalSamples,
			UniqueFunctions: len(stats),
		},
	}
	sorted := make([]*frameFunctionStat, 0, len(stats))
	for _, stat := range stats {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if totalSamples > 0 {
			stat.selfPercent = float64(stat.selfValue) / float64(totalSamples) * 100
			stat.totalPercent = float64(stat.totalValue) / float64(totalSamples) * 100
		}
		sorted = append(sorted, stat)
	}
	if analysisType == constant.ProfileAnalysisSelfTime {
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].selfValue > sorted[j].selfValue })
	} else {
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].totalValue > sorted[j].totalValue })
	}
	for index, stat := range sorted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index >= topN {
			break
		}
		function := hotspotFunction{
			Rank:         index + 1,
			Name:         stat.name,
			SelfPercent:  stat.selfPercent,
			TotalPercent: stat.totalPercent,
			SampleCount:  stat.sampleCount,
			CallPaths:    make([]profileCallPath, 0, min(len(stat.leafPaths), maxCallPaths)),
		}
		mergedPaths := make(map[string]*framePathStat, len(stat.leafPaths))
		for _, path := range stat.leafPaths {
			if analysisType == constant.ProfileAnalysisSelfTime && path.selfValue <= 0 {
				continue
			}
			labels := path.path
			if len(labels) > maxStackDepth {
				labels = labels[:maxStackDepth]
			}
			key := strings.Join(labels, "\x00")
			merged := mergedPaths[key]
			if merged == nil {
				merged = &framePathStat{path: append([]string(nil), labels...)}
				mergedPaths[key] = merged
			}
			merged.selfValue += path.selfValue
			if analysisType == constant.ProfileAnalysisSelfTime {
				// Zero-self occurrences contribute only to inclusive analysis,
				// even when another document samples self time on the same path.
				merged.totalValue += path.selfPathTotal
			} else {
				merged.totalValue += path.totalValue
			}
		}
		paths := make([]*framePathStat, 0, len(mergedPaths))
		for _, path := range mergedPaths {
			paths = append(paths, path)
		}
		sort.Slice(paths, func(i, j int) bool { return paths[i].totalValue > paths[j].totalValue })
		for pathIndex, path := range paths {
			if pathIndex >= maxCallPaths {
				break
			}
			percentage := float64(0)
			if totalSamples > 0 {
				percentage = float64(path.totalValue) / float64(totalSamples) * 100
			}
			function.CallPaths = append(function.CallPaths, profileCallPath{
				Path:         append([]string(nil), path.path...),
				SelfSamples:  path.selfValue,
				TotalSamples: path.totalValue,
				Percentage:   percentage,
			})
		}
		result.TopFunctions = append(result.TopFunctions, function)
		result.Summary.Coverage += function.SelfPercent
	}
	return result, nil
}
