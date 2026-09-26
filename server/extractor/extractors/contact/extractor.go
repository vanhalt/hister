// SPDX-License-Identifier: AGPL-3.0-or-later

// Package contact extracts contact information (emails, phones, WhatsApp
// numbers and social profiles) from HTML documents.
//
// It is an enrichment-only extractor: it never selects the body text, it only
// appends a JSON document to d.Metadata["contacts"]. The design mirrors
// EmbeddedVideo and JSONLD: cheap Match pre-filter, single tokenizer pass,
// strict validation to avoid false positives, deterministic output.
package contact

import (
	"bytes"
	"encoding/json"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"github.com/asciimoo/hister/server/extractor/sdk"
	"github.com/asciimoo/hister/server/sanitizer"
)

// ContactExtractor scans HTML for contact signals and stores them in
// d.Metadata["contacts"] as a JSON string.
type ContactExtractor struct {
	sdk.ConfigSupport
}

func (e *ContactExtractor) Name() string { return "Contact" }

func (e *ContactExtractor) Description() string {
	return "Scans HTML for contact information (emails, phones, WhatsApp numbers, social profiles, Chilean RUTs, addresses, opening hours, contact/map links) and stores it in document metadata."
}

func (e *ContactExtractor) Capabilities() sdk.Capabilities {
	return sdk.Capabilities{Enrich: true}
}

// matchMarkers is the cheap pre-scan. If none appear there is nothing to do.
// "@" covers bare-text emails; the rest cover link-based signals.
var matchMarkers = []string{
	"mailto:", "tel:", "wa.me", "whatsapp", "t.me",
	"facebook.com", "instagram.com", "linkedin.com",
	"twitter.com", "x.com", "youtube.com", "youtu.be",
	"tiktok.com", "threads.", "pinterest.", "snapchat.com",
	"mastodon", "bsky.app", "discord.", "telegram",
	"contact", "contacto", "rut", "horario", "postaladdress",
	"openinghours", "$", "@",
}

