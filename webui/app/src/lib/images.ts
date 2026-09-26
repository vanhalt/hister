import { base } from '$app/paths';

export interface ImageItem {
  /** Page the image was extracted from. */
  pageUrl: string;
  pageTitle: string;
  domain: string;
  documentId: string;
  alt: string;
  /** Content-addressed store key. Preferred image source. */
  key?: string;
  /** Pending client upload hash. Shown as a placeholder until bytes arrive. */
  hash?: string;
  /** Legacy inline image. No remote URLs are ever exposed here. */
  dataUri?: string;
}

export function imageSrc(item: Pick<ImageItem, 'key' | 'dataUri'>): string {
  if (item.key) return `${base}/api/image?key=${encodeURIComponent(item.key)}`;
  if (item.dataUri) return item.dataUri;
  return '';
}

interface GalleryImage {
  alt?: string;
  key?: string;
  hash?: string;
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
        (typeof (entry as GalleryImage).key === 'string' ||
          typeof (entry as GalleryImage).hash === 'string' ||
          (typeof (entry as GalleryImage).data_uri === 'string' &&
            (entry as GalleryImage).data_uri!.startsWith('data:image/'))),
    );
  } catch {
    return [];
  }
}

/**
 * Collapses byte-identical images from the same page into a single card.
 * The same file is often referenced by several URLs (query-string variants,
 * srcset/src duplicates), which URL-based dedupe at index time cannot catch.
 * The full data URI participates in the key so `slice` tricks can never
 * collide again; the first occurrence (with its alt text) wins.
 */
export function dedupeImageItems(items: ImageItem[]): ImageItem[] {
  const seen = new Set<string>();
  return items.filter((item) => {
    const key = `${item.pageUrl}\n${item.key ?? ''}\n${item.hash ?? ''}\n${item.dataUri ?? ''}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
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
        alt: typeof img.alt === 'string' ? img.alt : '',
        key: typeof img.key === 'string' ? img.key : undefined,
        hash: typeof img.hash === 'string' ? img.hash : undefined,
        dataUri:
          typeof img.data_uri === 'string' && img.data_uri.startsWith('data:image/')
            ? img.data_uri
            : undefined,
      });
    }
  }
  return dedupeImageItems(out).slice(0, limit);
}
