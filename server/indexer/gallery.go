// SPDX-License-Identifier: AGPL-3.0-or-later

package indexer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/asciimoo/hister/server/document"
)

// galleryEntry is a single image in the gallery manifest stored in
// Metadata["images"]. Key addresses bytes in the content-addressed image
// store; Hash is a not-yet-uploaded client image (its SHA-256, which becomes
// the key once the bytes arrive); DataURI is a legacy or freshly submitted
// inline image converted to a key before indexing.
type galleryEntry struct {
	Alt     string `json:"alt,omitempty"`
	Key     string `json:"key,omitempty"`
	Hash    string `json:"hash,omitempty"`
	DataURI string `json:"data_uri,omitempty"`
}

// validKey reports whether s looks like a SHA-256 content key.
func validKey(s string) bool {
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

// parseGalleryEntries decodes the gallery manifest from document metadata,
// accepting both the canonical JSON string form and a plain array.
func parseGalleryEntries(metadata map[string]any) []galleryEntry {
	raw, ok := metadata["images"]
	if !ok {
		return nil
	}
	var decoded []galleryEntry
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
	return decoded
}

// appendImageKeyRefs collects content keys from a stored image_keys field
// value, which bleve returns as either a string or an array.
func appendImageKeyRefs(field any, out *[]string, seen map[string]struct{}) {
	switch v := field.(type) {
	case string:
		if v != "" {
			if _, dup := seen[v]; !dup {
				*out = append(*out, v)
				seen[v] = struct{}{}
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				if _, dup := seen[s]; !dup {
					*out = append(*out, s)
					seen[s] = struct{}{}
				}
			}
		}
	}
}

// galleryImageContentType identifies supported image formats from their
// contents, never from a supplied media type.
func galleryImageContentType(data []byte) string {
	if contentType := http.DetectContentType(data); strings.HasPrefix(contentType, "image/") {
		return contentType
	}
	return ""
}

// StoreImageBytes validates raw image bytes and writes them to the
// content-addressed image store, returning the content key. Sizes are
// intentionally uncapped: deduplication keeps repeated images to a single
// blob and callers enforce their own request limits.
func (i *Indexer) StoreImageBytes(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty image data")
	}
	if galleryImageContentType(data) == "" {
		return "", fmt.Errorf("unsupported image content")
	}
	return i.data.write(imageSubdir, data)
}

// HasImage reports whether the image store already holds the key.
func (i *Indexer) HasImage(key string) bool {
	if !validKey(key) {
		return false
	}
	return i.data.has(imageSubdir, key)
}

// ReadImage returns the stored image bytes for key.
func (i *Indexer) ReadImage(key string) ([]byte, error) {
	if !validKey(key) {
		return nil, fmt.Errorf("invalid image key")
	}
	return i.data.read(imageSubdir, key)
}

