package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const manifestTestPodYAML = `apiVersion: v1
kind: Pod
metadata:
  name: manifest-pod
spec:
  containers:
    - name: main
      image: alpine:latest
      command: ["sh", "-c", "sleep 3600"]
`

// TestGetPodManifest covers issue #80 quick win 1: an operator recovering
// from a state-divergence incident needs to recreate a pod faithfully,
// which requires the exact manifest that was submitted, not a
// reconstruction of it. It exercises the real POST /api/v1/pods flow (never
// store.Apply directly) and asserts the GET response is byte-equivalent
// (modulo whitespace) to the original POST body -- not a JSON-wrapped
// reserialization of the parsed spec.
func TestGetPodManifest(t *testing.T) {
	srv, _, _ := newMutateTestServer(t)

	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader(manifestTestPodYAML))
	applyRec := httptest.NewRecorder()
	srv.ServeHTTP(applyRec, applyReq)
	if applyRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 applying pod, got %d: %s", applyRec.Code, applyRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/manifest-pod/manifest", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got := strings.TrimSpace(rec.Body.String())
	want := strings.TrimSpace(manifestTestPodYAML)
	if got != want {
		t.Errorf("manifest not byte-equivalent to the POSTed body:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestGetPodManifestJSON covers the JSON submission path (issue #74):
// the returned manifest must match whatever content-type was originally
// POSTed, not always re-render as YAML or a structured envelope.
func TestGetPodManifestJSON(t *testing.T) {
	srv, _, _ := newMutateTestServer(t)

	body := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"manifest-pod-json"},"spec":{"containers":[{"name":"main","image":"alpine:latest"}]}}`
	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader(body))
	applyReq.Header.Set("Content-Type", "application/json")
	applyRec := httptest.NewRecorder()
	srv.ServeHTTP(applyRec, applyReq)
	if applyRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 applying pod, got %d: %s", applyRec.Code, applyRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/manifest-pod-json/manifest", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != strings.TrimSpace(body) {
		t.Errorf("manifest not byte-equivalent to the POSTed JSON body:\ngot:  %q\nwant: %q", got, body)
	}
}

func TestGetPodManifestNotFound(t *testing.T) {
	srv, _, _ := newMutateTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/nonexistent/manifest", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "pod not found: nonexistent" {
		t.Errorf("expected error message, got %q", body["error"])
	}
}
