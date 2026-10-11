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

package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ccfos/huatuo/internal/profiler"
	profileroutput "github.com/ccfos/huatuo/internal/profiler/output"
	_ "github.com/ccfos/huatuo/internal/profiler/output/flamegraph"
	_ "github.com/ccfos/huatuo/internal/profiler/output/raw"
	"github.com/ccfos/huatuo/internal/toolstream"
)

type irqTracingSnapshot struct {
	result *IRQTracingResult
	stacks []*profiler.TreeItem
}

type writer interface {
	Write(snapshot *irqTracingSnapshot) error
}

type formatterWriter struct {
	output    io.Writer
	formatter profileroutput.Formatter
}

func (w *formatterWriter) Write(snapshot *irqTracingSnapshot) error {
	for _, item := range snapshot.stacks {
		frames := make([]string, len(item.Stack))
		for i, frame := range item.Stack {
			frames[i] = string(frame)
		}
		if err := w.formatter.Add(&profileroutput.Sample{
			Frames: frames,
			Count:  int64(item.Value),
		}); err != nil {
			return fmt.Errorf("add profiling sample: %w", err)
		}
	}
	return w.formatter.Write(w.output)
}

type jsonWriter struct{ output io.Writer }

func (w *jsonWriter) Write(snapshot *irqTracingSnapshot) error {
	data, err := json.Marshal(snapshot.result)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.output.Write(data)
	return err
}

type socketWriter struct{ client *toolstream.Client }

func (w *socketWriter) Write(snapshot *irqTracingSnapshot) error {
	return w.client.Send(snapshot.result)
}

func newWriter(destination io.Writer, outputFormat string, client *toolstream.Client) (writer, error) {
	if client != nil {
		return &socketWriter{client: client}, nil
	}
	if outputFormat == outputJSON {
		return &jsonWriter{output: destination}, nil
	}

	format := profileroutput.OutputFormat(outputFormat)
	if outputFormat == outputText {
		format = profileroutput.FormatCollapsed
	}
	formatter, err := format.NewFormatter()
	if err != nil {
		return nil, err
	}
	return &formatterWriter{output: destination, formatter: formatter}, nil
}