// Match returns true when the raw HTML plausibly contains contact signals.
func (e *ContactExtractor) Match(d *sdk.Document) bool {
	if len(d.HTML) == 0 {
		return false
	}
	lower := strings.ToLower(d.HTML)
	for _, needle := range matchMarkers {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// Preview does not provide a custom rendering; let the chain continue.
func (e *ContactExtractor) Preview(_ *sdk.Document) sdk.PreviewResult {
	return sdk.PreviewFallback(nil)
}

// Extract scans d.HTML and writes normalized contacts to metadata.
func (e *ContactExtractor) Extract(d *sdk.Document) sdk.ExtractResult {
	result := ExtractContacts(d.URL, d.Domain, d.HTML, defaultLimits())
	if result.IsEmpty() {
		return sdk.ExtractFallback(nil)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return sdk.ExtractFallback(err)
	}
	d.AddMetadata("contacts", string(raw))
	return sdk.Extracted()
}

// EmailEntry is a single discovered email address.
type EmailEntry struct {
	Value      string `json:"value"`
	Source     string `json:"source"`               // mailto | text | obfuscated
	Confidence string `json:"confidence"`           // high | medium
	Obfuscated bool   `json:"obfuscated,omitempty"`
}

// PhoneEntry is a single discovered phone number.
type PhoneEntry struct {
	Value      string `json:"value"`
	Normalized string `json:"normalized,omitempty"`
	Source     string `json:"source"`     // tel | whatsapp | text
	Confidence string `json:"confidence"` // high | medium | low
}

// SocialEntry is a single discovered social profile link.
type SocialEntry struct {
	Network string `json:"network"`
	URL     string `json:"url"`
	Handle  string `json:"handle,omitempty"`
}

// RutEntry is a single discovered Chilean RUT (Rol Único Tributario).
// RUTs are sensitive PII: only values with a valid modulo-11 check digit
// are kept, and consumers should consider masked display.
type RutEntry struct {
	Value      string `json:"value"`
	Normalized string `json:"normalized,omitempty"`
	Source     string `json:"source"`     // text | jsonld
	Confidence string `json:"confidence"` // high (check digit verified)
}

// AddressEntry is a single discovered postal address.
type AddressEntry struct {
	Value      string `json:"value"`
	Source     string `json:"source"`     // jsonld | text
	Confidence string `json:"confidence"` // high | medium
}

// HoursEntry is a single discovered opening-hours statement.
type HoursEntry struct {
	Value      string `json:"value"`
	Source     string `json:"source"`     // jsonld | text
	Confidence string `json:"confidence"` // high | medium
}

// ContactLink is a contact-page or map link.
type ContactLink struct {
	Kind string `json:"kind"` // contact_page | map
	URL  string `json:"url"`
}

// Contacts is the normalized payload stored as Metadata["contacts"].
type Contacts struct {
	Emails    []EmailEntry   `json:"emails"`
	Phones    []PhoneEntry   `json:"phones"`
	WhatsApp  []PhoneEntry   `json:"whatsapp"`
	Socials   []SocialEntry  `json:"socials"`
	Ruts      []RutEntry     `json:"ruts,omitempty"`
	Addresses []AddressEntry `json:"addresses,omitempty"`
	Hours     []HoursEntry   `json:"hours,omitempty"`
	Links     []ContactLink  `json:"links,omitempty"`
}

// IsEmpty reports whether no contact was found.
func (c Contacts) IsEmpty() bool {
	return len(c.Emails) == 0 && len(c.Phones) == 0 && len(c.Whatsapp()) == 0 &&
		len(c.Socials) == 0 && len(c.Ruts) == 0 && len(c.Addresses) == 0 &&
		len(c.Hours) == 0 && len(c.Links) == 0
}

// Whatsapp is an alias-safe accessor (JSON key is "whatsapp").
func (c Contacts) Whatsapp() []PhoneEntry { return c.WhatsApp }

type limits struct {
	maxPerKind int
	maxTotal   int
}

func defaultLimits() limits { return limits{maxPerKind: 20, maxTotal: 50} }

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// phoneRe matches international/national runs with common separators.
	// Validation below (digit count, price/RUT context) removes prices,
	// RUTs, dates and years.
	phoneRe = regexp.MustCompile(`\+?[0-9][0-9\s().\-]{6,}[0-9]`)
	// obfuscated email forms: user [at] example [dot] com
	obfAtRe  = regexp.MustCompile(`(?i)\s*(?:\[at\]|\(at\)|\sat\s)\s*`)
	obfDotRe = regexp.MustCompile(`(?i)\s*(?:\[dot\]|\(dot\)|\sdot\s)\s*`)
	// rutLooseRe matches RUT-shaped runs; validity is confirmed by the
	// modulo-11 check digit (see validRUT) before anything is kept.
	rutLooseRe = regexp.MustCompile(`\b\d{1,2}\.?\d{3}\.?\d{3}-[\dKk]\b`)
	// thousandsDotRe matches dot-grouped thousands: 5.000.000, 1.990.000.
	// Chilean prices use this shape; phones essentially never do.
	thousandsDotRe = regexp.MustCompile(`^\d{1,3}(\.\d{3})+$`)
	// priceHintRe matches currency/price keywords (CLP context).
	priceHintRe = regexp.MustCompile(`(?i)\$|clp\b|uf\b|usd|us\$|eur|mxn|ars|pen|cop|brl|r\$|precio|valor|oferta|cuota|descuento|desde|hasta|\biva\b|pago|total|monto|pesos|c/u`)
	// phoneHintRe rescues dot-grouped runs that sit next to phone keywords.
	phoneHintRe = regexp.MustCompile(`(?i)tel|fono|tel[eé]fono|ll[áa]m|whatsapp|celular|contacto|phone|call|m[óo]vil`)
	// streetRe matches Chilean street addresses in visible text.
	streetRe = regexp.MustCompile(`(?i)\b(?:av\.|avenida|calle|pasaje|camino|cam\.|ruta\s+\d+)\s+[A-Za-zÁÉÍÓÚÜÑáéíóúüñ0-9.\- ]{2,60}\d[A-Za-zÁÉÍÓÚÜÑáéíóúüñ0-9.\- ]{0,40}`)
	// hoursTextRe matches Spanish opening-hours statements in visible text.
	hoursTextRe = regexp.MustCompile(`(?i)\b(?:lun(?:es)?|mar(?:tes)?|mi[ée](?:rcoles)?|jue(?:ves)?|vie(?:rnes)?|s[áa]b(?:ado)?|dom(?:ingo)?)[^\n]{0,8}(?:-|–|a|al)[^\n]{0,8}(?:lun|mar|mi[ée]|jue|vie|s[áa]b|dom)[^\n]{0,24}\d{1,2}:\d{2}`)
)

// emailBlocklist drops placeholders and binary-asset lookalikes.
var emailBlocklist = []string{
	"example.com", "example.org", "example.net",
	".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".css", ".js",
}

func isBlockedEmail(lower string) bool {
	for _, b := range emailBlocklist {
		if strings.HasSuffix(lower, b) {
			return true
		}
	}
	return false
}

// socialHosts maps host suffixes to network names. Suffix matching prevents
// evil.com/facebook.com spoofing. Keep in sync with webui ContactPreview.
var socialHosts = []struct {
	suffix  string
	network string
}{
	{"facebook.com", "facebook"},
	{"instagram.com", "instagram"},
	{"linkedin.com", "linkedin"},
	{"twitter.com", "twitter"},
	{"x.com", "x"},
	{"youtube.com", "youtube"},
	{"youtu.be", "youtube"},
	{"tiktok.com", "tiktok"},
	{"threads.net", "threads"},
	{"threads.com", "threads"},
	{"pinterest.com", "pinterest"},
	{"snapchat.com", "snapchat"},
	{"t.me", "telegram"},
	{"telegram.me", "telegram"},
	{"discord.gg", "discord"},
	{"discord.com", "discord"},
	{"bsky.app", "bluesky"},
}

func classifySocial(host string) (string, bool) {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	for _, s := range socialHosts {
		if host == s.suffix || strings.HasSuffix(host, "."+s.suffix) {
			return s.network, true
		}
		// Mastodon is federated: any host is a candidate only when the path
		// looks like /@user. Handled by the caller via mastodonHint.
	}
	return "", false
}

// ExtractContacts parses rawHTML and returns normalized contacts.
// baseURL is used to resolve relative links; domain skips same-host social
// noise (e.g. a github.com page linking to itself is not a contact signal).
func ExtractContacts(pageURL, pageDomain, rawHTML string, lim limits) Contacts {
	var out Contacts
	if strings.TrimSpace(rawHTML) == "" {
		return out
	}
	var base *url.URL
	if pageURL != "" {
		if u, err := url.Parse(pageURL); err == nil {
			base = u
		}
	}

	hrefs, text := tokenize(rawHTML)

	seenEmails := map[string]struct{}{}
	seenPhones := map[string]struct{}{}
	seenWA := map[string]struct{}{}
	seenSocials := map[string]struct{}{}
	seenRuts := map[string]struct{}{}
	seenAddresses := map[string]struct{}{}
	seenHours := map[string]struct{}{}
	seenLinks := map[string]struct{}{}

	addEmail := func(v, source, confidence string, obfuscated bool) {
		v = strings.TrimSpace(strings.ToLower(v))
		v = strings.Trim(v, ".,;:!?()[]<>\"'")
		if v == "" || isBlockedEmail(v) {
			return
		}
		if _, err := mail.ParseAddress(v); err != nil {
			return
		}
		if _, dup := seenEmails[v]; dup {
			return
		}
		if len(out.Emails) >= lim.maxPerKind {
			return
		}
		seenEmails[v] = struct{}{}
		out.Emails = append(out.Emails, EmailEntry{
			Value: v, Source: source, Confidence: confidence, Obfuscated: obfuscated,
		})
	}
	addPhone := func(raw, source, confidence string, wa bool) {
		digits := digitCount(raw)
		if digits < 7 || digits > 15 {
			return
		}
		// Reject bare years / short dates without a tel:/wa.me anchor.
		if source == "text" && digits <= 8 && !strings.Contains(raw, "+") &&
			!strings.ContainsAny(raw, " ().-") {
			return
		}
		norm := normalizePhone(raw)
		key := norm
		seen := seenPhones
		if wa {
			seen = seenWA
		}
		if _, dup := seen[key]; dup {
			return
		}
		if wa && len(out.WhatsApp) >= lim.maxPerKind {
			return
		}
		if !wa && len(out.Phones) >= lim.maxPerKind {
			return
		}
		seen[key] = struct{}{}
		entry := PhoneEntry{
			Value: sanitizer.SanitizeText(raw), Normalized: norm,
			Source: source, Confidence: confidence,
		}
		if wa {
			out.WhatsApp = append(out.WhatsApp, entry)
		} else {
			out.Phones = append(out.Phones, entry)
		}
	}
	addSocial := func(network, rawURL string) {
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			return
		}
		if pageDomain != "" && strings.EqualFold(u.Hostname(), pageDomain) {
			return
		}
		u.RawQuery = ""
		u.Fragment = ""
		u.Path = strings.TrimSuffix(u.Path, "/")
		if u.Path == "" && u.Host != "" {
			// Bare domain link (e.g. https://x.com) carries no profile signal.
			return
		}
		canonical := u.String()
		if _, dup := seenSocials[network+"\x00"+canonical]; dup {
			return
		}
		if len(out.Socials) >= lim.maxPerKind {
			return
		}
		seenSocials[network+"\x00"+canonical] = struct{}{}
		out.Socials = append(out.Socials, SocialEntry{
			Network: network, URL: canonical, Handle: socialHandle(u.Path),
		})
	}
	addRut := func(raw, source string) {
		norm, ok := normalizeRUT(raw)
		if !ok {
			return
		}
		if _, dup := seenRuts[norm]; dup {
			return
		}
		if len(out.Ruts) >= lim.maxPerKind {
			return
		}
		seenRuts[norm] = struct{}{}
		out.Ruts = append(out.Ruts, RutEntry{
			Value: norm, Normalized: norm, Source: source, Confidence: "high",
		})
	}
	addAddress := func(v, source, confidence string) {
		v = strings.TrimSpace(sanitizer.SanitizeText(v))
		v = strings.Join(strings.Fields(v), " ")
		if len([]rune(v)) < 6 || len([]rune(v)) > 200 {
			return
		}
		if _, dup := seenAddresses[v]; dup {
			return
		}
		if len(out.Addresses) >= lim.maxPerKind {
			return
		}
		seenAddresses[v] = struct{}{}
		out.Addresses = append(out.Addresses, AddressEntry{
			Value: v, Source: source, Confidence: confidence,
		})
	}
	addHours := func(v, source, confidence string) {
		v = strings.TrimSpace(sanitizer.SanitizeText(v))
		v = strings.Join(strings.Fields(v), " ")
		if len([]rune(v)) < 4 || len([]rune(v)) > 200 {
			return
		}
		if _, dup := seenHours[v]; dup {
			return
		}
		if len(out.Hours) >= lim.maxPerKind {
			return
		}
		seenHours[v] = struct{}{}
		out.Hours = append(out.Hours, HoursEntry{
			Value: v, Source: source, Confidence: confidence,
		})
	}
	addLink := func(kind, rawURL string) {
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			return
		}
		u.Fragment = ""
		canonical := u.String()
		if _, dup := seenLinks[kind+"\x00"+canonical]; dup {
			return
		}
		if len(out.Links) >= lim.maxPerKind {
			return
		}
		seenLinks[kind+"\x00"+canonical] = struct{}{}
		out.Links = append(out.Links, ContactLink{Kind: kind, URL: canonical})
	}

	for _, href := range hrefs {
		h := strings.TrimSpace(href)
		if h == "" {
			continue
		}
		lower := strings.ToLower(h)
		switch {
		case strings.HasPrefix(lower, "mailto:"):
			addr := h[len("mailto:"):]
			if i := strings.Index(addr, "?"); i >= 0 {
				addr = addr[:i]
			}
			for _, part := range strings.Split(addr, ",") {
				for _, sub := range strings.Split(part, ";") {
					addEmail(sub, "mailto", "high", false)
				}
			}
		case strings.HasPrefix(lower, "tel:"):
			num := strings.TrimSpace(h[len("tel:"):])
			if i := strings.Index(num, ";"); i >= 0 {
				num = num[:i]
			}
			addPhone(num, "tel", "high", false)
		case isWhatsAppLink(lower):
			if num := whatsappNumber(h); num != "" {
				addPhone(num, "whatsapp", "high", true)
			} else {
				// WhatsApp group/channel invite without a number is still a
				// contact signal; surface it as a social link.
				if abs := absolutize(base, h); abs != "" {
					addSocial("whatsapp", abs)
				}
			}
		default:
			abs := absolutize(base, h)
			if abs == "" {
				continue
			}
			u, err := url.Parse(abs)
			if err != nil || u.Host == "" {
				continue
			}
			if network, ok := classifySocial(u.Hostname()); ok {
				addSocial(network, abs)
				continue
			}
			// Federated Mastodon-style profile: https://host/@user
			if isMastodonProfile(u) {
				if pageDomain == "" || !strings.EqualFold(u.Hostname(), pageDomain) {
					addSocial("mastodon", abs)
				}
				continue
			}
			if kind, ok := classifyContactLink(u); ok {
				// Map links are outbound by nature; contact pages only count
				// when they belong to the page itself (same host or relative).
				if kind == "map" || pageDomain == "" || strings.EqualFold(u.Hostname(), pageDomain) {
					addLink(kind, abs)
				}
			}
		}
	}

	// Structured contact data from JSON-LD (PostalAddress, opening hours,
	// contactPoint telephones). High precision: schema.org authorship.
	for _, blob := range findJSONLDStrings(rawHTML) {
		for _, addr := range jsonldAddresses(blob) {
			addAddress(addr, "jsonld", "high")
		}
		for _, hrs := range jsonldHours(blob) {
			addHours(hrs, "jsonld", "high")
		}
		for _, tel := range jsonldTelephones(blob) {
			tel = strings.TrimSpace(tel)
			if tel == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(tel), "tel:") {
				tel = tel[len("tel:"):]
			}
			addPhone(tel, "jsonld", "high", false)
		}
		for _, r := range rutLooseRe.FindAllString(blob, 8) {
			addRut(r, "jsonld")
		}
	}

	// RUT pre-claim: RUT-shaped runs are claimed by RUT validation and must
	// never reach phone matching (12.345.678-9 is not a phone number).
	rutSpans := rutLooseRe.FindAllStringIndex(text, 64)
	for _, m := range rutLooseRe.FindAllString(text, 64) {
		addRut(m, "text")
	}

	// Bare-text signals from visible copy.
	for _, m := range emailRe.FindAllString(text, 64) {
		addEmail(m, "text", "medium", false)
	}
	if deob := deobfuscateEmails(text); deob != "" {
		for _, m := range emailRe.FindAllString(deob, 32) {
			// Only count addresses that were actually obfuscated.
			if emailRe.FindString(text) != m {
				addEmail(m, "obfuscated", "medium", true)
			}
		}
	}
	for _, idx := range phoneRe.FindAllStringIndex(text, 64) {
		m := strings.TrimSpace(text[idx[0]:idx[1]])
		if digitCount(m) < 7 {
			continue
		}
		if overlapsAny(idx, rutSpans) {
			// Claimed by RUT validation above.
			continue
		}
		if looksLikePrice(text, idx, m) {
			// Chilean price (5.000.000 CLP) or RUT-shaped run, not a phone.
			// Rescued only next to explicit phone keywords.
			if !hasPhoneContext(text, idx) {
				continue
			}
		}
		addPhone(m, "text", "low", false)
	}
	for _, m := range streetRe.FindAllString(text, 16) {
		addAddress(m, "text", "medium")
	}
	for _, m := range hoursTextRe.FindAllString(text, 8) {
		addHours(m, "text", "medium")
	}

	sort.Slice(out.Emails, func(i, j int) bool { return out.Emails[i].Value < out.Emails[j].Value })
	sort.Slice(out.Phones, func(i, j int) bool { return out.Phones[i].Normalized < out.Phones[j].Normalized })
	sort.Slice(out.Ruts, func(i, j int) bool { return out.Ruts[i].Normalized < out.Ruts[j].Normalized })
	sort.Slice(out.Addresses, func(i, j int) bool { return out.Addresses[i].Value < out.Addresses[j].Value })
	sort.Slice(out.Hours, func(i, j int) bool { return out.Hours[i].Value < out.Hours[j].Value })
	sort.Slice(out.Links, func(i, j int) bool {
		if out.Links[i].Kind == out.Links[j].Kind {
			return out.Links[i].URL < out.Links[j].URL
		}
		return out.Links[i].Kind < out.Links[j].Kind
	})
	sort.Slice(out.WhatsApp, func(i, j int) bool { return out.WhatsApp[i].Normalized < out.WhatsApp[j].Normalized })
	sort.Slice(out.Socials, func(i, j int) bool {
		if out.Socials[i].Network == out.Socials[j].Network {
			return out.Socials[i].URL < out.Socials[j].URL
		}
		return out.Socials[i].Network < out.Socials[j].Network
	})
	_ = lim
	return out
}

