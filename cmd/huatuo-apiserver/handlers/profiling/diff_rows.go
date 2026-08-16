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

package profiling

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"huatuo-bamai/internal/log"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"github.com/prometheus/prometheus/model/labels"
)

const (
	diffFlamegraphNodeWidth     = 7
	maxProfileDiffTargetLength  = 512
	maxProfileDiffTypeLength    = 256
	maxProfileDiffResponseBytes = 8 << 20
)

type profileDiffRowsRequest struct {
	ProfileTypeID  string `json:"profile_type_id"`
	Hostname       string `json:"hostname"`
	ContainerID    string `json:"container_id"`
	ProfilingScope string `json:"profiling_scope"`
	CPU            string `json:"cpu"`
	PID            string `json:"pid"`
	TGID           string `json:"tgid"`
	Start          int64  `json:"start"`
	End            int64  `json:"end"`
	MaxNodes       int64  `json:"max_nodes"`
}

type profileDiffRow struct {
	Level      int    `json:"level"`
	Value      int64  `json:"value"`
	Self       int64  `json:"self"`
	ValueRight int64  `json:"valueRight"`
	SelfRight  int64  `json:"selfRight"`
	Label      string `json:"label"`
}

type profileDiffNode struct {
	row        profileDiffRow
	leftStart  int64
	rightStart int64
	children   []*profileDiffNode
}

func (h *Handler) displayDiffRows(ctx *server.Context) error {
	var request profileDiffRowsRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		return response.ErrInvalidRequest.WithMessage(
			"invalid profile diff request",
		)
	}

	diffRequest, err := buildAdjacentProfileDiffRequest(&request)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}
	diffResponse, err := h.profileService.Diff(
		ctx.Request().Context(),
		diffRequest,
	)
	if err != nil {
		status, message := profileQueryHTTPError(err)
		if status == http.StatusInternalServerError {
			log.WithError(err).WithField(
				"operation",
				"display_diff_rows",
			).Error("profile query failed")
		}
		ctx.JSON(status, map[string]any{"message": message})
		return nil
	}

	rows, err := profileDiffRows(diffResponse.GetFlamegraph())
	if err != nil {
		log.WithError(err).Error("invalid profile diff response")
		ctx.JSON(
			http.StatusInternalServerError,
			map[string]any{"message": "internal error"},
		)
		return nil
	}
	tooLarge, err := profileDiffResponseExceedsLimit(rows)
	if err != nil {
		log.WithError(err).Error("encode profile diff response")
		ctx.JSON(
			http.StatusInternalServerError,
			map[string]any{"message": "internal error"},
		)
		return nil
	}
	if tooLarge {
		ctx.JSON(http.StatusUnprocessableEntity, map[string]any{
			"message": fmt.Sprintf(
				"profile diff response exceeds %d bytes",
				maxProfileDiffResponseBytes,
			),
		})
		return nil
	}
	response.Success(ctx, rows)
	return nil
}

func profileDiffResponseExceedsLimit(rows []profileDiffRow) (bool, error) {
	payload, err := json.Marshal(response.Response{
		Code:    0,
		Message: "success",
		Data:    rows,
	})
	if err != nil {
		return false, err
	}
	return len(payload) > maxProfileDiffResponseBytes, nil
}

func buildAdjacentProfileDiffRequest(
	request *profileDiffRowsRequest,
) (*querierv1.DiffRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("request is required")
	}
	if request.ProfileTypeID == "" {
		return nil, fmt.Errorf("profile_type_id is required")
	}
	if len(request.ProfileTypeID) > maxProfileDiffTypeLength {
		return nil, fmt.Errorf(
			"profile_type_id must not exceed %d bytes",
			maxProfileDiffTypeLength,
		)
	}
	if request.Start < 0 || request.End <= request.Start {
		return nil, fmt.Errorf("end must be later than a non-negative start")
	}
	window := request.End - request.Start
	if window > request.Start {
		return nil, fmt.Errorf("selected range has no preceding window")
	}
	if request.MaxNodes < 0 ||
		request.MaxNodes > profileService.MaxProfileNodes {
		return nil, fmt.Errorf(
			"max_nodes must be between 0 and %d",
			profileService.MaxProfileNodes,
		)
	}

	targetName := "hostname"
	targetValue := request.Hostname
	if request.ContainerID != "" {
		targetName = "container_id"
		targetValue = request.ContainerID
	}
	if strings.TrimSpace(targetValue) == "" {
		return nil, fmt.Errorf("hostname or container_id is required")
	}
	selectors := []struct {
		name  string
		value string
	}{
		{name: targetName, value: targetValue},
		{name: "profiling_scope", value: request.ProfilingScope},
		{name: "cpu", value: request.CPU},
		{name: "pid", value: request.PID},
		{name: "tgid", value: request.TGID},
	}
	matchers := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if selector.value == "" {
			continue
		}
		if len(selector.value) > maxProfileDiffTargetLength {
			return nil, fmt.Errorf(
				"%s must not exceed %d bytes",
				selector.name,
				maxProfileDiffTargetLength,
			)
		}
		matcher, err := labels.NewMatcher(
			labels.MatchEqual,
			selector.name,
			selector.value,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"build %s selector: %w",
				selector.name,
				err,
			)
		}
		matchers = append(matchers, matcher.String())
	}
	selector := "{" + strings.Join(matchers, ",") + "}"

	maxNodesValue := request.MaxNodes
	if maxNodesValue == 0 {
		maxNodesValue = profileService.DefaultProfileMaxNodes
	}
	maxNodes := &maxNodesValue
	return &querierv1.DiffRequest{
		Left: &querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: request.ProfileTypeID,
			LabelSelector: selector,
			Start:         request.Start - window,
			End:           request.Start - 1,
			MaxNodes:      maxNodes,
		},
		Right: &querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: request.ProfileTypeID,
			LabelSelector: selector,
			Start:         request.Start,
			End:           request.End,
			MaxNodes:      maxNodes,
		},
	}, nil
}

