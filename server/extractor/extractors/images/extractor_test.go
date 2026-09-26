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
	"time"

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
	if res.Response().Template != "gallery" {
		t.Errorf("template = %q, want gallery", res.Response().Template)
	}
	var items []galleryPreviewItem
	if err := json.Unmarshal([]byte(res.Response().Content), &items); err != nil {
		t.Fatalf("preview content is not JSON: %v", err)
	}
	if len(items) != 1 || !strings.HasPrefix(items[0].DataURI, "data:image/png;base64,") {
		t.Errorf("unexpected preview items: %+v", items)
	}
}

func TestPreviewGalleryKeys(t *testing.T) {
	e := &ImagesExtractor{}
	key := strings.Repeat("a", 64)
	hash := strings.Repeat("b", 64)
	raw, _ := json.Marshal([]imageEntry{
		{Alt: "Stored", Key: key},
		{Alt: "Pending", Hash: hash},
		{Alt: "Bogus", Key: "not-a-key"},
	})
	d := &sdk.Document{
		URL:      "https://example.com/",
		Domain:   "example.com",
		Metadata: map[string]any{"images": string(raw)},
	}
	res := e.Preview(d)
	if res.Decision() != sdk.ExtractorSuccess {
		t.Fatalf("expected success, got %v (%v)", res.Decision(), res.Err())
	}
	var items []galleryPreviewItem
	if err := json.Unmarshal([]byte(res.Response().Content), &items); err != nil {
		t.Fatalf("preview content is not JSON: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected key and hash entries, got %+v", items)
	}
	if items[0].Key != key || items[1].Hash != hash {
		t.Errorf("unexpected preview items: %+v", items)
	}
}

func TestClientGalleryAcceptsKeysAndHashes(t *testing.T) {
	e := &ImagesExtractor{}
	key := strings.Repeat("a", 64)
	hash := strings.Repeat("b", 64)
	raw, _ := json.Marshal([]imageEntry{
		{Alt: "Stored", Key: key},
		{Alt: "Pending", Hash: hash},
		{Alt: "Bogus", Key: "xyz", Hash: "xyz"},
	})
	d := &sdk.Document{
		URL:      "https://example.com/",
		Domain:   "example.com",
		HTML:     "<html><body>no images here</body></html>",
		Metadata: map[string]any{"images": string(raw)},
	}
	if res := e.Extract(d); res.Decision() != sdk.ExtractorSuccess {
		t.Fatalf("expected success, got %v (%v)", res.Decision(), res.Err())
	}
	stored, _ := d.Metadata["images"].(string)
	var entries []imageEntry
	if err := json.Unmarshal([]byte(stored), &entries); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}
	if len(entries) != 2 || entries[0].Key != key || entries[1].Hash != hash {
		t.Errorf("unexpected normalized gallery: %+v", entries)
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
	if err := e.SetConfig(&sdk.Config{Enable: true, Options: map[string]any{"max_concurrent_downloads": 2}}); err != nil {
		t.Errorf("valid option rejected: %v", err)
	}
}

func clientGalleryDoc(entries []imageEntry) *sdk.Document {
	raw, _ := json.Marshal(entries)
	return &sdk.Document{
		URL:      "https://example.com/page",
		Domain:   "example.com",
		HTML:     `<html><body><img src="https://example.com/a.jpg" alt="A"></body></html>`,
		Metadata: map[string]any{"images": string(raw)},
	}
}