// tokenize collects anchor hrefs and visible text in one pass.
func tokenize(rawHTML string) (hrefs []string, text string) {
	z := html.NewTokenizer(bytes.NewReader([]byte(rawHTML)))
	var tb strings.Builder
	skip := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return hrefs, tb.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" {
				if tt == html.StartTagToken {
					skip = true
				}
			}
			if tag == "a" {
				for hasAttr {
					k, v, more := z.TagAttr()
					if strings.EqualFold(string(k), "href") {
						hrefs = append(hrefs, string(v))
					}
					hasAttr = more
				}
			} else {
				for hasAttr {
					_, _, more := z.TagAttr()
					hasAttr = more
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if tag := string(name); tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" {
				skip = false
			}
		case html.TextToken:
			if !skip {
				tb.Write(z.Text())
				tb.WriteByte('\n')
			}
		}
	}
}

func absolutize(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "data:") ||
		strings.HasPrefix(ref, "javascript:") {
		return ""
	}
	if strings.HasPrefix(ref, "//") && base != nil {
		return base.Scheme + ":" + ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	if base == nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

func isWhatsAppLink(lower string) bool {
	return strings.Contains(lower, "wa.me/") ||
		strings.Contains(lower, "whatsapp.com/") ||
		strings.Contains(lower, "whatsapp:")
}

// whatsappNumber extracts the dial digits from wa.me/<num> or
// whatsapp.com/...?[phone|text]=<num> links.
func whatsappNumber(href string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "wa.me" || strings.HasSuffix(host, ".wa.me"):
		num := strings.Trim(u.Path, "/")
		if i := strings.Index(num, "/"); i >= 0 {
			num = num[:i]
		}
		num = "+" + digitsOnly(num)
		if digitCount(num) >= 7 && digitCount(num) <= 15 {
			return num
		}
	case strings.Contains(host, "whatsapp.com"):
		if q := u.Query().Get("phone"); q != "" && digitCount(q) >= 7 {
			return "+" + digitsOnly(q)
		}
		if q := u.Query().Get("text"); q != "" && digitCount(q) >= 7 && digitCount(q) <= 15 {
			return "+" + digitsOnly(q)
		}
	}
	return ""
}

