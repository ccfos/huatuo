// Copyright 2025 The HuaTuo Authors
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

package context

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/output"
	_ "huatuo-bamai/internal/profiler/output/flamegraph"
	_ "huatuo-bamai/internal/profiler/output/raw"
	psignal "huatuo-bamai/internal/profiler/signal"
	"huatuo-bamai/internal/profiler/strutil"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/tracing"

	"github.com/urfave/cli/v2"
)

type ProfilerContext struct {
	Ctx    context.Context
	Cancel context.CancelFunc
	Cli    *cli.Context

	PID          int
	Freq         int
	Duration     int
	ToolLimit    int
	AggrInterval int
	IsOneShotAgg bool

	ServerAddress string
	OutputFormat  output.OutputFormat
	OutputPath    string
	ContainerID   string
	Type          string
	Language      string
	ExecPath      string
	Scope         string
	ToolPath      string

	ExtraFlags      map[string]string
	MetaData        map[string]string
	CpuIdleMetaData map[string]int64
	CpuSysMetaData  map[string]int64

	DataSaver *storage.Store[*tracing.Document]
}

type TracerData struct {
	MetricData any                   `json:"metric_data,omitempty"`
	MetaData   any                   `json:"metadata,omitempty"`
	FlameData  *profiler.ProfileData `json:"flamedata"`
}

func NewProfilerContext(cliCtx *cli.Context, logBuf *bytes.Buffer) (*ProfilerContext, error) {
	ctx, cancel := context.WithCancel(cliCtx.Context)

	sigCh, err := psignal.SetupSignals()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to setup signals: %w", err)
	}

	go func() {
		sig, err := psignal.ListenSignalAndCancel(sigCh, cancel)
		if err != nil {
			fmt.Fprintf(logBuf, "[signal] error: %v\n", err)
		}
		fmt.Fprintf(logBuf, "[signal] caught signal: %s, canceling context\n", sig)
	}()

	flagsMap, err := parseExtraFlagsString(cliCtx.StringSlice("flags"))
	if err != nil {
		return nil, err
	}

	metaData, err := parseExtraFlagsString(cliCtx.StringSlice("metadata"))
	if err != nil {
		return nil, err
	}
	if mockContainer := cliCtx.String("mock-container"); mockContainer != "" {
		metaData["mock_container"] = mockContainer
	}

	cpuidleMeta, err := parseExtraFlagsInt64(cliCtx.StringSlice("cpuidle-metadata"))
	if err != nil {
		return nil, err
	}

	cpusysMeta, err := parseExtraFlagsInt64(cliCtx.StringSlice("cpusys-metadata"))
	if err != nil {
		return nil, err
	}

	outputFormat := output.OutputFormat(cliCtx.String("output-format"))

	dataSaver, err := initESStorage(cliCtx, outputFormat)
	if err != nil {
		return nil, err
	}

	return &ProfilerContext{
		Ctx:    ctx,
		Cancel: cancel,
		Cli:    cliCtx,

		PID:          cliCtx.Int("pid"),
		Freq:         cliCtx.Int("freq"),
		Duration:     cliCtx.Int("duration"),
		ToolLimit:    cliCtx.Int("tool-limit"),
		AggrInterval: cliCtx.Int("aggr-interval"),

		ServerAddress: cliCtx.String("server-address"),
		Type:          cliCtx.String("type"),
		Language:      cliCtx.String("language"),
		ContainerID:   cliCtx.String("container-id"),
		ExecPath:      cliCtx.String("exec-path"),
		Scope:         cliCtx.String("scope"),
		ToolPath:      cliCtx.String("tool-path"),
		OutputPath:    cliCtx.String("output-path"),
		OutputFormat:  outputFormat,

		MetaData:        metaData,
		ExtraFlags:      flagsMap,
		CpuSysMetaData:  cpusysMeta,
		CpuIdleMetaData: cpuidleMeta,

		DataSaver: dataSaver,
	}, nil
}

func MapToStructByJSON[T any](m map[string]int64) (T, error) {
	var meta T

	data, err := json.Marshal(m)
	if err != nil {
		return meta, err
	}

	err = json.Unmarshal(data, &meta)
	return meta, err
}

func splitStorageAddresses(raw string) []string {
	return strutil.SplitCommaList(raw)
}

type flagSegment struct {
	key   string
	value string
}

func parseFlagSegments(flagList []string) ([]flagSegment, error) {
	var segments []flagSegment

	for _, raw := range flagList {
		for _, segment := range strings.Split(raw, ",") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}

			clean := strings.TrimLeft(segment, "-")

			if strings.Contains(clean, "=") {
				parts := strings.SplitN(clean, "=", 2)
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				if key != "" {
					segments = append(segments, flagSegment{key: key, value: value})
				}

				continue
			}

			parts := strings.Fields(clean)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				if key != "" {
					segments = append(segments, flagSegment{key: key, value: value})
				}

				continue
			}

			return nil, fmt.Errorf("invalid extra flag format: %q (expected --key=value or --key value)", segment)
		}
	}

	return segments, nil
}

func parseExtraFlagsString(flagList []string) (map[string]string, error) {
	segments, err := parseFlagSegments(flagList)
	if err != nil {
		return nil, err
	}

	flags := make(map[string]string, len(segments))
	for _, s := range segments {
		flags[s.key] = s.value
	}

	return flags, nil
}

func parseExtraFlagsInt64(flagList []string) (map[string]int64, error) {
	segments, err := parseFlagSegments(flagList)
	if err != nil {
		return nil, err
	}

	flags := make(map[string]int64, len(segments))
	for _, s := range segments {
		iValue, err := strconv.Atoi(s.value)
		if err != nil {
			return nil, fmt.Errorf("invalid int64 flag value for %q: %w", s.key, err)
		}

		flags[s.key] = int64(iValue)
	}

	return flags, nil
}

func initESStorage(cliCtx *cli.Context, format output.OutputFormat) (*storage.Store[*tracing.Document], error) {
	if format != output.FormatES {
		return nil, nil
	}

	store, err := storage.NewFromConfig[*tracing.Document](
		context.Background(),
		&driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: splitStorageAddresses(cliCtx.String("es-address")),
			ESUsername:  cliCtx.String("es-username"),
			ESPassword:  cliCtx.String("es-password"),
			ESIndex:     cliCtx.String("es-index"),
		},
		profiler.ProfilingDocumentMapper{},
	)
	if err != nil {
		return nil, fmt.Errorf("storage.New(profiling metadata): %w", err)
	}

	return store, nil
}
