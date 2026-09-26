// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/asciimoo/hister/server/testutil"
)

var galleryTestPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func galleryTestHash() string {
	sum := sha256.Sum256(galleryTestPNG)
	return fmt.Sprintf("%x", sum)
}

func galleryTestURI() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(galleryTestPNG)
}

var galleryAuthHeaders = map[string]string{
	"Content-Type":   "application/json",
	"X-Access-Token": "secret",
	"Origin":         chromeExtensionOrigin,
}

func submitGalleryPage(t *testing.T, handler http.Handler, metadata string) {
	t.Helper()
	body := fmt.Sprintf(`{"url":"https://example.com/gallery","title":"Gallery","text":"gallery page","metadata":%s}`,
		metadata)
	rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/add", strings.NewReader(body), galleryAuthHeaders)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/add status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestGalleryUploadNeededRoundTrip(t *testing.T) {
	_, handler := newTokenTestServer(t, false)
	hash := galleryTestHash()
	manifest, _ := json.Marshal([]map[string]string{{"alt": "Client", "hash": hash}})
	submitGalleryPage(t, handler, fmt.Sprintf(`{"images":%s}`, string(manifest)))

	needed := func(hashes ...string) []string {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"hashes": hashes})
		rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/image/needed", strings.NewReader(string(payload)), galleryAuthHeaders)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /api/image/needed status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Needed []string `json:"needed"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("needed response is not JSON: %v", err)
		}
		return out.Needed
	}

	if got := needed(hash); len(got) != 1 || got[0] != hash {
		t.Fatalf("needed before upload = %v, want [%s]", got, hash)
	}

	upload, _ := json.Marshal(map[string]string{
		"url": "https://example.com/gallery", "alt": "Client", "hash": hash, "data_uri": galleryTestURI(),
	})
	rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/image", strings.NewReader(string(upload)), galleryAuthHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/image status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var uploaded struct {
		Key    string `json:"key"`
		Merged bool   `json:"merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("upload response is not JSON: %v", err)
	}
	if uploaded.Key != hash || !uploaded.Merged {
		t.Errorf("upload = %+v, want key %s merged", uploaded, hash)
	}
	if got := needed(hash, strings.Repeat("0", 64)); len(got) != 1 || got[0] != strings.Repeat("0", 64) {
		t.Errorf("needed after upload = %v, want only the unknown hash", got)
	}

	rec = testutil.ServeHTTP(t, handler, http.MethodGet, "/api/image?key="+hash, nil, galleryAuthHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/image status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type = %q, want image/png", ct)
	}
	if rec.Body.String() != string(galleryTestPNG) {
		t.Error("served bytes differ from uploaded bytes")
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache control = %q, want immutable", cc)
	}

	// The manifest hash must now resolve to a keyed entry in search results.
	rec = testutil.ServeHTTP(t, handler, http.MethodGet, "/search?format=json&q=gallery", nil, map[string]string{"Accept": "application/json", "X-Access-Token": "secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var results struct {
		Documents []struct {
			URL      string         `json:"url"`
			Metadata map[string]any `json:"metadata"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("search response is not JSON: %v", err)
	}
	if len(results.Documents) == 0 {
		t.Fatal("search returned no documents")
	}
	manifestJSON, _ := results.Documents[0].Metadata["images"].(string)
	var entries []map[string]string
	if err := json.Unmarshal([]byte(manifestJSON), &entries); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if len(entries) != 1 || entries[0]["key"] != hash || entries[0]["hash"] != "" {
		t.Errorf("manifest not resolved to key: %v", entries)
	}
}

func TestGalleryUploadWithoutDocument(t *testing.T) {
	_, handler := newTokenTestServer(t, false)
	upload, _ := json.Marshal(map[string]string{
		"url": "https://example.com/unknown", "alt": "A", "hash": galleryTestHash(), "data_uri": galleryTestURI(),
	})
	rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/image", strings.NewReader(string(upload)), galleryAuthHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/image status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var uploaded struct {
		Key    string `json:"key"`
		Merged bool   `json:"merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("upload response is not JSON: %v", err)
	}
	if uploaded.Key != galleryTestHash() || uploaded.Merged {
		t.Errorf("upload = %+v, want key kept and merged=false", uploaded)
	}
}

func TestGalleryEndpointsRejectBadInput(t *testing.T) {
	_, handler := newTokenTestServer(t, false)
	upload := func(body string) int {
		rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/image", strings.NewReader(body), galleryAuthHeaders)
		return rec.Code
	}
	if got := upload(`{"url":"","data_uri":"x"}`); got != http.StatusBadRequest {
		t.Errorf("missing url status = %d, want 400", got)
	}
	if got := upload(`{"url":"https://example.com/","data_uri":"https://example.com/a.jpg"}`); got != http.StatusBadRequest {
		t.Errorf("remote URL status = %d, want 400", got)
	}
	if got := upload(`{"url":"https://example.com/","data_uri":"data:image/png;base64,!!!"}`); got == http.StatusOK {
		t.Errorf("invalid base64 status = %d, want non-200", got)
	}
	textURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("hello"))
	if got := upload(fmt.Sprintf(`{"url":"https://example.com/","data_uri":%q}`, textURI)); got != http.StatusUnsupportedMediaType {
		t.Errorf("non-image status = %d, want 415", got)
	}

	rec := testutil.ServeHTTP(t, handler, http.MethodGet, "/api/image?key=bogus", nil, galleryAuthHeaders)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed key status = %d, want 400", rec.Code)
	}
	rec = testutil.ServeHTTP(t, handler, http.MethodGet, "/api/image?key="+strings.Repeat("0", 64), nil, galleryAuthHeaders)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown key status = %d, want 404", rec.Code)
	}
}
