// SPDX-License-Identifier: AGPL-3.0-or-later
package contact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asciimoo/hister/server/document"
)

const contactFixture = `<!doctype html><html><head><title>Acme Corp</title></head><body>
<p>Mail us at hello@acme.test or support [at] acme [dot] test</p>
<p>Call <a href="tel:+1-415-555-0132">+1-415-555-0132</a> or 415 555 0199</p>
<a href="mailto:info@acme.test?subject=hi">info@acme.test</a>
<a href="https://wa.me/14155550132">WhatsApp</a>
<a href="https://www.facebook.com/acme">FB</a>
<a href="https://x.com/acme">X</a>
<a href="https://www.linkedin.com/company/acme">LI</a>
<a href="https://mastodon.social/@acme">Mastodon</a>
<a href="https://evil.com/facebook.com/acme">spoof</a>
<p>Not a phone: 2026 or 1234. Not an email: user@example.com logo.png@2x</p>
</body></html>`

func extractFixture(t *testing.T) Contacts {
	t.Helper()
	doc := &document.Document{URL: "https://acme.test/contact", Domain: "acme.test", HTML: contactFixture}
	e := &ContactExtractor{}
	if !e.Match(doc) {
		t.Fatal("Match = false, want true")
	}
	if res := e.Extract(doc); res.Decision() != 1 {
		t.Fatalf("Extract decision = %v: %v", res.Decision(), res.Err())
	}
	raw, ok := doc.Metadata["contacts"].(string)
	if !ok || raw == "" {
		t.Fatal("metadata contacts missing")
	}
	var c Contacts
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExtractsMailtoAndBareEmails(t *testing.T) {
	c := extractFixture(t)
	want := map[string]bool{"info@acme.test": false, "hello@acme.test": false}
	for _, e := range c.Emails {
		if _, ok := want[e.Value]; ok {
			want[e.Value] = true
		}
		if e.Value == "user@example.com" {
			t.Error("blocklisted example.com address was kept")
		}
	}
	for v, found := range want {
		if !found {
			t.Errorf("missing email %q (got %+v)", v, c.Emails)
		}
	}
}

func TestExtractsObfuscatedEmail(t *testing.T) {
	c := extractFixture(t)
	for _, e := range c.Emails {
		if e.Value == "support@acme.test" {
			if !e.Obfuscated {
				t.Error("obfuscated flag not set")
			}
			return
		}
	}
	t.Errorf("missing de-obfuscated support@acme.test (got %+v)", c.Emails)
}

func TestExtractsPhonesAndWhatsApp(t *testing.T) {
	c := extractFixture(t)
	if len(c.Phones) == 0 {
		t.Error("no phones extracted")
	}
	foundTel := false
	for _, p := range c.Phones {
		if strings.Contains(p.Normalized, "14155550132") && p.Source == "tel" {
			foundTel = true
		}
		if p.Value == "2026" || p.Value == "1234" {
			t.Errorf("year kept as phone: %q", p.Value)
		}
	}
	if !foundTel {
		t.Errorf("missing tel: phone (got %+v)", c.Phones)
	}
	if len(c.WhatsApp) != 1 || !strings.Contains(c.WhatsApp[0].Normalized, "14155550132") {
		t.Errorf("missing wa.me number (got %+v)", c.WhatsApp)
	}
}

func TestExtractsSocialsAndRejectsSpoof(t *testing.T) {
	c := extractFixture(t)
	networks := map[string]string{}
	for _, s := range c.Socials {
		networks[s.Network] = s.URL
	}
	for _, want := range []string{"facebook", "x", "linkedin", "mastodon"} {
		if _, ok := networks[want]; !ok {
			t.Errorf("missing social %q (got %+v)", want, c.Socials)
		}
	}
	for _, s := range c.Socials {
		if strings.Contains(s.URL, "evil.com") {
			t.Errorf("spoofed social kept: %+v", s)
		}
	}
	if h := networks["linkedin"]; h != "" && !strings.Contains(h, "acme") {
		t.Errorf("linkedin handle not preserved: %q", h)
	}
}

func TestNoContactsFallsBack(t *testing.T) {
	doc := &document.Document{URL: "https://example.com/", Domain: "example.com", HTML: "<html><body><p>Hello world, no signals here.</p></body></html>"}
	e := &ContactExtractor{}
	if res := e.Extract(doc); res.Decision() == 1 {
		t.Fatal("expected fallback on contact-free page")
	}
	if _, exists := doc.Metadata["contacts"]; exists {
		t.Fatal("contacts metadata should be absent")
	}
}

func TestChileanPricesAreNotPhones(t *testing.T) {
	html := `<!doctype html><html><body>
<p>Oferta: $5.000.000 (antes $6.500.000)</p>
<p>Precio CLP 1.990.000, UF 38,5. Desde $ 990.000 hasta $1.200.000.</p>
<p>Despacho $4.990 c/u. Total $12.345 con IVA.</p>
</body></html>`
	// Prices alone must not yield any contact: ExtractContacts directly,
	// since the chain correctly falls back on an empty result.
	c := ExtractContacts("https://tienda.cl/producto", "tienda.cl", html, defaultLimits())
	for _, p := range c.Phones {
		t.Errorf("price kept as phone: %+v", p)
	}
	for _, p := range c.WhatsApp {
		t.Errorf("price kept as whatsapp: %+v", p)
	}
}

func TestChileanPhonesKept(t *testing.T) {
	doc := &document.Document{URL: "https://tienda.cl/contacto", Domain: "tienda.cl", HTML: `<!doctype html><html><body>
<p>Fono ventas +56 9 1234 5678</p>
<p>Llámanos al 9 8765 4321 o al (2) 2345 6789</p>
<a href="tel:+56912345678">llamar</a>
</body></html>`}
	c := extractToContacts(t, doc)
	if len(c.Phones) == 0 {
		t.Fatal("no phones extracted")
	}
	// tel: and bare-text "+56 9 1234 5678" share a normalized key, so the
	// anchor (high) wins the dedup; the other bare numbers must survive.
	norms := map[string]string{}
	for _, p := range c.Phones {
		norms[p.Normalized] = p.Source
	}
	if src, ok := norms["+56912345678"]; !ok || src != "tel" {
		t.Errorf("tel: phone missing or wrong source (got %+v)", c.Phones)
	}
	if _, ok := norms["987654321"]; !ok {
		t.Errorf("missing bare mobile 9 8765 4321 (got %+v)", c.Phones)
	}
	if _, ok := norms["223456789"]; !ok {
		t.Errorf("missing bare landline (got %+v)", c.Phones)
	}
}

func TestRUTValidKeptInvalidDropped(t *testing.T) {
	// 12.345.678-5 is modulo-11 valid; -9 is not.
	doc := &document.Document{URL: "https://empresa.cl/nosotros", Domain: "empresa.cl", HTML: `<!doctype html><html><body>
<p>RUT 12.345.678-5 — Empresa Acme SpA</p>
<p>RUT 11.111.111-1, sucursal Santiago</p>
<p>Folio 12.345.678-9 (invalido)</p>
</body></html>`}
	c := extractToContacts(t, doc)
	want := map[string]bool{"12.345.678-5": false, "11.111.111-1": false}
	for _, r := range c.Ruts {
		if _, ok := want[r.Normalized]; ok {
			want[r.Normalized] = true
		}
		if r.Normalized == "12.345.678-9" {
			t.Error("invalid RUT kept")
		}
		if r.Confidence != "high" {
			t.Errorf("RUT confidence = %q, want high", r.Confidence)
		}
	}
	for v, found := range want {
		if !found {
			t.Errorf("missing valid RUT %q (got %+v)", v, c.Ruts)
		}
	}
	for _, p := range c.Phones {
		if strings.Contains(p.Value, "345.678") {
			t.Errorf("RUT kept as phone: %+v", p)
		}
	}
}

func TestJSONLDAddressHoursTelephone(t *testing.T) {
	doc := &document.Document{URL: "https://empresa.cl/", Domain: "empresa.cl", HTML: `<!doctype html><html><head>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"LocalBusiness",
"name":"Acme","telephone":"+56 2 2345 6789",
"address":{"@type":"PostalAddress","streetAddress":"Av. Providencia 1234","addressLocality":"Santiago","addressCountry":"CL"},
"openingHoursSpecification":{"@type":"OpeningHoursSpecification","dayOfWeek":["Monday","Tuesday","Wednesday","Thursday","Friday"],"opens":"09:00","closes":"18:00"}}</script>
</head><body><p>Hola</p></body></html>`}
	c := extractToContacts(t, doc)
	if len(c.Addresses) != 1 || !strings.Contains(c.Addresses[0].Value, "Providencia") {
		t.Errorf("missing JSON-LD address (got %+v)", c.Addresses)
	}
	if c.Addresses[0].Confidence != "high" {
		t.Errorf("address confidence = %q, want high", c.Addresses[0].Confidence)
	}
	if len(c.Hours) != 1 || !strings.Contains(c.Hours[0].Value, "09:00-18:00") {
		t.Errorf("missing JSON-LD hours (got %+v)", c.Hours)
	}
	found := false
	for _, p := range c.Phones {
		if strings.Contains(p.Normalized, "56223456789") && p.Source == "jsonld" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing JSON-LD telephone (got %+v)", c.Phones)
	}
}

func TestContactAndMapLinks(t *testing.T) {
	doc := &document.Document{URL: "https://empresa.cl/", Domain: "empresa.cl", HTML: `<!doctype html><html><body>
<a href="/contacto">Contacto</a>
<a href="https://empresa.cl/tiendas">Sucursales</a>
<a href="https://maps.google.com/?q=providencia+1234">Mapa</a>
<a href="https://evil.com/contacto">x</a>
</body></html>`}
	c := extractToContacts(t, doc)
	kinds := map[string]string{}
	for _, l := range c.Links {
		kinds[l.Kind] = l.URL
	}
	if _, ok := kinds["contact_page"]; !ok {
		t.Errorf("missing contact_page link (got %+v)", c.Links)
	}
	if _, ok := kinds["map"]; !ok {
		t.Errorf("missing map link (got %+v)", c.Links)
	}
	for _, l := range c.Links {
		if strings.Contains(l.URL, "evil.com") {
			t.Errorf("cross-host contacto link kept: %+v", l)
		}
	}
}

func extractToContacts(t *testing.T, doc *document.Document) Contacts {
	t.Helper()
	e := &ContactExtractor{}
	if res := e.Extract(doc); res.Decision() != 1 {
		t.Fatalf("Extract decision = %v: %v", res.Decision(), res.Err())
	}
	raw, ok := doc.Metadata["contacts"].(string)
	if !ok || raw == "" {
		t.Fatal("metadata contacts missing")
	}
	var c Contacts
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCapabilities(t *testing.T) {
	e := &ContactExtractor{}
	caps := e.Capabilities()
	if !caps.Enrich || caps.Extract || caps.Preview {
		t.Fatalf("capabilities = %+v, want enrichment only", caps)
	}
}