func isMastodonProfile(u *url.URL) bool {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return false
	}
	first := parts[0]
	return strings.HasPrefix(first, "@") && len(first) > 1
}

func socialHandle(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	h := parts[0]
	h = strings.TrimPrefix(h, "@")
	// linkedin.com/in/<handle> and youtube.com/channel/<id> carry the
	// identity in the second segment.
	if (h == "in" || h == "company" || h == "channel" || h == "c" || h == "user") && len(parts) > 1 {
		return strings.TrimPrefix(parts[1], "@")
	}
	return h
}

func deobfuscateEmails(text string) string {
	if !strings.Contains(strings.ToLower(text), " at ") &&
		!strings.Contains(text, "[at]") && !strings.Contains(text, "(at)") {
		return ""
	}
	s := obfAtRe.ReplaceAllString(text, "@")
	s = obfDotRe.ReplaceAllString(s, ".")
	return s
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func digitCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

// normalizePhone keeps a leading + and digits only.
func normalizePhone(s string) string {
	s = strings.TrimSpace(s)
	plus := strings.HasPrefix(s, "+")
	return map[bool]string{true: "+", false: ""}[plus] + digitsOnly(s)
}

// overlapsAny reports whether span [s,e) overlaps any span in spans.
func overlapsAny(span []int, spans [][]int) bool {
	for _, o := range spans {
		if span[0] < o[1] && o[0] < span[1] {
			return true
		}
	}
	return false
}

// contextWindow returns lowercased text around a match for keyword scans.
func contextWindow(text string, idx []int, before, after int) (string, string) {
	s := idx[0] - before
	if s < 0 {
		s = 0
	}
	e := idx[1] + after
	if e > len(text) {
		e = len(text)
	}
	return strings.ToLower(text[s:idx[0]]), strings.ToLower(text[idx[1]:e])
}

// looksLikePrice reports whether a bare-text digit run is a price (or other
// non-phone figure) rather than a phone number. Chilean prices group
// thousands with dots (5.000.000 = 5M CLP), which is otherwise
// indistinguishable from a digit run by shape alone.
func looksLikePrice(text string, idx []int, raw string) bool {
	before, after := contextWindow(text, idx, 24, 16)
	if priceHintRe.MatchString(before) || priceHintRe.MatchString(after) {
		return true
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "+") {
		return false
	}
	if thousandsDotRe.MatchString(trimmed) {
		return true
	}
	return false
}

// hasPhoneContext reports whether phone keywords appear near a match,
// rescuing ambiguous runs (e.g. dot-grouped numbers next to "Fono:").
func hasPhoneContext(text string, idx []int) bool {
	before, after := contextWindow(text, idx, 30, 16)
	return phoneHintRe.MatchString(before) || phoneHintRe.MatchString(after)
}

// normalizeRUT validates a RUT-shaped string with the modulo-11 check digit
// and returns it in dotted canonical form (12.345.678-5). ok=false drops
// mistyped lookalikes.
func normalizeRUT(raw string) (norm string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, " ", "")
	i := strings.LastIndex(s, "-")
	if i < 0 || i == 0 || i != len(s)-2 {
		return "", false
	}
	body, given := s[:i], strings.ToUpper(s[i+1:])
	for _, r := range body {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	if len(body) < 7 || len(body) > 8 {
		return "", false
	}
	sum, factor := 0, 2
	for j := len(body) - 1; j >= 0; j-- {
		sum += int(body[j]-'0') * factor
		factor++
		if factor > 7 {
			factor = 2
		}
	}
	want := 11 - (sum % 11)
	var wantS string
	switch want {
	case 11:
		wantS = "0"
	case 10:
		wantS = "K"
	default:
		wantS = string(rune('0' + want))
	}
	if given != wantS {
		return "", false
	}
	// Canonical dotted form: 12345678 -> 12.345.678
	var b strings.Builder
	for j, r := range body {
		if j > 0 && (len(body)-j)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return b.String() + "-" + wantS, true
}

// classifyContactLink maps outbound URLs to contact-page or map links.
func classifyContactLink(u *url.URL) (string, bool) {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, "maps.google.") || host == "goo.gl" ||
		(host == "google.com" && strings.HasPrefix(strings.ToLower(u.Path), "/maps")) {
		return "map", true
	}
	for _, seg := range strings.Split(strings.ToLower(strings.Trim(u.Path, "/")), "/") {
		switch seg {
		case "contacto", "contact", "contactanos", "contact-us", "contactenos",
			"escribenos", "ubicanos", "ubicacion", "sucursales", "tiendas":
			return "contact_page", true
		}
	}
	return "", false
}

// findJSONLDStrings collects application/ld+json script bodies from raw HTML.
func findJSONLDStrings(rawHTML string) []string {
	var blobs []string
	z := html.NewTokenizer(bytes.NewReader([]byte(rawHTML)))
	var inLD bool
	var depth int
	var tb strings.Builder
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return blobs
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			if string(name) == "script" {
				isLD := false
				for hasAttr {
					k, v, more := z.TagAttr()
					if strings.EqualFold(string(k), "type") &&
						strings.EqualFold(strings.TrimSpace(string(v)), "application/ld+json") {
						isLD = true
					}
					hasAttr = more
				}
				if isLD {
					inLD = true
					depth = 1
					tb.Reset()
				}
			} else if inLD {
				for hasAttr {
					_, _, more := z.TagAttr()
					hasAttr = more
				}
			}
		case html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			_ = name
			for hasAttr {
				_, _, more := z.TagAttr()
				hasAttr = more
			}
		case html.TextToken:
			if inLD && depth == 1 {
				tb.Write(z.Text())
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if inLD && string(name) == "script" {
				blobs = append(blobs, tb.String())
				inLD = false
			}
		}
	}
}

