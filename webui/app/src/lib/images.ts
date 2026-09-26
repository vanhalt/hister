import { base } from '$app/paths';

export interface ImageItem {
  /** Page the image was extracted from. */
  pageUrl: string;
  pageTitle: string;
  domain: string;
  documentId: string;
  alt: string;
  /** Base64 data URI. No remote URLs are ever exposed here. */
  dataUri: string;
}

interface GalleryImage {
  alt?: string;
  data_uri?: string;
}

interface SearchDocument {
  id?: string;
  url: string;
  title?: string;
  domain?: string;
  metadata?: Record<string, unknown>;
}

function parseGallery(metadata?: Record<string, unknown>): GalleryImage[] {
  if (!metadata) return [];
  const raw = metadata.images;
  if (typeof raw !== 'string' || !raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (entry): entry is GalleryImage =>
        typeof entry === 'object' &&
        entry !== null &&
        typeof (entry as GalleryImage).data_uri === 'string' &&
        (entry as GalleryImage).data_uri!.startsWith('data:image/'),
    );
  } catch {
    return [];
  }
}

/** Fetches indexed gallery images (one card per image) with source attribution. */
export async function fetchImages(limit = 200): Promise<ImageItem[]> {
  // NOTE: the search endpoint lives at /search, not /api/search, so apiFetch
  // (which prefixes /api) cannot be used here.
  const params = new URLSearchParams({ format: 'json', q: '*' });
  const res = await fetch(`${base}/search?${params.toString()}`, {
    headers: { Accept: 'application/json' },
    credentials: 'include',
  });
  if (res.status === 403) {
    window.location.href = `${base}/auth?reason=auth_required`;
    throw new Error('Authentication required');
  }
  if (!res.ok) throw new Error(`Failed to load images (HTTP ${res.status})`);
  const contentType = res.headers.get('content-type') ?? '';
  if (!contentType.includes('application/json')) {
    throw new Error(`Search endpoint returned ${res.status} ${contentType || 'with no content type'}`);
  }
  const data = await res.json();
  const docs: SearchDocument[] = data?.documents ?? [];
  const out: ImageItem[] = [];
  for (const doc of docs) {
    if (!doc.url) continue;
    for (const img of parseGallery(doc.metadata)) {
      out.push({
        pageUrl: doc.url,
        pageTitle: doc.title || doc.url,
        domain: doc.domain || doc.url.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').split('/')[0],
        documentId: typeof doc.id === 'string' ? doc.id : '',
        alt: img.alt || '',
        dataUri: img.data_uri!,
      });
      if (out.length >= limit) return out;
    }
  }
  return out;
}