// ImageKeyForBytes returns the content key the bytes would be stored under
// without writing anything.
func ImageKeyForBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// storeGalleryDataURIs converts inline data URI gallery entries to
// content-addressed keys, populates d.ImageKeys from the final key set, and
// rewrites the manifest without inline blobs so large images are never
// persisted inside the Bleve index. Hash-only (pending client upload) and
// key entries pass through untouched; unusable entries are dropped.
func (i *Indexer) storeGalleryDataURIs(d *document.Document) error {
	entries := parseGalleryEntries(d.Metadata)
	if len(entries) == 0 {
		return nil
	}
	normalized := make([]galleryEntry, 0, len(entries))
	keys := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		alt := strings.TrimSpace(entry.Alt)
		if len(alt) > 500 {
			alt = alt[:500]
		}
		if entry.DataURI != "" {
			mime, payload, ok := strings.Cut(strings.TrimSpace(entry.DataURI), ",")
			if !ok || !strings.HasPrefix(mime, "data:image/") || !strings.HasSuffix(mime, ";base64") {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(payload)
			if err != nil || len(data) == 0 || galleryImageContentType(data) == "" {
				continue
			}
			key, err := i.data.write(imageSubdir, data)
			if err != nil {
				return fmt.Errorf("store gallery image: %w", err)
			}
			entry = galleryEntry{Alt: alt, Key: key}
		} else {
			entry.Alt = alt
			if entry.Key != "" && !validKey(entry.Key) {
				entry.Key = ""
			}
			if entry.Hash != "" && !validKey(entry.Hash) {
				entry.Hash = ""
			}
			if entry.Key == "" && entry.Hash == "" {
				continue
			}
		}
		normalized = append(normalized, entry)
		if entry.Key != "" {
			if _, dup := seen[entry.Key]; !dup {
				seen[entry.Key] = struct{}{}
				keys = append(keys, entry.Key)
			}
		}
	}
	d.ImageKeys = keys
	if d.Metadata == nil {
		d.Metadata = make(map[string]any)
	}
	if len(normalized) == 0 {
		delete(d.Metadata, "images")
		delete(d.Metadata, "image_count")
		return nil
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("encode gallery manifest: %w", err)
	}
	d.Metadata["images"] = string(raw)
	d.Metadata["image_count"] = len(normalized)
	return nil
}

// mergeGalleryImage stores validated image bytes and merges the resulting
// key into the document's gallery manifest, resolving a pending hash entry
// when one matches. It returns the content key.
func (i *Indexer) mergeGalleryImage(docURL string, uid uint, alt, hash string, data []byte) (string, error) {
	if galleryImageContentType(data) == "" {
		return "", fmt.Errorf("unsupported image content")
	}
	key, err := i.data.write(imageSubdir, data)
	if err != nil {
		return "", fmt.Errorf("store gallery image: %w", err)
	}
	_, err = i.mergeGalleryKey(docURL, uid, alt, hash, key)
	if err != nil {
		return "", err
	}
	return key, nil
}

// AddGalleryImageData validates image bytes, stores them, and merges the key
// into the document's gallery when the document exists. It reports whether
// the key was merged; bytes are kept even when the document is missing so
// later submissions and other pages sharing the image benefit.
func (i *Indexer) AddGalleryImageData(docURL string, uid uint, alt, hash string, data []byte) (key string, merged bool, err error) {
	if galleryImageContentType(data) == "" {
		return "", false, fmt.Errorf("unsupported image content")
	}
	key, err = i.data.write(imageSubdir, data)
	if err != nil {
		return "", false, fmt.Errorf("store gallery image: %w", err)
	}
	merged, err = i.mergeGalleryKey(docURL, uid, alt, hash, key)
	if err != nil {
		return "", false, err
	}
	return key, merged, nil
}

// mergeGalleryKey resolves a pending hash entry to key (or appends a new
// keyed entry) in the document's gallery manifest and persists it. It
// returns false without an error when the document does not exist.
func (i *Indexer) mergeGalleryKey(docURL string, uid uint, alt, hash, key string) (bool, error) {
	d := i.GetByURLAndUser(docURL, uid)
	if d == nil {
		return false, nil
	}
	entries := parseGalleryEntries(d.Metadata)
	merged := false
	for n, entry := range entries {
		if entry.Hash != "" && (hash == "" || entry.Hash == hash) && entry.Key == "" {
			entries[n] = galleryEntry{Alt: firstNonEmpty(entry.Alt, alt), Key: key}
			merged = true
			if hash != "" {
				break
			}
		}
	}
	if !merged {
		entries = append(entries, galleryEntry{Alt: alt, Key: key})
	}
	if d.Metadata == nil {
		d.Metadata = make(map[string]any)
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return false, fmt.Errorf("encode gallery manifest: %w", err)
	}
	d.Metadata["images"] = string(raw)
	d.Metadata["image_count"] = len(entries)
	if err := i.storeGalleryDataURIs(d); err != nil {
		return false, err
	}
	if err := i.save(d); err != nil {
		return false, fmt.Errorf("save gallery manifest: %w", err)
	}
	return true, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