func TestExtractPrefersClientGalleryWithoutDownloads(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	d := clientGalleryDoc([]imageEntry{{Alt: "Client image", DataURI: uri}})
	// Point the HTML at the test server: any download would be counted.
	d.HTML = fmt.Sprintf(`<html><body><img src="%s/a.jpg" alt="A"></body></html>`, srv.URL)
	e := &ImagesExtractor{}
	if res := e.ExtractContext(context.Background(), d); res.Decision() != sdk.ExtractorSuccess {
		t.Fatalf("Extract decision = %v, err = %v", res.Decision(), res.Err())
	}
	if requests != 0 {
		t.Errorf("client gallery should skip downloads, got %d requests", requests)
	}
	raw, _ := d.Metadata["images"].(string)
	var entries []imageEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].Alt != "Client image" {
		t.Errorf("client gallery not preserved: %+v", entries)
	}
	if n, _ := d.Metadata["image_count"].(int); n != 1 {
		t.Errorf("expected image_count 1, got %v", d.Metadata["image_count"])
	}
}

func TestExtractRejectsInvalidClientGallery(t *testing.T) {
	cases := map[string]string{
		"remote URL":     `[{"alt":"x","data_uri":"https://example.com/a.jpg"}]`,
		"not base64":     `[{"alt":"x","data_uri":"data:image/png;base64,!!!"}]`,
		"not an image":   `[{"alt":"x","data_uri":"data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"}]`,
		"malformed JSON": `not json`,
		"empty array":    `[]`,
	}
	for name, raw := range cases {
		d := &sdk.Document{
			URL:      "https://example.com/page",
			Domain:   "example.com",
			HTML:     "<html><body>text only, no img tags</body></html>",
			Metadata: map[string]any{"images": raw},
		}
		e := &ImagesExtractor{}
		if res := e.Extract(d); res.Decision() != sdk.ExtractorFallback {
			t.Errorf("%s: expected fallback, got %v", name, res.Decision())
		}
	}
}

func TestNoImageCountCapByDefault(t *testing.T) {
	e := &ImagesExtractor{}
	if e.maxImages() != 0 {
		t.Fatalf("default max_images = %d, want 0 (unlimited)", e.maxImages())
	}
	var sb strings.Builder
	sb.WriteString("<html><body>")
	for i := range 25 {
		fmt.Fprintf(&sb, `<img src="/img%d.jpg" alt="I%d">`, i, i)
	}
	sb.WriteString("</body></html>")
	if got := collectImageCandidates(sb.String(), "https://example.com/", e.maxImages()); len(got) != 25 {
		t.Fatalf("expected 25 candidates without cap, got %d", len(got))
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	entries := make([]imageEntry, 0, 25)
	for i := range 25 {
		entries = append(entries, imageEntry{Alt: fmt.Sprintf("I%d", i), DataURI: uri})
	}
	raw, _ := json.Marshal(entries)
	d := &sdk.Document{
		URL:      "https://example.com/page",
		Domain:   "example.com",
		Metadata: map[string]any{"images": string(raw)},
	}
	if res := e.ExtractContext(context.Background(), d); res.Decision() != sdk.ExtractorSuccess {
		t.Fatalf("Extract decision = %v, err = %v", res.Decision(), res.Err())
	}
	if n, _ := d.Metadata["image_count"].(int); n != 25 {
		t.Errorf("expected image_count 25, got %v", d.Metadata["image_count"])
	}
}

func TestDownloadImagesConcurrentPreservesOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	const n = 8
	candidates := make([]imageCandidate, 0, n)
	for i := range n {
		candidates = append(candidates, imageCandidate{
			url: fmt.Sprintf("%s/img%d.jpg", srv.URL, i),
			alt: fmt.Sprintf("Image %d", i),
		})
	}
	e := &ImagesExtractor{}
	start := time.Now()
	got := e.downloadImages(context.Background(), candidates)
	elapsed := time.Since(start)
	if len(got) != n {
		t.Fatalf("expected %d images, got %d", n, len(got))
	}
	for i, entry := range got {
		if entry.Alt != fmt.Sprintf("Image %d", i) {
			t.Fatalf("order not preserved at %d: %+v", i, entry)
		}
	}
	// Sequential would take n*150ms = 1.2s; six concurrent workers finish in ~2 waves.
	if elapsed >= 1100*time.Millisecond {
		t.Errorf("downloads look sequential: %d images took %v", n, elapsed)
	}
}
