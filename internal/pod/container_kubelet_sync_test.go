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
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	"huatuo-bamai/internal/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestHTTPDoRequestPropagatesBodyReadError reproduces issue #258: when a kubelet
// response body fails to read, the error must be surfaced to the caller rather
// than being swallowed and replaced with an empty body.
func TestHTTPDoRequestPropagatesBodyReadError(t *testing.T) {
	wantReadErr := errors.New("simulated mid-stream read failure")
	client := &http.Client{
		Transport: bodyReadErrorTransport{readErr: wantReadErr},
	}
	requestURL := "http://test.kubelet.invalid/pods"

	_, err := httpDoRequest(client, requestURL)
	if err == nil {
		t.Fatal("httpDoRequest() error = nil, want non-nil")
	}
	if !errors.Is(err, wantReadErr) {
		t.Errorf("errors.Is(httpDoRequest(%q) error, wantReadErr) = false, want true; error = %v", requestURL, err)
	}
	wantError := fmt.Sprintf("http: %s, read body: %v", requestURL, wantReadErr)
	if err.Error() != wantError {
		t.Errorf("httpDoRequest(%q) error = %q, want %q", requestURL, err, wantError)
	}
}

// TestHTTPDoRequestReturnsBodyOnSuccess confirms the happy path still works
// after the fix, guarding against regressions that drop the body.
func TestHTTPDoRequestReturnsBodyOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	requestURL := srv.URL + "/pods"
	body, err := httpDoRequest(srv.Client(), requestURL)
	if err != nil {
		t.Fatalf("httpDoRequest() error = %v, want nil", err)
	}
	wantBody := `{"ok":true}`
	if string(body) != wantBody {
		t.Errorf("httpDoRequest(%q) body = %q, want %q", requestURL, body, wantBody)
	}
}

// TestHTTPDoRequestReportsNonOKStatusAndBody makes sure the existing
// non-200 error path is unaffected by the read-error fix.
func TestHTTPDoRequestReportsNonOKStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `boom`)
	}))
	t.Cleanup(srv.Close)

	requestURL := srv.URL + "/pods"
	_, err := httpDoRequest(srv.Client(), requestURL)
	if err == nil {
		t.Fatal("httpDoRequest() error = nil, want non-nil")
	}
	wantError := fmt.Sprintf("http: %s, status: %d, body: boom", requestURL, http.StatusInternalServerError)
	if err.Error() != wantError {
		t.Errorf("httpDoRequest(%q) error = %q, want %q", requestURL, err, wantError)
	}
}

// bodyReadErrorTransport returns a response whose body read fails after a
// partial payload.
type bodyReadErrorTransport struct {
	readErr error
}

type kubeletRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kubeletRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	reader io.Reader
	read   int
	closed bool
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestHTTPDoRequestRejectsDeclaredOversizedBodyWithoutReading(t *testing.T) {
	const limit = int64(64)
	body := &trackingReadCloser{reader: strings.NewReader(strings.Repeat("x", 65))}
	client := &http.Client{Transport: kubeletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: maxKubeletResponseBodyBytes + 1,
			Header:        make(http.Header),
			Body:          body,
		}, nil
	})}

	_, err := httpDoRequest(client, "http://test.kubelet.invalid/pods")
	if err == nil || !strings.Contains(err.Error(), "declares 134217729 bytes, limit is 134217728 bytes") {
		t.Fatalf("httpDoRequest() error=%v, want declared-size rejection", err)
	}
	if body.read != 0 {
		t.Fatalf("body bytes read=%d, want 0", body.read)
	}
	if !body.closed {
		t.Fatal("response body was not closed after declared-size rejection")
	}
}

func TestHTTPDoRequestBoundsUnknownLengthBody(t *testing.T) {
	const limit = int64(64)
	body := &trackingReadCloser{reader: strings.NewReader(strings.Repeat("x", 128))}
	_, truncated, err := requestLimitedBody(body, limit)
	if err != nil || !truncated {
		t.Fatalf("requestLimitedBody() err=%v truncated=%v, want overflow", err, truncated)
	}
	if body.read != int(limit+1) {
		t.Fatalf("body bytes read=%d, want %d", body.read, limit+1)
	}
}

func TestHTTPDoRequestAcceptsExactLimit(t *testing.T) {
	const limit = int64(64)
	want := strings.Repeat("x", int(limit))
	got, truncated, err := requestLimitedBody(strings.NewReader(want), limit)
	if err != nil {
		t.Fatalf("requestLimitedBody() error=%v", err)
	}
	if truncated {
		t.Fatal("requestLimitedBody() reported truncation at exact limit")
	}
	if string(got) != want {
		t.Fatalf("body length=%d, want %d", len(got), len(want))
	}
}

func TestHTTPDoRequestBoundsChunkedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, strings.Repeat("x", 65))
	}))
	t.Cleanup(srv.Close)

	_, truncated, err := requestLimitedBody(strings.NewReader(strings.Repeat("x", 65)), 64)
	if err != nil || !truncated {
		t.Fatalf("requestLimitedBody() err=%v truncated=%v, want overflow", err, truncated)
	}
}

func TestHTTPDoRequestBoundsDecompressedBody(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(strings.Repeat("x", 128))); err != nil {
		t.Fatalf("gzip.Write() error=%v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip.Close() error=%v", err)
	}
	if compressed.Len() >= 64 {
		t.Fatalf("compressed body=%d bytes, want below test limit", compressed.Len())
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(compressed.Len()))
		_, _ = w.Write(compressed.Bytes())
	}))
	t.Cleanup(srv.Close)

	reader, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader() error=%v", err)
	}
	_, truncated, err := requestLimitedBody(reader, 64)
	if err != nil || !truncated {
		t.Fatalf("requestLimitedBody() err=%v truncated=%v, want overflow", err, truncated)
	}
}

func TestHTTPDoRequestTruncatesNonOKBody(t *testing.T) {
	body := &trackingReadCloser{
		reader: strings.NewReader(strings.Repeat("x", maxKubeletErrorBodyBytes*2)),
	}
	client := &http.Client{Transport: kubeletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusInternalServerError,
			ContentLength: -1,
			Header:        make(http.Header),
			Body:          body,
		}, nil
	})}

	_, err := httpDoRequest(client, "http://test.kubelet.invalid/pods")
	if err == nil {
		t.Fatal("httpDoRequest() error=nil, want non-OK response")
	}
	if !strings.Contains(err.Error(), "[truncated after 8192 bytes]") {
		t.Fatalf("httpDoRequest() error=%q, want truncation marker", err)
	}
	if body.read != maxKubeletErrorBodyBytes+1 {
		t.Fatalf("body bytes read=%d, want %d", body.read, maxKubeletErrorBodyBytes+1)
	}
	if len(err.Error()) > maxKubeletErrorBodyBytes+256 {
		t.Fatalf("error length=%d, want bounded diagnostic", len(err.Error()))
	}
}

func TestKubeletDecodersBoundMalformedBodyDiagnostics(t *testing.T) {
	malformed := "[" + strings.Repeat("x", maxKubeletErrorBodyBytes+1024)
	client := &http.Client{Transport: kubeletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: int64(len(malformed)),
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader(malformed)),
		}, nil
	})}

	_, podErr := kubeletPodListDoRequest(client, "http://test.kubelet.invalid/pods")
	if podErr == nil || !strings.Contains(podErr.Error(), "[truncated after 8192 bytes]") {
		t.Fatalf("kubeletPodListDoRequest() error=%v, want bounded diagnostic", podErr)
	}
	_, configErr := kubeletConfigDoRequest(client, "http://test.kubelet.invalid/configz")
	if configErr == nil || !strings.Contains(configErr.Error(), "[truncated after 8192 bytes]") {
		t.Fatalf("kubeletConfigDoRequest() error=%v, want bounded diagnostic", configErr)
	}
	for name, err := range map[string]error{"pods": podErr, "configz": configErr} {
		if len(err.Error()) > maxKubeletErrorBodyBytes+512 {
			t.Errorf("%s error length=%d, want bounded diagnostic", name, len(err.Error()))
		}
	}
}

func TestHTTPDoRequestWarnsWhenResponseExceedsLimit(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })
	body := &trackingReadCloser{reader: strings.NewReader("unused")}
	client := &http.Client{Transport: kubeletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: maxKubeletResponseBodyBytes + 1,
			Header:        make(http.Header),
			Body:          body,
		}, nil
	})}
	requestURL := "http://test.kubelet.invalid/pods"
	_, err := httpDoRequest(client, requestURL)
	if err == nil {
		t.Fatal("httpDoRequest() error=nil, want size rejection")
	}
	output := logs.String()
	for _, want := range []string{
		"level=\"warning\"",
		"rejecting oversized kubelet response",
		"url=\"" + requestURL + "\"",
		"limit_bytes=\"134217728\"",
		"declared_size_bytes=\"134217729\"",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("warning log=%q, want field %q", output, want)
		}
	}
}