func profileDiffRows(graph *querierv1.FlameGraphDiff) ([]profileDiffRow, error) {
	if graph == nil || len(graph.Levels) == 0 {
		return []profileDiffRow{}, nil
	}

	levels := make([][]*profileDiffNode, len(graph.Levels))
	nodeCount := 0
	for levelIndex, level := range graph.Levels {
		if level == nil ||
			len(level.Values)%diffFlamegraphNodeWidth != 0 {
			return nil, fmt.Errorf(
				"invalid diff flame graph level %d",
				levelIndex,
			)
		}
		nodeCount += len(level.Values) / diffFlamegraphNodeWidth
		if nodeCount > int(profileService.MaxProfileNodes) {
			return nil, fmt.Errorf(
				"diff flame graph exceeds %d nodes",
				profileService.MaxProfileNodes,
			)
		}
		nodes := make(
			[]*profileDiffNode,
			0,
			len(level.Values)/diffFlamegraphNodeWidth,
		)
		var leftOffset int64
		var rightOffset int64
		for index := 0; index < len(level.Values); index += diffFlamegraphNodeWidth {
			leftStart, ok := addProfileDiffValue(
				leftOffset,
				level.Values[index],
			)
			if !ok {
				return nil, fmt.Errorf(
					"invalid left offset in diff flame graph level %d",
					levelIndex,
				)
			}
			rightStart, ok := addProfileDiffValue(
				rightOffset,
				level.Values[index+3],
			)
			if !ok {
				return nil, fmt.Errorf(
					"invalid right offset in diff flame graph level %d",
					levelIndex,
				)
			}
			nameIndex := level.Values[index+6]
			if nameIndex < 0 || nameIndex >= int64(len(graph.Names)) {
				return nil, fmt.Errorf(
					"invalid diff flame graph name index %d",
					nameIndex,
				)
			}
			node := &profileDiffNode{
				row: profileDiffRow{
					Level:      levelIndex,
					Value:      level.Values[index+1],
					Self:       level.Values[index+2],
					ValueRight: level.Values[index+4],
					SelfRight:  level.Values[index+5],
					Label:      graph.Names[nameIndex],
				},
				leftStart:  leftStart,
				rightStart: rightStart,
			}
			if node.row.Value < 0 ||
				node.row.Self < 0 ||
				node.row.ValueRight < 0 ||
				node.row.SelfRight < 0 ||
				node.row.Self > node.row.Value ||
				node.row.SelfRight > node.row.ValueRight {
				return nil, fmt.Errorf(
					"invalid values in diff flame graph level %d",
					levelIndex,
				)
			}
			nodes = append(nodes, node)
			leftOffset, ok = addProfileDiffValue(leftStart, node.row.Value)
			if !ok {
				return nil, fmt.Errorf(
					"left range overflows in diff flame graph level %d",
					levelIndex,
				)
			}
			rightOffset, ok = addProfileDiffValue(
				rightStart,
				node.row.ValueRight,
			)
			if !ok {
				return nil, fmt.Errorf(
					"right range overflows in diff flame graph level %d",
					levelIndex,
				)
			}
		}
		levels[levelIndex] = nodes
	}
	if len(levels[0]) != 1 {
		return nil, fmt.Errorf("diff flame graph must have one root")
	}

	for levelIndex := 1; levelIndex < len(levels); levelIndex++ {
		for _, node := range levels[levelIndex] {
			parent := profileDiffParent(levels[levelIndex-1], node)
			if parent == nil {
				return nil, fmt.Errorf(
					"diff flame graph level %d node %q has no parent",
					levelIndex,
					node.row.Label,
				)
			}
			parent.children = append(parent.children, node)
		}
	}

	rows := make([]profileDiffRow, 0, nodeCount)
	var appendNode func(*profileDiffNode)
	appendNode = func(node *profileDiffNode) {
		rows = append(rows, node.row)
		for _, child := range node.children {
			appendNode(child)
		}
	}
	appendNode(levels[0][0])
	return rows, nil
}

func profileDiffParent(
	parents []*profileDiffNode,
	child *profileDiffNode,
) *profileDiffNode {
	// Delta decoding starts each interval at or after the preceding interval's
	// end, so the last start not after the child is its only possible parent.
	leftIndex := sort.Search(len(parents), func(index int) bool {
		return parents[index].leftStart > child.leftStart
	})
	if leftIndex > 0 {
		parent := parents[leftIndex-1]
		if profileDiffRangeContains(
			parent.leftStart,
			parent.row.Value,
			child.leftStart,
			child.row.Value,
		) {
			return parent
		}
	}

	rightIndex := sort.Search(len(parents), func(index int) bool {
		return parents[index].rightStart > child.rightStart
	})
	if rightIndex > 0 {
		parent := parents[rightIndex-1]
		if profileDiffRangeContains(
			parent.rightStart,
			parent.row.ValueRight,
			child.rightStart,
			child.row.ValueRight,
		) {
			return parent
		}
	}
	return nil
}

func profileDiffRangeContains(
	parentStart, parentValue, childStart, childValue int64,
) bool {
	parentEnd, parentOK := addProfileDiffValue(parentStart, parentValue)
	childEnd, childOK := addProfileDiffValue(childStart, childValue)
	return childValue > 0 &&
		parentValue >= childValue &&
		parentOK &&
		childOK &&
		childStart >= parentStart &&
		childEnd <= parentEnd
}

func addProfileDiffValue(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}