// walkJSONLD applies fn to every object node in a decoded JSON-LD value,
// descending through @graph wrappers, arrays and nested objects.
func walkJSONLD(v any, fn func(map[string]any)) {
	switch n := v.(type) {
	case map[string]any:
		fn(n)
		for _, child := range n {
			walkJSONLD(child, fn)
		}
	case []any:
		for _, child := range n {
			walkJSONLD(child, fn)
		}
	}
}

func jsonldTypeSet(n map[string]any) map[string]bool {
	set := map[string]bool{}
	add := func(s string) {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "#"); i >= 0 {
			s = s[i+1:]
		}
		set[strings.ToLower(s)] = true
	}
	switch t := n["@type"].(type) {
	case string:
		add(t)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				add(s)
			}
		}
	}
	return set
}

// formatPostalAddress renders a schema.org PostalAddress node as one line.
func formatPostalAddress(n map[string]any) string {
	parts := []string{}
	for _, k := range []string{"streetAddress", "addressLocality", "addressRegion", "postalCode", "addressCountry"} {
		if s, ok := n[k].(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, strings.TrimSpace(s))
		}
	}
	return strings.Join(parts, ", ")
}

// jsonldAddresses extracts postal addresses from a JSON-LD blob.
func jsonldAddresses(blob string) []string {
	var out []string
	var parsed any
	if err := json.Unmarshal([]byte(blob), &parsed); err != nil {
		return nil
	}
	walkJSONLD(parsed, func(n map[string]any) {
		if jsonldTypeSet(n)["postaladdress"] {
			if s := formatPostalAddress(n); s != "" {
				out = append(out, s)
			}
		}
	})
	return out
}

