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
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/rs/zerolog/log"

	"github.com/asciimoo/hister/server/extractor/sdk"
)

// ImagesExtractor scans HTML for image galleries and stores downloaded images
// in d.Metadata["images"] as a JSON string.
type ImagesExtractor struct {
	cfg *sdk.Config
}

var _ sdk.Extractor = (*ImagesExtractor)(nil)

func (e *ImagesExtractor) Name() string { return "Images" }

func (e *ImagesExtractor) Description() string {
	return "Downloads the image gallery of a web page and stores images in the content-addressed image store, referenced by key from document metadata."
}

func (e *ImagesExtractor) Capabilities() sdk.Capabilities {
	return sdk.Capabilities{Enrich: true, Preview: true}
}

func defaultOptions() map[string]any {
	return map[string]any{
		"download_images":          true,
		"image_timeout":            10,
		"max_image_bytes":          0,
		"max_images":               0,
		"max_concurrent_downloads": 6,
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
		case "download_images", "image_timeout", "max_image_bytes", "max_images",
			"max_concurrent_downloads":
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

// maxImageBytes returns the per-image download cap. Zero means uncapped;
// readImageBody always applies a hard 1 GiB safety ceiling so a hostile
// origin cannot exhaust memory regardless of configuration.
func (e *ImagesExtractor) maxImageBytes() int {
	return intOption(e.GetConfig().Options, "max_image_bytes", 0)
}

func (e *ImagesExtractor) maxImages() int {
	// Zero or negative means no cap: every gallery image is indexed.
	return intOption(e.GetConfig().Options, "max_images", 0)
}

func (e *ImagesExtractor) maxConcurrentDownloads() int {
	if n := intOption(e.GetConfig().Options, "max_concurrent_downloads", 6); n > 0 {
		return n
	}
	return 6
}

// sharedTransport is reused across documents so connections to hosts that
// serve many gallery images (or many indexed pages) stay warm.
var sharedTransport = &http.Transport{
	MaxIdleConns:        64,
	MaxIdleConnsPerHost: 16,
	IdleConnTimeout:     90 * time.Second,
}

// imageEntry is a single gallery image. Key addresses bytes in the
// content-addressed image store, Hash is a not-yet-uploaded client image,
// and DataURI is a legacy or freshly submitted inline image converted to a
// key before indexing. The remote source URL is deliberately never
// persisted.
type imageEntry struct {
	Alt     string `json:"alt,omitempty"`
	Key     string `json:"key,omitempty"`
	Hash    string `json:"hash,omitempty"`
	DataURI string `json:"data_uri,omitempty"`
}

// validEntryKey reports whether s looks like a SHA-256 content key or hash.
func validEntryKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
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

// ExtractContext prefers a client-supplied gallery (e.g. images already
// downloaded by the browser extension and submitted with the document) and
// falls back to downloading the gallery itself.
func (e *ImagesExtractor) ExtractContext(ctx context.Context, d *sdk.Document) sdk.ExtractResult {
	if err := ctx.Err(); err != nil {
		return sdk.AbortExtraction(err)
	}
	if entries := e.clientGallery(d); len(entries) > 0 {
		raw, err := json.Marshal(entries)
		if err != nil {
			return sdk.ExtractFallback(err)
		}
		d.AddMetadata("images", string(raw))
		d.AddMetadata("image_count", len(entries))
		return sdk.Extracted()
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

// clientGallery validates a gallery submitted with the document (e.g. by
// the browser extension, which downloads images from its warm cache) and
// returns the normalized entries. Only full-size base64 data URIs are
// accepted; remote URLs are rejected so no remote references leak into the
// index. It returns nil when no usable client gallery is present, in which
// case the caller falls back to server-side downloads.
func (e *ImagesExtractor) clientGallery(d *sdk.Document) []imageEntry {
	raw, ok := d.Metadata["images"]
	if !ok {
		return nil
	}
	var decoded []imageEntry
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(v), &decoded); err != nil {
			return nil
		}
	case []any:
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil
		}
	default:
		return nil
	}
	limit := e.maxImages()
	out := make([]imageEntry, 0, len(decoded))
	for _, entry := range decoded {
		if limit > 0 && len(out) >= limit {
			break
		}
		alt := strings.TrimSpace(entry.Alt)
		if len(alt) > 500 {
			alt = alt[:500]
		}
		// Content-addressed and pending entries pass through with format
		// validation; sizes are uncapped by design.
		if entry.DataURI == "" {
			entry.Alt = alt
			if entry.Key != "" && !validEntryKey(entry.Key) {
				entry.Key = ""
			}
			if entry.Hash != "" && !validEntryKey(entry.Hash) {
				entry.Hash = ""
			}
			if entry.Key == "" && entry.Hash == "" {
				continue
			}
			out = append(out, entry)
			continue
		}
		mime, payload, ok := strings.Cut(strings.TrimSpace(entry.DataURI), ",")
		if !ok || !strings.HasPrefix(mime, "data:image/") || !strings.HasSuffix(mime, ";base64") {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || len(data) == 0 {
			continue
		}
		if sniffed := http.DetectContentType(data); !strings.HasPrefix(sniffed, "image/") {
			continue
		}
		out = append(out, imageEntry{Alt: alt, DataURI: entry.DataURI})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// downloadImages fetches candidates concurrently with bounded parallelism
// and returns base64 data URIs in candidate order. Failures are skipped; an
// empty result is not fatal to the caller chain.
func (e *ImagesExtractor) downloadImages(ctx context.Context, candidates []imageCandidate) []imageEntry {
	maxBytes := e.maxImageBytes()
	cli := &http.Client{Transport: sharedTransport, Timeout: e.imageTimeout()}
	slots := make([]imageEntry, len(candidates))
	valid := make([]bool, len(candidates))
	sem := make(chan struct{}, e.maxConcurrentDownloads())
	var wg sync.WaitGroup
loop:
	for i, c := range candidates {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
			if err != nil {
				return
			}
			resp, err := cli.Do(req)
			if err != nil {
				return
			}
			data, contentType, ok := readImageBody(resp, maxBytes)
			if !ok {
				return
			}
			slots[i] = imageEntry{
				Alt:     c.alt,
				DataURI: fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(data)),
			}
			valid[i] = true
		}()
	}
	wg.Wait()
	out := make([]imageEntry, 0, len(candidates))
	for i, entry := range slots {
		if valid[i] {
			out = append(out, entry)
		}
	}
	return out
}

// maxDownloadBytes is the hard safety ceiling for a single server-side
// image download. Configured max_image_bytes values above it still apply,
// but an uncapped configuration (0) cannot be turned into unbounded memory
// use by a hostile origin.
const maxDownloadBytes = 1 << 30

func readImageBody(resp *http.Response, maxBytes int) ([]byte, string, bool) {
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Warn().Err(cerr).Msg("failed to close gallery image response body")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	limit := maxBytes
	if limit <= 0 || limit > maxDownloadBytes {
		limit = maxDownloadBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil || len(data) == 0 || len(data) > limit {
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
	var raw string
	switch v := d.Metadata["images"].(type) {
	case string:
		raw = v
	case []any:
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		raw = string(data)
	default:
		return nil
	}
	if raw == "" {
		return nil
	}
	var entries []imageEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil
	}
	filtered := entries[:0]
	for _, img := range entries {
		switch {
		case img.Key != "" && validEntryKey(img.Key),
			img.Hash != "" && validEntryKey(img.Hash),
			strings.HasPrefix(img.DataURI, "data:image/"):
			filtered = append(filtered, img)
		}
	}
	return filtered
}

// galleryPreviewItem is the frontend GalleryPreview template payload. Image
// locations stay as content keys so the front end resolves them against its
// own base path; only legacy inline entries carry data URIs.
type galleryPreviewItem struct {
	Alt     string `json:"alt,omitempty"`
	Key     string `json:"key,omitempty"`
	Hash    string `json:"hash,omitempty"`
	DataURI string `json:"data_uri,omitempty"`
}

// Preview renders the gallery via the "gallery" front-end template.
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
	items := make([]galleryPreviewItem, 0, len(entries))
	for _, img := range entries {
		items = append(items, galleryPreviewItem{
			Alt:     img.Alt,
			Key:     img.Key,
			Hash:    img.Hash,
			DataURI: img.DataURI,
		})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return sdk.PreviewFallback(err)
	}
	return sdk.Previewed(sdk.PreviewResponse{Content: string(raw), Template: "gallery"})
}
