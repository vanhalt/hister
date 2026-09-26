export interface HistoryItem {
  id?: number | string;
  query: string;
  url: string;
  title: string;
  updated_at?: string;
  added?: number;
  updated?: number;
  add_count?: number;
  favicon?: string;
  favicon_key?: string;
  text?: string;
}

export interface DocumentVersion {
  id: number;
  created_at: string;
  html_diff: string;
  text_diff: string;
}

export interface EmbeddedVideo {
  url: string;
  type: 'iframe' | 'video' | 'embed' | 'object';
  mime?: string;
}

export interface ContactEmail {
  value: string;
  source: string;
  confidence: string;
  obfuscated?: boolean;
}

export interface ContactPhone {
  value: string;
  normalized?: string;
  source: string;
  confidence: string;
}

export interface ContactSocial {
  network: string;
  url: string;
  handle?: string;
}

export interface ContactRut {
  value: string;
  normalized?: string;
  source: string;
  confidence: string;
}

export interface ContactAddress {
  value: string;
  source: string;
  confidence: string;
}

export interface ContactHours {
  value: string;
  source: string;
  confidence: string;
}

export interface ContactLink {
  kind: string;
  url: string;
}

export interface ContactInfo {
  emails: ContactEmail[];
  phones: ContactPhone[];
  whatsapp: ContactPhone[];
  socials: ContactSocial[];
  ruts?: ContactRut[];
  addresses?: ContactAddress[];
  hours?: ContactHours[];
  links?: ContactLink[];
}

export interface PreviewMetadata extends Record<string, unknown> {
  author?: string;
  published?: string;
  type?: string;
  description?: string;
  videos?: EmbeddedVideo[];
  jsonld?: unknown[];
  contacts?: ContactInfo;
}

export interface PreviewDocumentDetails {
  type: string;
  language: string;
  label: string;
  visits: number;
  user_id: number;
  metadata: Record<string, unknown>;
}

export interface DocumentPreviewResponse {
  title: string;
  content: string;
  template: string;
  added: number;
  updated: number;
  details: PreviewDocumentDetails;
  meta?: PreviewMetadata;
  version_id?: number;
  version_created_at?: string;
  version_count?: number;
}
