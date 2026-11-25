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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestKubeletTokenClientReReadsRotatedToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	old := serviceAccountTokenPath
	serviceAccountTokenPath = tokenPath
	t.Cleanup(func() { serviceAccountTokenPath = old })

	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	if _, err := kubeletAuthorizedClient(&ManagerCtx{}); err == nil {
		t.Fatal("built a token client without a token file")
	}
	if err := os.WriteFile(tokenPath, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := kubeletAuthorizedClient(&ManagerCtx{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kubeletFetchPodList(context.Background(), client, srv.URL+"/pods"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := kubeletFetchPodList(context.Background(), client, srv.URL+"/pods"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "Bearer first" || seen[1] != "Bearer second" {
		t.Fatalf("authorization headers = %q", seen)
	}
}

func TestInitManagerRequiresCertOrToken(t *testing.T) {
	old := serviceAccountTokenPath
	serviceAccountTokenPath = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { serviceAccountTokenPath = old })

	if err := InitManager(&ManagerCtx{PodAuthorizedPort: 10250}); err == nil {
		ReleaseManager()
		t.Fatal("started without a client certificate or token")
	}
}

func TestKubeletTokenClientDoesNotFollowRedirects(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("test-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := serviceAccountTokenPath
	serviceAccountTokenPath = tokenPath
	t.Cleanup(func() { serviceAccountTokenPath = old })

	for _, tc := range []struct {
		name      string
		newTarget func(http.Handler) *httptest.Server
	}{
		{name: "https_to_http", newTarget: httptest.NewServer},
		{name: "https_to_https", newTarget: httptest.NewTLSServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			target := tc.newTarget(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()

			src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/leak", http.StatusFound)
			}))
			defer src.Close()

			client, err := kubeletAuthorizedClient(&ManagerCtx{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = kubeletFetchPodList(context.Background(), client, src.URL+"/pods")
			if err == nil || !strings.Contains(err.Error(), "status: 302") {
				t.Fatalf("err = %v, want HTTP 302 error", err)
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("redirect target received %d request(s), want 0", got)
			}
		})
	}
}
