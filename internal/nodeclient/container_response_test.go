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

package nodeclient

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestParseContainerResponseRejectsMismatchedID(t *testing.T) {
	response := jsonResponse(
		http.StatusOK,
		fmt.Sprintf(
			`{"data":{"id":%q,"cgroup_css":{}}}`,
			strings.Repeat("b", 64),
		),
	)
	defer response.Body.Close()

	_, err := parseContainerResponse(response, testContainerID)
	var nodeErr *Error
	if !errors.As(err, &nodeErr) {
		t.Fatalf("parseContainerResponse() error = %v, want *Error", err)
	}
	if nodeErr.Code != ErrorCodeClientProtocol {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}