// dayOfWeekShort normalizes schema.org day values to Mo..Su.
func dayOfWeekShort(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	switch strings.ToLower(s) {
	case "monday", "mo", "mon", "lun", "lunes":
		return "Mo"
	case "tuesday", "tu", "tue", "mar", "martes":
		return "Tu"
	case "wednesday", "we", "wed", "mie", "mié", "miercoles", "miércoles":
		return "We"
	case "thursday", "th", "thu", "jue", "jueves":
		return "Th"
	case "friday", "fr", "fri", "vie", "viernes":
		return "Fr"
	case "saturday", "sa", "sat", "sab", "sáb", "sabado", "sábado":
		return "Sa"
	case "sunday", "su", "sun", "dom", "domingo":
		return "Su"
	}
	return ""
}

// formatOpeningSpec renders one openingHoursSpecification node, or "".
func formatOpeningSpec(n map[string]any) string {
	strs := func(v any) []string {
		var r []string
		switch t := v.(type) {
		case string:
			r = append(r, t)
		case []any:
			for _, e := range t {
				if s, ok := e.(string); ok {
					r = append(r, s)
				}
			}
		}
		return r
	}
	var days []string
	for _, d := range strs(n["dayOfWeek"]) {
		if s := dayOfWeekShort(d); s != "" {
			days = append(days, s)
		}
	}
	if len(days) == 0 {
		return ""
	}
	opens, closes := "", ""
	if s, ok := n["opens"].(string); ok {
		opens = strings.TrimSpace(s)
	}
	if s, ok := n["closes"].(string); ok {
		closes = strings.TrimSpace(s)
	}
	when := ""
	if opens != "" && closes != "" {
		when = opens + "-" + closes
	} else if opens != "" {
		when = opens
	}
	if when != "" {
		return strings.Join(days, ",") + " " + when
	}
	return strings.Join(days, ",")
}

