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
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyPortFailureWithoutAuthorizedPortSkipsHTTPS(t *testing.T) {
	// A token is present, so only the missing authorized port can stop the HTTPS fallback.
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := serviceAccountTokenPath
	serviceAccountTokenPath = tokenPath
	t.Cleanup(func() { serviceAccountTokenPath = old })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	err = kubeletPodListPortCacheUpdate(context.Background(), &ManagerCtx{PodReadOnlyPort: port})
	if err == nil {
		t.Fatal("expected the read-only port error")
	}
	if !strings.HasPrefix(err.Error(), "podlist http:") || strings.Contains(err.Error(), "https://") {
		t.Fatalf("err = %v, want the read-only port error without an HTTPS attempt", err)
	}
}
