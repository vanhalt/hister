// SPDX-License-Identifier: AGPL-3.0-or-later

package indexer

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asciimoo/hister/server/document"
	"github.com/asciimoo/hister/server/testutil"
)

var testPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func testGalleryURI() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG)
}

func TestStoreImageBytesRoundTrip(t *testing.T) {
	idx := newTestIndexer(t, testutil.Config(t))
	t.Cleanup(idx.Close)

	key, err := idx.StoreImageBytes(testPNG)
	if err != nil {
		t.Fatalf("StoreImageBytes: %v", err)
	}
	if key != ImageKeyForBytes(testPNG) {
		t.Errorf("key = %q, want content hash", key)
	}
	if !idx.HasImage(key) {
		t.Error("HasImage should be true after store")
	}
	if idx.HasImage(strings.Repeat("0", 64)) {
		t.Error("HasImage should be false for unknown key")
	}
	if idx.HasImage("not-a-key") {
		t.Error("HasImage should be false for malformed key")
	}
	got, err := idx.ReadImage(key)
	if err != nil {
		t.Fatalf("ReadImage: %v", err)
	}
	if string(got) != string(testPNG) {
		t.Error("ReadImage bytes differ from stored bytes")
	}
	// Storing the same bytes twice must not fail or duplicate.
	if _, err := idx.StoreImageBytes(testPNG); err != nil {
		t.Errorf("second store: %v", err)
	}
	if _, err := idx.StoreImageBytes([]byte("not an image")); err == nil {
		t.Error("expected error for non-image bytes")
	}
	if _, err := idx.ReadImage("bogus"); err == nil {
		t.Error("expected error for malformed key")
	}
}

func TestStoreGalleryDataURIsConvertsToKeys(t *testing.T) {
	idx := newTestIndexer(t, testutil.Config(t))
	t.Cleanup(idx.Close)

	raw, _ := json.Marshal([]galleryEntry{
		{Alt: "A", DataURI: testGalleryURI()},
		{Alt: "remote", DataURI: "https://example.com/a.jpg"},
	})
	d := &document.Document{
		URL:      "https://example.com/page",
		Metadata: map[string]any{"images": string(raw)},
	}
	if err := idx.storeGalleryDataURIs(d); err != nil {
		t.Fatalf("storeGalleryDataURIs: %v", err)
	}
	if len(d.ImageKeys) != 1 {
		t.Fatalf("ImageKeys = %v, want one key", d.ImageKeys)
	}
	stored, _ := d.Metadata["images"].(string)
	if strings.Contains(stored, "data:image") {
		t.Error("inline data URI must not persist in stored metadata")
	}
	var entries []galleryEntry
	if err := json.Unmarshal([]byte(stored), &entries); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != d.ImageKeys[0] || entries[0].Alt != "A" {
		t.Errorf("unexpected normalized manifest: %+v", entries)
	}
	if !idx.HasImage(d.ImageKeys[0]) {
		t.Error("converted image bytes missing from store")
	}
}

func TestAddConvertsInlineGallery(t *testing.T) {
	idx := newTestIndexer(t, testutil.Config(t))
	t.Cleanup(idx.Close)

	raw, _ := json.Marshal([]galleryEntry{{Alt: "A", DataURI: testGalleryURI()}})
	d := &document.Document{
		URL:      "https://example.com/gallery",
		Title:    "Gallery",
		Text:     "gallery page",
		Metadata: map[string]any{"images": string(raw)},
	}
	if err := idx.Add(d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	stored := idx.GetByURLAndUser("https://example.com/gallery", 0)
	if stored == nil {
		t.Fatal("document not found after Add")
	}
	if len(stored.ImageKeys) != 1 {
		t.Fatalf("ImageKeys = %v, want one key", stored.ImageKeys)
	}
	manifest, _ := stored.Metadata["images"].(string)
	if strings.Contains(manifest, "data:image") {
		t.Error("inline data URI persisted in index")
	}
}

func TestMergeGalleryImageResolvesHash(t *testing.T) {
	idx := newTestIndexer(t, testutil.Config(t))
	t.Cleanup(idx.Close)

	hash := ImageKeyForBytes(testPNG)
	raw, _ := json.Marshal([]galleryEntry{{Alt: "Client", Hash: hash}})
	d := &document.Document{
		URL:      "https://example.com/gallery",
		Title:    "Gallery",
		Text:     "gallery page",
		Metadata: map[string]any{"images": string(raw)},
	}
	if err := idx.Add(d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	key, err := idx.mergeGalleryImage("https://example.com/gallery", 0, "Client", hash, testPNG)
	if err != nil {
		t.Fatalf("mergeGalleryImage: %v", err)
	}
	if key != hash {
		t.Errorf("key = %q, want content hash %q", key, hash)
	}
	stored := idx.GetByURLAndUser("https://example.com/gallery", 0)
	if stored == nil {
		t.Fatal("document not found after merge")
	}
	manifest, _ := stored.Metadata["images"].(string)
	var entries []galleryEntry
	if err := json.Unmarshal([]byte(manifest), &entries); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != hash || entries[0].Hash != "" {
		t.Errorf("hash entry not resolved: %+v", entries)
	}
	if len(stored.ImageKeys) != 1 || stored.ImageKeys[0] != hash {
		t.Errorf("ImageKeys = %v, want [%s]", stored.ImageKeys, hash)
	}
}
