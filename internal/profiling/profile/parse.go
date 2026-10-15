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

package profile

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
)

// parseMultiProcessData parses profiles of multiple PIDs from SampleOutput.
func parseMultiProcessData(ctx context.Context, input *ParseInput, outputs []SampleOutput, prependProcess bool) (*Data, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var tree []*TreeItem

	for _, out := range outputs {
		if out.Output == "" {
			continue
		}
		lines := bytes.Split([]byte(out.Output), []byte("\n"))

		var headerFrame []byte
		if prependProcess {
			name, ok := input.ProcessNames[out.PID]
			if !ok {
				return nil, fmt.Errorf("missing process name for PID %d", out.PID)
			}
			headerFrame = []byte(fmt.Sprintf("process %d:%s", out.PID, name))
		}

		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			// Find the last space character to separate the call stack from the sample value
			i := bytes.LastIndexByte(line, ' ')
			if i < 0 {
				continue // Skip malformed lines
			}

			stackPart := line[:i]
			valuePart := line[i+1:]

			// Parse the sample count
			value, err := strconv.ParseUint(string(valuePart), 10, 64)
			if err != nil {
				continue
			}
			// Split the stack trace into individual frames
			frames := bytes.Split(stackPart, []byte(";"))

			// Calculate the stack capacity
			// Java: need an additional header frame for process name
			capacity := len(frames)
			if len(headerFrame) > 0 {
				capacity++
			}

			item := &TreeItem{
				Stack: make([][]byte, 0, capacity),
				Value: value,
			}

			if len(headerFrame) > 0 {
				item.Stack = append(item.Stack, headerFrame)
			}

			// Append each frame in order
			for _, frame := range frames {
				frame = bytes.TrimSpace(frame)
				if len(frame) > 0 {
					item.Stack = append(item.Stack, frame)
				}
			}

			tree = append(tree, item)
		}
	}

	return ParseTree(input.StartTime, input.ProfileType, tree, input.Opt)
}

// parseCommonData parses profile of single PID from raw data.
func parseCommonData(ctx context.Context, input *ParseInput) (*Data, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	lines := bytes.Split(input.Data, []byte("\n"))
	tree := make([]*TreeItem, 0, len(lines))

	threadName, ok := input.ProcessNames[input.PID]
	if !ok {
		return nil, fmt.Errorf("missing process name for PID %d", input.PID)
	}

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		// Find the last space character to separate the call stack from the sample value
		i := bytes.LastIndexByte(line, ' ')
		if i < 0 {
			continue // Skip malformed lines
		}

		stackPart := line[:i]
		valuePart := line[i+1:]

		// Parse the sample count
		value, err := strconv.ParseUint(string(valuePart), 10, 64)
		if err != nil {
			continue // Skip lines with invalid sample values
		}

		// Split the stack trace into individual frames
		frames := bytes.Split(stackPart, []byte(";"))

		item := &TreeItem{
			Stack: make([][]byte, 0, len(frames)+1), // +1 for threadName#pid
			Value: value,
		}

		// Format: threadName#pid
		firstField := fmt.Sprintf("%s#%d", threadName, input.PID)
		item.Stack = append(item.Stack, []byte(firstField))

		// Append each frame in order
		for _, frame := range frames {
			frame = bytes.TrimSpace(frame)
			if len(frame) > 0 {
				item.Stack = append(item.Stack, frame)
			}
		}

		tree = append(tree, item)
	}

	// Convert the parsed stack data to a Data structure
	return ParseTree(input.StartTime, input.ProfileType, tree, input.Opt)
}
