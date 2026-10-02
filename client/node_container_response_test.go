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

package client

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestParseNodeContainerResponseRejectsMismatchedID(t *testing.T) {
	response := nodeJSONResponse(
		http.StatusOK,
		fmt.Sprintf(
			`{"data":{"id":%q,"cgroup_css":{}}}`,
			strings.Repeat("b", 64),
		),
	)
	defer response.Body.Close()

	_, err := parseNodeContainerResponse(response, nodeTestContainerID)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("parseNodeContainerResponse() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != NodeErrorCodeProtocol {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}