func TestKubeletResponseLimitCoversDenseNodeStressProfiles(t *testing.T) {
	const podsPerNode = 400
	tests := []struct {
		name string
		pod  corev1.Pod
	}{
		{
			name: "annotation-heavy workload",
			pod: denseNodeProofPod(
				4,
				1,
				12,
				8,
				12,
				1024,
				256<<10,
			),
		},
		{
			name: "spec-and-status-heavy workload",
			pod: denseNodeProofPod(
				16,
				4,
				32,
				32,
				32,
				4096,
				16<<10,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responseBytes := repeatedPodListJSONSize(t, &test.pod, podsPerNode)
			t.Logf(
				"400-pod response=%d bytes, headroom=%d bytes",
				responseBytes,
				maxKubeletResponseBodyBytes-responseBytes,
			)
			if responseBytes > maxKubeletResponseBodyBytes {
				t.Errorf(
					"400-pod response=%d bytes, limit=%d bytes",
					responseBytes,
					maxKubeletResponseBodyBytes,
				)
			}
		})
	}
}

func denseNodeProofPod(
	containers int,
	initContainers int,
	envPerContainer int,
	volumes int,
	labels int,
	statusMessageBytes int,
	annotationBytes int,
) corev1.Pod {
	annotationKey := "example.com/payload"
	pod := corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dense-node-pod-0000",
			Namespace: "default",
			UID:       "12345678-1234-1234-1234-123456789012",
			Labels:    make(map[string]string, labels),
			Annotations: map[string]string{
				annotationKey: strings.Repeat(
					"a",
					annotationBytes-len(annotationKey),
				),
			},
		},
		Spec: corev1.PodSpec{
			NodeName:       "dense-node",
			Containers:     make([]corev1.Container, containers),
			InitContainers: make([]corev1.Container, initContainers),
			Volumes:        make([]corev1.Volume, volumes),
		},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			PodIP:             "10.0.0.1",
			ContainerStatuses: make([]corev1.ContainerStatus, containers),
		},
	}
	for index := 0; index < labels; index++ {
		pod.Labels[fmt.Sprintf("example.com/label-%02d", index)] = strings.Repeat("l", 48)
	}

	for container := range pod.Spec.Containers {
		env := make([]corev1.EnvVar, envPerContainer)
		mounts := make([]corev1.VolumeMount, volumes)
		for index := range env {
			env[index] = corev1.EnvVar{
				Name:  fmt.Sprintf("CONFIG_%03d", index),
				Value: strings.Repeat("v", 128),
			}
		}
		for index := range mounts {
			mounts[index] = corev1.VolumeMount{
				Name:      fmt.Sprintf("volume-%02d", index),
				MountPath: fmt.Sprintf("/var/lib/app/%02d", index),
			}
		}

		name := fmt.Sprintf("container-%02d", container)
		pod.Spec.Containers[container] = corev1.Container{
			Name: name,
			Image: "registry.example.com/team/application@sha256:" +
				strings.Repeat("a", 64),
			Command:      []string{"/bin/application", "--config=/etc/application/config.yaml"},
			Args:         []string{"--log-format=json", "--metrics=:9090"},
			Env:          env,
			VolumeMounts: mounts,
		}
		pod.Status.ContainerStatuses[container] = corev1.ContainerStatus{
			Name:         name,
			Ready:        true,
			RestartCount: 3,
			Image:        pod.Spec.Containers[container].Image,
			ImageID:      "containerd://sha256:" + strings.Repeat("b", 64),
			ContainerID:  "containerd://" + strings.Repeat("c", 64),
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1,
					Reason:   "Error",
					Message:  strings.Repeat("m", statusMessageBytes),
				},
			},
		}
	}
	for index := range pod.Spec.InitContainers {
		pod.Spec.InitContainers[index] = pod.Spec.Containers[index%containers]
	}
	for index := range pod.Spec.Volumes {
		pod.Spec.Volumes[index] = corev1.Volume{
			Name: fmt.Sprintf("volume-%02d", index),
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}
	}

	return pod
}

func repeatedPodListJSONSize(t *testing.T, pod *corev1.Pod, count int) int64 {
	t.Helper()
	podJSON, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("json.Marshal(pod) error=%v", err)
	}
	onePodListJSON, err := json.Marshal(corev1.PodList{Items: []corev1.Pod{*pod}})
	if err != nil {
		t.Fatalf("json.Marshal(pod list) error=%v", err)
	}

	return int64(len(onePodListJSON) + (count-1)*(len(podJSON)+1))
}

func (t bodyReadErrorTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(io.MultiReader(
			strings.NewReader(`{"partial":`),
			iotest.ErrReader(t.readErr),
		)),
	}, nil
}
