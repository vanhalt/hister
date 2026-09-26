// Gallery collection (content-script side, DOM only) and download (background
// side, network only) for page images. Downloaded full-size images are
// attached to the submission so the server can index them without fetching
// anything itself; the browser's warm HTTP cache makes these fetches cheap.

export interface GalleryCandidate {
  url: string;
  alt: string;
}

export interface GalleryImage {
  alt: string;
  data_uri: string;
}

export const GALLERY_MAX_IMAGES = 20;
export const GALLERY_MAX_IMAGE_BYTES = 1024 * 1024;
const GALLERY_CONCURRENCY = 4;
const GALLERY_IMAGE_TIMEOUT_MS = 8000;
const GALLERY_BUDGET_MS = 20000;

const SKIP_MARKERS = ['favicon', 'sprite', 'pixel', 'tracking', '1x1', '.svg'];

function firstSrcsetURL(srcset: string | null): string {
  if (!srcset) return '';
  for (const part of srcset.split(',')) {
    const fields = part.trim().split(/\s+/);
    if (fields[0]) return fields[0];
  }
  return '';
}

function attr(el: Element, name: string): string {
  return el.getAttribute(name) ?? '';
}

/** Collects gallery image candidates from the live DOM. No network access. */
export function collectGalleryCandidates(
  doc: Document,
  limit: number = GALLERY_MAX_IMAGES,
): GalleryCandidate[] {
  const seen = new Set<string>();
  const out: GalleryCandidate[] = [];
  const add = (raw: string, alt: string) => {
    raw = raw.trim();
    if (!raw || raw.startsWith('data:') || raw.startsWith('blob:')) return;
    const first = raw.split(/\s+/)[0];
    let resolved: string;
    try {
      resolved = new URL(first, doc.baseURI).toString();
    } catch {
      return;
    }
    if (!resolved.startsWith('http://') && !resolved.startsWith('https://')) return;
    const lower = resolved.toLowerCase();
    if (SKIP_MARKERS.some((marker) => lower.includes(marker))) return;
    if (seen.has(resolved)) return;
    seen.add(resolved);
    out.push({ url: resolved, alt: alt.trim() });
  };
  doc.querySelectorAll('img').forEach((el) => {
    const width = attr(el, 'width');
    const height = attr(el, 'height');
    if ((width === '1' && height === '1') || width === '1x1') return;
    const alt = attr(el, 'alt');
    const src = attr(el, 'src') || attr(el, 'data-src');
    if (src) add(src, alt);
    const srcset = firstSrcsetURL(attr(el, 'srcset'));
    if (srcset) add(srcset, alt);
  });
  doc.querySelectorAll('picture source[srcset]').forEach((el) => {
    const srcset = firstSrcsetURL(attr(el, 'srcset'));
    if (srcset) add(srcset, '');
  });
  doc.querySelectorAll('meta[property="og:image"]').forEach((el) => {
    const content = attr(el, 'content');
    if (content) add(content, '');
  });
  return out.slice(0, limit);
}

function sniffImageMime(bytes: Uint8Array): string {
  const startsWith = (prefix: number[]): boolean =>
    prefix.every((byte, i) => bytes[i] === byte);
  if (startsWith([0x89, 0x50, 0x4e, 0x47])) return 'image/png';
  if (startsWith([0xff, 0xd8, 0xff])) return 'image/jpeg';
  if (startsWith([0x47, 0x49, 0x46])) return 'image/gif';
  if (
    startsWith([0x52, 0x49, 0x46, 0x46]) &&
    bytes[8] === 0x57 &&
    bytes[9] === 0x45 &&
    bytes[10] === 0x42 &&
    bytes[11] === 0x50
  )
    return 'image/webp';
  if (startsWith([0x42, 0x4d])) return 'image/bmp';
  if (startsWith([0x00, 0x00, 0x01, 0x00])) return 'image/x-icon';
  return '';
}

function arrayBufferToBase64(bytes: Uint8Array): string {
  let binary = '';
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return btoa(binary);
}

async function readCappedBody(
  response: Response,
  maxBytes: number,
): Promise<Uint8Array | null> {
  const reader = response.body?.getReader();
  if (!reader) return null;
  try {
    const chunks: Uint8Array[] = [];
    let total = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.length;
      if (total > maxBytes) {
        await reader.cancel().catch(() => undefined);
        return null;
      }
      chunks.push(value);
    }
    const out = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      out.set(chunk, offset);
      offset += chunk.length;
    }
    return out;
  } finally {
    reader.releaseLock();
  }
}

async function downloadCandidate(
  candidate: GalleryCandidate,
  maxBytes: number,
  timeoutMs: number,
): Promise<GalleryImage | null> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    // Host permissions bypass CORS; the shared browser HTTP cache keeps
    // recently rendered images warm. Cookies are included so login-walled
    // images the server could never fetch resolve here.
    const response = await fetch(candidate.url, {
      credentials: 'include',
      signal: controller.signal,
    });
    if (!response.ok) return null;
    const headerMime = (response.headers.get('content-type') ?? '').split(';')[0].trim().toLowerCase();
    const bytes = await readCappedBody(response, maxBytes);
    if (!bytes || bytes.length === 0) return null;
    const mime = headerMime.startsWith('image/') ? headerMime : sniffImageMime(bytes);
    if (!mime.startsWith('image/')) return null;
    return {
      alt: candidate.alt,
      data_uri: `data:${mime};base64,${arrayBufferToBase64(bytes)}`,
    };
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Downloads gallery candidates concurrently with bounded parallelism,
 * preserving candidate order. Stops launching new downloads after the
 * overall budget so submissions are never blocked for long. Full-size
 * bytes are kept; the server validates and stores them.
 */
export async function downloadGalleryImages(
  candidates: GalleryCandidate[],
  options: {
    maxImages?: number;
    maxBytes?: number;
    concurrency?: number;
    timeoutMs?: number;
    budgetMs?: number;
  } = {},
): Promise<GalleryImage[]> {
  const {
    maxImages = GALLERY_MAX_IMAGES,
    maxBytes = GALLERY_MAX_IMAGE_BYTES,
    concurrency = GALLERY_CONCURRENCY,
    timeoutMs = GALLERY_IMAGE_TIMEOUT_MS,
    budgetMs = GALLERY_BUDGET_MS,
  } = options;
  const queue = candidates.slice(0, maxImages);
  const slots: (GalleryImage | null)[] = new Array(queue.length).fill(null);
  const deadline = Date.now() + budgetMs;
  let next = 0;
  const workers = Array.from(
    { length: Math.min(concurrency, queue.length) },
    async () => {
      for (;;) {
        if (Date.now() > deadline) return;
        const i = next++;
        if (i >= queue.length) return;
        slots[i] = await downloadCandidate(queue[i], maxBytes, timeoutMs);
      }
    },
  );
  await Promise.all(workers);
  return slots.filter((entry): entry is GalleryImage => entry !== null);
}
