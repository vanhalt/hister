// SPDX-License-Identifier: AGPL-3.0-or-later

package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asciimoo/hister/server/extractor/sdk"
)

// tinyPNG is a 1x1 transparent PNG.
var tinyPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func TestMatch(t *testing.T) {
	e := &ImagesExtractor{}
	if e.Match(&sdk.Document{HTML: "<html><body>no pictures</body></html>"}) {
		t.Error("Match should be false without <img>")
	}
	if !e.Match(&sdk.Document{HTML: `<html><body><IMG src="a.jpg"></body></html>`}) {
		t.Error("Match should be case-insensitive")
	}
}

func TestCollectImageCandidates(t *testing.T) {
	html := `<html><head><meta property="og:image" content="/og.jpg"></head><body>
		<img src="/a.jpg" alt="A">
		<img src="/a.jpg" alt="A duplicate">
		<img src="data:image/png;base64,xx" alt="inline">
		<img src="/favicon.ico" alt="icon">
		<img src="/pixel.gif" width="1" height="1" alt="tracker">
		<img data-src="/lazy.jpg" alt="Lazy">
		<img srcset="/r1.jpg 1x, /r2.jpg 2x" alt="Responsive">
		<picture><source srcset="/p1.webp 1x, /p2.webp 2x"></picture>
	</body></html>`
	got := collectImageCandidates(html, "https://example.com/page", 20)
	urls := make([]string, 0, len(got))
	for _, c := range got {
		urls = append(urls, c.url)
		for _, needle := range []string{"data:", "favicon", "pixel"} {
			if strings.Contains(c.url, needle) {
				t.Errorf("candidate %q should have been filtered", c.url)
			}
		}
	}
	for _, want := range []string{
		"https://example.com/a.jpg",
		"https://example.com/lazy.jpg",
		"https://example.com/r1.jpg",
		"https://example.com/p1.webp",
		"https://example.com/og.jpg",
	} {
		found := false
		for _, u := range urls {
			if u == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected candidate %q in %v", want, urls)
		}
	}
	if len(got) != 5 {
		t.Errorf("expected 5 deduped candidates, got %d: %v", len(got), urls)
	}
	if got[0].alt != "A" {
		t.Errorf("expected alt %q, got %q", "A", got[0].alt)
	}
}

func TestExtractDownloadsBase64(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if r.URL.Path == "/missing.jpg" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/text.txt" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("not an image"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	html := fmt.Sprintf(`<html><body>
		<img src="%s/one.jpg" alt="One">
		<img src="%s/missing.jpg" alt="Missing">
		<img src="%s/text.txt" alt="Text">
	</body></html>`, srv.URL, srv.URL, srv.URL)
	d := &sdk.Document{URL: "https://example.com/page", Domain: "example.com", HTML: html}
	e := &ImagesExtractor{}
	if res := e.ExtractContext(context.Background(), d); res.Decision() != sdk.ExtractorSuccess {
		t.Fatalf("Extract decision = %v, err = %v", res.Decision(), res.Err())
	}
	raw, ok := d.Metadata["images"].(string)
	if !ok || raw == "" {
		t.Fatal("expected d.Metadata[images] JSON string")
	}
	var entries []imageEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 downloaded image, got %d", len(entries))
	}
	if !strings.HasPrefix(entries[0].DataURI, "data:image/png;base64,") {
		t.Errorf("expected png data URI, got %.40q", entries[0].DataURI)
	}
	if entries[0].Alt != "One" {
		t.Errorf("expected alt %q, got %q", "One", entries[0].Alt)
	}
	if strings.Contains(raw, srv.URL) {
		t.Error("metadata must not contain remote image URLs")
	}
	if n, _ := d.Metadata["image_count"].(int); n != 1 {
		t.Errorf("expected image_count 1, got %v", d.Metadata["image_count"])
	}
}

func TestExtractNoImagesFallback(t *testing.T) {
	e := &ImagesExtractor{}
	d := &sdk.Document{URL: "https://example.com/", HTML: "<html><body>text only</body></html>"}
	if res := e.Extract(d); res.Decision() != sdk.ExtractorFallback {
		t.Errorf("expected fallback, got %v", res.Decision())
	}
}

func TestPreviewGallery(t *testing.T) {
	e := &ImagesExtractor{}
	d := &sdk.Document{URL: "https://example.com/", Domain: "example.com", HTML: "<html></html>"}
	if res := e.Preview(d); res.Decision() != sdk.ExtractorFallback {
		t.Fatalf("expected fallback without metadata, got %v", res.Decision())
	}
	raw, _ := json.Marshal([]imageEntry{{
		Alt:     `A <script>alert(1)</script>`,
		DataURI: "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG),
	}})
	d.Metadata = map[string]any{"images": string(raw)}
	res := e.Preview(d)
	if res.Decision() != sdk.ExtractorSuccess {
		t.Fatalf("expected success, got %v (%v)", res.Decision(), res.Err())
	}
	content := res.Response().Content
	if strings.Contains(content, "<script>") {
		t.Error("preview must sanitize alt text")
	}
	if !strings.Contains(content, "data:image/png;base64,") {
		t.Error("preview must embed the data URI image")
	}
}

func TestSetConfigRejectsUnknownOption(t *testing.T) {
	e := &ImagesExtractor{}
	if err := e.SetConfig(&sdk.Config{Enable: true, Options: map[string]any{"bogus": 1}}); err == nil {
		t.Error("expected error for unknown option")
	}
	if err := e.SetConfig(&sdk.Config{Enable: true, Options: map[string]any{"max_images": 5}}); err != nil {
		t.Errorf("valid option rejected: %v", err)
	}
}