// jsonldHours extracts opening-hours statements from a JSON-LD blob.
func jsonldHours(blob string) []string {
	var out []string
	var parsed any
	if err := json.Unmarshal([]byte(blob), &parsed); err != nil {
		return nil
	}
	walkJSONLD(parsed, func(n map[string]any) {
		if spec, ok := n["openingHoursSpecification"]; ok {
			walkJSONLD(spec, func(s map[string]any) {
				if h := formatOpeningSpec(s); h != "" {
					out = append(out, h)
				}
			})
		}
		if v, ok := n["openingHours"]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					out = append(out, strings.TrimSpace(t))
				}
			case []any:
				for _, e := range t {
					if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
						out = append(out, strings.TrimSpace(s))
					}
				}
			}
		}
	})
	return out
}

// jsonldTelephones extracts telephone values from a JSON-LD blob, both
// direct (Organization.telephone) and via contactPoint nodes.
func jsonldTelephones(blob string) []string {
	var out []string
	var parsed any
	if err := json.Unmarshal([]byte(blob), &parsed); err != nil {
		return nil
	}
	collect := func(n map[string]any) {
		if tel, ok := n["telephone"].(string); ok && strings.TrimSpace(tel) != "" {
			out = append(out, tel)
		}
	}
	walkJSONLD(parsed, func(n map[string]any) {
		types := jsonldTypeSet(n)
		if types["organization"] || types["localbusiness"] || types["person"] ||
			types["store"] || types["restaurant"] || types["hotel"] {
			collect(n)
		}
		if cp, ok := n["contactPoint"]; ok {
			walkJSONLD(cp, func(c map[string]any) {
				collect(c)
			})
		}
	})
	return out
}
