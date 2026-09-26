// SPDX-License-Identifier: AGPL-3.0-or-later

// Package images downloads the image gallery of a web page and stores it as
// base64 data URIs in document metadata. It is an enrichment-only extractor:
// it never selects the body text, it only appends a JSON document to
// d.Metadata["images"]. No remote image URLs are persisted; every stored
// image is a downloaded data URI.
package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/rs/zerolog/log"

	"github.com/asciimoo/hister/server/extractor/sdk"
	"github.com/asciimoo/hister/server/sanitizer"
)

// ImagesExtractor scans HTML for image galleries and stores downloaded images
// in d.Metadata["images"] as a JSON string.
type ImagesExtractor struct {
	cfg *sdk.Config
}

var _ sdk.Extractor = (*ImagesExtractor)(nil)

func (e *ImagesExtractor) Name() string { return "Images" }

func (e *ImagesExtractor) Description() string {
	return "Downloads the image gallery of a web page and stores images as base64 data URIs in document metadata."
}

func (e *ImagesExtractor) Capabilities() sdk.Capabilities {
	return sdk.Capabilities{Enrich: true, Preview: true}
}

func defaultOptions() map[string]any {
	return map[string]any{
		"download_images": true,
		"image_timeout":   10,
		"max_image_bytes": 1024 * 1024,
		"max_images":      20,
	}
}

func (e *ImagesExtractor) GetConfig() *sdk.Config {
	if e.cfg == nil {
		return &sdk.Config{Enable: true, Options: defaultOptions()}
	}
	return e.cfg
}

func (e *ImagesExtractor) SetConfig(c *sdk.Config) error {
	for k := range c.Options {
		switch k {
		case "download_images", "image_timeout", "max_image_bytes", "max_images":
		default:
			return fmt.Errorf("unknown option %q", k)
		}
	}
	e.cfg = c
	return nil
}

func (e *ImagesExtractor) downloadEnabled() bool {
	v, ok := e.GetConfig().Options["download_images"].(bool)
	return !ok || v
}

func intOption(options map[string]any, key string, def int) int {
	switch v := options[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case int64:
		return int(v)
	}
	return def
}

func (e *ImagesExtractor) imageTimeout() time.Duration {
	return time.Duration(intOption(e.GetConfig().Options, "image_timeout", 10)) * time.Second
}

func (e *ImagesExtractor) maxImageBytes() int {
	return intOption(e.GetConfig().Options, "max_image_bytes", 1024*1024)
}

func (e *ImagesExtractor) maxImages() int {
	if n := intOption(e.GetConfig().Options, "max_images", 20); n > 0 {
		return n
	}
	return 20
}

// imageEntry is a single downloaded gallery image. Only the base64 data URI
// is persisted; the remote source URL is deliberately dropped so no remote
// URLs leak into the index.
type imageEntry struct {
	Alt     string `json:"alt,omitempty"`
	DataURI string `json:"data_uri"`
}

// Match returns true when the raw HTML plausibly contains an image gallery.
func (e *ImagesExtractor) Match(d *sdk.Document) bool {
	if len(d.HTML) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(d.HTML), "<img")
}

// Extract downloads gallery images and writes them to metadata.
func (e *ImagesExtractor) Extract(d *sdk.Document) sdk.ExtractResult {
	return e.ExtractContext(context.Background(), d)
}

// ExtractContext collects image URLs, downloads them, and stores data URIs.
func (e *ImagesExtractor) ExtractContext(ctx context.Context, d *sdk.Document) sdk.ExtractResult {
	if err := ctx.Err(); err != nil {
		return sdk.AbortExtraction(err)
	}
	if !e.downloadEnabled() {
		return sdk.ExtractFallback(fmt.Errorf("image downloads disabled"))
	}
	candidates := collectImageCandidates(d.HTML, d.URL, e.maxImages())
	if len(candidates) == 0 {
		return sdk.ExtractFallback(fmt.Errorf("no images found"))
	}
	downloaded := e.downloadImages(ctx, candidates)
	if len(downloaded) == 0 {
		return sdk.ExtractFallback(fmt.Errorf("no images downloaded"))
	}
	raw, err := json.Marshal(downloaded)
	if err != nil {
		return sdk.ExtractFallback(err)
	}
	d.AddMetadata("images", string(raw))
	d.AddMetadata("image_count", len(downloaded))
	return sdk.Extracted()
}

// imageCandidate is a resolved image URL with its alt text.
type imageCandidate struct {
	url string
	alt string
}

