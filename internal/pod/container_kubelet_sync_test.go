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

package pod

import (
	"net/http"
	"testing"
)

func TestKubeletConfigCacheUpdateReturnsErrorWhenConfigUnavailable(t *testing.T) {
	oldPaths := kubeletDefaultConfigPath
	oldClient := kubeletPodListClient
	defer func() {
		kubeletDefaultConfigPath = oldPaths
		kubeletPodListClient = oldClient
	}()

	kubeletDefaultConfigPath = []string{t.TempDir() + "/missing.yaml"}
	kubeletPodListClient = &http.Client{}

	err := kubeletConfigCacheUpdate(&ManagerCtx{PodAuthorizedPort: 1})
	if err == nil {
		t.Fatal("kubeletConfigCacheUpdate() error = nil, want non-nil")
	}
}
