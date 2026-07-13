package extract

import (
	"regexp"
	"strings"

	"github.com/makeforme/leadscraper/internal/domain"
)

var (
	emailRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

	// Indian numbers: optional +91/0 prefix, then a 10-digit number starting 6-9.
	phoneRe = regexp.MustCompile(`(?:\+?91[\s\-]?|0)?[6-9]\d{4}[\s\-]?\d{5}\b`)

	waLinkRe = regexp.MustCompile(`(?i)(?:wa\.me|api\.whatsapp\.com/send\?phone=)/?(\+?\d[\d\s\-]{7,15})`)
	telRe    = regexp.MustCompile(`(?i)tel:\+?([\d\s\-()]{7,20})`)
	mailToRe = regexp.MustCompile(`(?i)mailto:([a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,})`)

	digitsRe = regexp.MustCompile(`\D`)
)

// Junk addresses that appear in stock templates, tracking pixels, and asset
// filenames. Left in, they poison outreach lists.
var emailBlocklist = []string{
	"example.com", "example.org", "domain.com", "yourdomain",
	"sentry.io", "wixpress.com", "godaddy.com", "squarespace.com",
	"@2x.png", "@3x.png", "@sentry", "noreply@", "no-reply@",
}

var emailBlockedExt = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".css", ".js"}

// Contacts pulls emails, phones, and WhatsApp numbers out of page HTML.
// Structured sources (mailto:, tel:, wa.me) score higher than a bare regex hit
// in the body text, because those are markup the site owner wrote deliberately.
func Contacts(businessID, html, source string) []*domain.Contact {
	var out []*domain.Contact

	seenEmail := map[string]bool{}
	addEmail := func(addr, src string, confidence float64) {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if addr == "" || seenEmail[addr] || !plausibleEmail(addr) {
			return
		}
		seenEmail[addr] = true
		out = append(out, &domain.Contact{
			BusinessID:  businessID,
			Email:       addr,
			ContactType: classifyEmail(addr),
			Source:      src,
			Confidence:  confidence,
		})
	}

	for _, m := range mailToRe.FindAllStringSubmatch(html, -1) {
		addEmail(m[1], source+":mailto", 0.95)
	}
	for _, m := range emailRe.FindAllString(html, -1) {
		addEmail(m, source+":text", 0.6)
	}

	seenPhone := map[string]bool{}
	addPhone := func(raw, src string, confidence float64, whatsapp bool) {
		norm := normalizePhone(raw)
		if norm == "" || seenPhone[norm] {
			return
		}
		seenPhone[norm] = true

		c := &domain.Contact{
			BusinessID:  businessID,
			ContactType: "general",
			Source:      src,
			Confidence:  confidence,
		}
		if whatsapp {
			c.WhatsApp = norm
		} else {
			c.Phone = norm
		}
		out = append(out, c)
	}

	for _, m := range waLinkRe.FindAllStringSubmatch(html, -1) {
		addPhone(m[1], source+":wa", 0.95, true)
	}
	for _, m := range telRe.FindAllStringSubmatch(html, -1) {
		addPhone(m[1], source+":tel", 0.9, false)
	}

	// Strip tags before the loose body-text scan: raw HTML is full of digit
	// runs (hex colours, asset hashes, timestamps) that look like phone numbers.
	text := StripTags(html)
	for _, m := range phoneRe.FindAllString(text, -1) {
		addPhone(m, source+":text", 0.5, false)
	}

	return out
}

// normalizePhone reduces a number to E.164-ish digits, keeping only the ones
// that can actually be an Indian mobile or landline.
func normalizePhone(raw string) string {
	d := digitsRe.ReplaceAllString(raw, "")

	switch {
	case len(d) == 10:
		// bare national number
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		d = d[1:]
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 13 && strings.HasPrefix(d, "091"):
		d = d[3:]
	default:
		return ""
	}

	// Indian mobile numbers start 6-9. Anything else at 10 digits is noise.
	if d[0] < '6' || d[0] > '9' {
		return ""
	}
	return "+91" + d
}

func plausibleEmail(addr string) bool {
	for _, bad := range emailBlocklist {
		if strings.Contains(addr, bad) {
			return false
		}
	}
	for _, ext := range emailBlockedExt {
		if strings.HasSuffix(addr, ext) {
			return false
		}
	}
	// A local part longer than a domain name is almost always a mangled asset path.
	return len(addr) <= 100
}

func classifyEmail(addr string) string {
	local, _, _ := strings.Cut(addr, "@")
	switch {
	case strings.HasPrefix(local, "sales"), strings.HasPrefix(local, "business"):
		return "sales"
	case strings.HasPrefix(local, "support"), strings.HasPrefix(local, "help"):
		return "support"
	case strings.HasPrefix(local, "info"), strings.HasPrefix(local, "contact"),
		strings.HasPrefix(local, "hello"), strings.HasPrefix(local, "enquiry"):
		return "general"
	default:
		return "general"
	}
}
