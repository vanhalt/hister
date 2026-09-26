// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// maxImageUploadBytes bounds a single gallery image upload. Content-addressed
// sizes are otherwise uncapped; this only guards the server against
// unbounded request bodies.
const maxImageUploadBytes = 512 << 20

const storedImageCacheControl = "public, max-age=31536000, immutable"

// imageUploadRequest is a single gallery image submitted after the page,
// carrying the SHA-256 its client computed over the raw bytes.
type imageUploadRequest struct {
	URL     string `json:"url"`
	Alt     string `json:"alt"`
	Hash    string `json:"hash"`
	DataURI string `json:"data_uri"`
}

// serveImageUpload stores one gallery image and merges its key into the
// owning document's gallery manifest. It reports whether the key was merged;
// bytes are kept even when the document does not exist yet so later
// submissions and other pages sharing the image benefit.
func serveImageUpload(c *webContext) {
	if c.Request.Method != http.MethodPost {
		http.Error(c.Response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, maxImageUploadBytes)
	var req imageUploadRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		if maxBytesErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(c.Response, fmt.Sprintf("image exceeds the %d MiB limit", maxBytesErr.Limit>>20), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(c.Response, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		http.Error(c.Response, "url is required", http.StatusBadRequest)
		return
	}
	req.Hash = strings.ToLower(strings.TrimSpace(req.Hash))
	if req.Hash != "" && !validImageKey(req.Hash) {
		http.Error(c.Response, "invalid hash", http.StatusBadRequest)
		return
	}
	mime, payload, ok := strings.Cut(strings.TrimSpace(req.DataURI), ",")
	if !ok || !strings.HasPrefix(mime, "data:image/") || !strings.HasSuffix(mime, ";base64") {
		http.Error(c.Response, "data_uri must be a base64 image data URI", http.StatusBadRequest)
		return
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		http.Error(c.Response, "invalid image data", http.StatusBadRequest)
		return
	}
	key, merged, err := c.Indexer.AddGalleryImageData(req.URL, submittedDocumentUserID(c), galleryAlt(req.Alt), req.Hash, data)
	if err != nil {
		log.Warn().Err(err).Str("url", req.URL).Msg("failed to store gallery image")
		http.Error(c.Response, "unsupported image content", http.StatusUnsupportedMediaType)
		return
	}
	c.JSON(map[string]any{"key": key, "merged": merged})
}

// serveImageNeeded reports which of the submitted content hashes are absent
// from the image store so clients upload only unknown bytes.
func serveImageNeeded(c *webContext) {
	if c.Request.Method != http.MethodPost {
		http.Error(c.Response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	maxBodyBytes := c.Config.Server.MaxBatchBodyBytes()
	c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, maxBodyBytes)
	var req struct {
		Hashes []string `json:"hashes"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		http.Error(c.Response, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	needed := make([]string, 0, len(req.Hashes))
	seen := make(map[string]struct{}, len(req.Hashes))
	for _, hash := range req.Hashes {
		hash = strings.ToLower(strings.TrimSpace(hash))
		if !validImageKey(hash) {
			continue
		}
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		if !c.Indexer.HasImage(hash) {
			needed = append(needed, hash)
		}
	}
	c.JSON(map[string]any{"needed": needed})
}

// serveStoredImage serves a content-addressed gallery image by key. Served
// bytes are inert raster images; SVG is rejected at upload validation, and
// the sandbox CSP below isolates the response if it is ever opened as a
// document.
func serveStoredImage(c *webContext) {
	c.Response.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	c.Response.Header().Set("X-Content-Type-Options", "nosniff")
	c.Response.Header().Set("Cache-Control", "no-store")

	key := strings.ToLower(strings.TrimSpace(c.Request.URL.Query().Get("key")))
	if !validImageKey(key) {
		http.Error(c.Response, "invalid image key", http.StatusBadRequest)
		return
	}
	data, err := c.Indexer.ReadImage(key)
	if err != nil {
		http.Error(c.Response, "image not found", http.StatusNotFound)
		return
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		http.Error(c.Response, "invalid image data", http.StatusUnsupportedMediaType)
		return
	}
	c.Response.Header().Set("Content-Type", contentType)
	c.Response.Header().Set("Cache-Control", storedImageCacheControl)
	c.Response.Header().Set("ETag", `"`+key+`"`)
	if _, err := c.Response.Write(data); err != nil {
		log.Warn().Err(err).Str("key", key).Msg("failed to write stored image response")
	}
}

// validImageKey reports whether s looks like a SHA-256 content key.
func validImageKey(s string) bool {
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

// galleryAlt trims overlong alt text the same way the extractor does.
func galleryAlt(alt string) string {
	alt = strings.TrimSpace(alt)
	if len(alt) > 500 {
		alt = alt[:500]
	}
	return alt
}