// collectImageCandidates parses img/picture/og:image tags, resolves URLs
// against the page URL, dedupes, and drops icons, tracking pixels, and
// non-downloadable references. No network access is performed here.
func collectImageCandidates(pageHTML, pageURL string, limit int) []imageCandidate {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if err != nil {
		return nil
	}
	base, _ := url.Parse(pageURL)
	seen := make(map[string]struct{})
	var out []imageCandidate
	add := func(raw, alt string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "blob:") {
			return
		}
		// First srcset entry only; descriptors ("2x", "800w") are stripped.
		if fields := strings.Fields(raw); len(fields) > 0 {
			raw = fields[0]
		}
		if base != nil {
			if u, err := url.Parse(raw); err == nil {
				raw = base.ResolveReference(u).String()
			}
		}
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			return
		}
		lower := strings.ToLower(raw)
		// Drop obvious non-gallery assets.
		for _, marker := range []string{"favicon", "sprite", "pixel", "tracking", "1x1", ".svg"} {
			if strings.Contains(lower, marker) {
				return
			}
		}
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, imageCandidate{url: raw, alt: strings.TrimSpace(alt)})
	}
	doc.Find("img").Each(func(_ int, s *goquery.Selection) {
		alt := s.AttrOr("alt", "")
		// Skip 1x1 tracking pixels declared via attributes.
		if (s.AttrOr("width", "") == "1" && s.AttrOr("height", "") == "1") ||
			s.AttrOr("width", "") == "1x1" {
			return
		}
		if src := s.AttrOr("src", ""); src != "" {
			add(src, alt)
		} else if src := s.AttrOr("data-src", ""); src != "" {
			add(src, alt)
		}
		if srcset := s.AttrOr("srcset", ""); srcset != "" {
			add(firstSrcsetURL(srcset), alt)
		}
	})
	doc.Find("picture source[srcset]").Each(func(_ int, s *goquery.Selection) {
		add(firstSrcsetURL(s.AttrOr("srcset", "")), "")
	})
	doc.Find(`meta[property="og:image"]`).Each(func(_ int, s *goquery.Selection) {
		if content := s.AttrOr("content", ""); content != "" {
			add(content, "")
		}
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// firstSrcsetURL returns the URL of the first entry in a srcset attribute.
func firstSrcsetURL(srcset string) string {
	for _, part := range strings.Split(srcset, ",") {
		if fields := strings.Fields(strings.TrimSpace(part)); len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

// downloadImages fetches candidates and returns base64 data URIs.
// Failures are skipped; an empty result is not fatal to the caller chain.
func (e *ImagesExtractor) downloadImages(ctx context.Context, candidates []imageCandidate) []imageEntry {
	maxBytes := e.maxImageBytes()
	cli := &http.Client{Timeout: e.imageTimeout()}
	out := make([]imageEntry, 0, len(candidates))
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return out
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
		if err != nil {
			continue
		}
		resp, err := cli.Do(req)
		if err != nil {
			continue
		}
		data, contentType, ok := readImageBody(resp, maxBytes)
		if !ok {
			continue
		}
		out = append(out, imageEntry{
			Alt:     c.alt,
			DataURI: fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(data)),
		})
	}
	return out
}

func readImageBody(resp *http.Response, maxBytes int) ([]byte, string, bool) {
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Warn().Err(cerr).Msg("failed to close gallery image response body")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil || len(data) == 0 || len(data) > maxBytes {
		return nil, "", false
	}
	sniffed := http.DetectContentType(data)
	if !strings.HasPrefix(sniffed, "image/") {
		return nil, "", false
	}
	return data, sniffed, true
}

// galleryEntries decodes d.Metadata["images"] into entries.
func galleryEntries(d *sdk.Document) []imageEntry {
	raw, ok := d.Metadata["images"].(string)
	if !ok || raw == "" {
		return nil
	}
	var entries []imageEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil
	}
	filtered := entries[:0]
	for _, img := range entries {
		if strings.HasPrefix(img.DataURI, "data:image/") {
			filtered = append(filtered, img)
		}
	}
	return filtered
}

// Preview renders a sanitized gallery grid of the stored data URIs.
func (e *ImagesExtractor) Preview(d *sdk.Document) sdk.PreviewResult {
	return e.PreviewContext(context.Background(), d)
}

// PreviewContext renders the gallery with caller cancellation.
func (e *ImagesExtractor) PreviewContext(ctx context.Context, d *sdk.Document) sdk.PreviewResult {
	if err := ctx.Err(); err != nil {
		return sdk.AbortPreview(err)
	}
	entries := galleryEntries(d)
	if len(entries) == 0 {
		return sdk.PreviewFallback(fmt.Errorf("no gallery images"))
	}
	var b strings.Builder
	b.WriteString(`<div class="hister-gallery">`)
	for _, img := range entries {
		b.WriteString(`<figure class="hister-gallery-item"><img src="`)
		b.WriteString(img.DataURI)
		b.WriteString(`" alt="`)
		b.WriteString(stdhtml.EscapeString(img.Alt))
		b.WriteString(`" loading="lazy">`)
		if img.Alt != "" {
			b.WriteString(`<figcaption>`)
			b.WriteString(stdhtml.EscapeString(img.Alt))
			b.WriteString(`</figcaption>`)
		}
		b.WriteString(`</figure>`)
	}
	b.WriteString(`</div>`)
	return sdk.Previewed(sdk.PreviewResponse{Content: sanitizer.SanitizeHTML(b.String())})
}
