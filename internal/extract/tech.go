package extract

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/makeforme/leadscraper/internal/domain"
)

type fingerprint struct {
	Name     string
	Category string
	// HTML patterns are matched case-insensitively against the page body.
	HTML []string
	// Header is a response header whose presence (or value substring) implies
	// the technology.
	HeaderKey   string
	HeaderValue string
	// Version, when set, captures group 1 as the version string.
	Version *regexp.Regexp
}

var fingerprints = []fingerprint{
	{Name: "WordPress", Category: "cms",
		HTML:    []string{"/wp-content/", "/wp-includes/", `name="generator" content="wordpress`},
		Version: regexp.MustCompile(`(?i)content="WordPress ([\d.]+)"`)},
	{Name: "Shopify", Category: "ecommerce",
		HTML: []string{"cdn.shopify.com", "shopify-section", "myshopify.com"}},
	{Name: "Wix", Category: "site-builder",
		HTML: []string{"static.wixstatic.com", "wix.com/website-builder", "_wixCssStates"}},
	{Name: "Squarespace", Category: "site-builder",
		HTML: []string{"squarespace.com", "static1.squarespace.com"}},
	{Name: "Webflow", Category: "site-builder",
		HTML: []string{"assets.website-files.com", `content="webflow"`, "webflow.js"}},
	{Name: "GoDaddy Website Builder", Category: "site-builder",
		HTML: []string{"img1.wsimg.com", "godaddy.com/websites"}},
	{Name: "WooCommerce", Category: "ecommerce",
		HTML: []string{"woocommerce", "/plugins/woocommerce/"}},
	{Name: "Magento", Category: "ecommerce", HTML: []string{"/static/frontend/magento", "mage/cookies"}},
	{Name: "React", Category: "javascript-framework",
		HTML: []string{"__reactcontainer", "data-reactroot", "react-dom"}},
	{Name: "Next.js", Category: "javascript-framework",
		HTML: []string{"/_next/static/", "__NEXT_DATA__"}},
	{Name: "Vue.js", Category: "javascript-framework", HTML: []string{"data-v-app", "vue.min.js", "__vue__"}},
	{Name: "Angular", Category: "javascript-framework", HTML: []string{"ng-version=", "ng-app"}},
	{Name: "jQuery", Category: "javascript-library",
		HTML:    []string{"jquery.min.js", "jquery.js", "jquery-"},
		Version: regexp.MustCompile(`(?i)jquery[/-]?([\d.]+)(?:\.min)?\.js`)},
	{Name: "Bootstrap", Category: "ui-framework",
		HTML:    []string{"bootstrap.min.css", "bootstrap.css", "bootstrap.bundle"},
		Version: regexp.MustCompile(`(?i)bootstrap[/@-]?([\d.]+)`)},
	{Name: "Tailwind CSS", Category: "ui-framework", HTML: []string{"tailwindcss", "tailwind.min.css"}},
	{Name: "Google Analytics", Category: "analytics",
		HTML: []string{"google-analytics.com/analytics.js", "gtag/js?id=ua-", "googletagmanager.com/gtag/js"}},
	{Name: "Google Tag Manager", Category: "analytics", HTML: []string{"googletagmanager.com/gtm.js", "gtm-"}},
	{Name: "Meta Pixel", Category: "analytics", HTML: []string{"connect.facebook.net", "fbq('init'", "fbevents.js"}},
	{Name: "Hotjar", Category: "analytics", HTML: []string{"static.hotjar.com", "hjSiteSettings"}},
	{Name: "Razorpay", Category: "payments", HTML: []string{"checkout.razorpay.com", "razorpay.com/payment"}},
	{Name: "Stripe", Category: "payments", HTML: []string{"js.stripe.com", "stripe.com/v3"}},
	{Name: "PayPal", Category: "payments", HTML: []string{"paypal.com/sdk/js", "paypalobjects.com"}},
	{Name: "Instamojo", Category: "payments", HTML: []string{"instamojo.com"}},
	{Name: "Calendly", Category: "booking", HTML: []string{"calendly.com", "assets.calendly.com"}},
	{Name: "Cloudflare", Category: "cdn", HeaderKey: "Server", HeaderValue: "cloudflare"},
	{Name: "Nginx", Category: "web-server", HeaderKey: "Server", HeaderValue: "nginx"},
	{Name: "Apache", Category: "web-server", HeaderKey: "Server", HeaderValue: "apache"},
	{Name: "PHP", Category: "language", HeaderKey: "X-Powered-By", HeaderValue: "php",
		Version: regexp.MustCompile(`(?i)php/([\d.]+)`)},
	{Name: "ASP.NET", Category: "framework", HeaderKey: "X-Powered-By", HeaderValue: "asp.net"},
	{Name: "Vercel", Category: "hosting", HeaderKey: "Server", HeaderValue: "vercel"},
}

// Technologies fingerprints a page from its HTML body and response headers.
func Technologies(html string, headers http.Header) []domain.Technology {
	lower := strings.ToLower(html)

	var out []domain.Technology
	seen := map[string]bool{}

	for _, fp := range fingerprints {
		if seen[fp.Name] || !fp.matches(lower, headers) {
			continue
		}
		seen[fp.Name] = true

		tech := domain.Technology{Name: fp.Name, Category: fp.Category}
		if fp.Version != nil {
			// Match against the original HTML first, then the headers, since a
			// version can appear in either.
			if m := fp.Version.FindStringSubmatch(html); len(m) > 1 {
				tech.Version = m[1]
			} else if fp.HeaderKey != "" && headers != nil {
				if m := fp.Version.FindStringSubmatch(headers.Get(fp.HeaderKey)); len(m) > 1 {
					tech.Version = m[1]
				}
			}
		}
		out = append(out, tech)
	}

	return out
}

func (fp fingerprint) matches(lowerHTML string, headers http.Header) bool {
	for _, pattern := range fp.HTML {
		if strings.Contains(lowerHTML, strings.ToLower(pattern)) {
			return true
		}
	}

	if fp.HeaderKey != "" && headers != nil {
		val := strings.ToLower(headers.Get(fp.HeaderKey))
		if val != "" && strings.Contains(val, strings.ToLower(fp.HeaderValue)) {
			return true
		}
	}

	return false
}

// Go's regexp is RE2, which has no backreferences, so each element needs its
// own pattern rather than one `<(script|style)>.*?</\1>`.
var (
	scriptRe   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	styleRe    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	noscriptRe = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript\s*>`)
	anyTagRe   = regexp.MustCompile(`<[^>]+>`)
	spacesRe   = regexp.MustCompile(`\s+`)
)

// StripTags reduces HTML to its visible text. Script and style bodies go first,
// since they hold most of the noise that fools the contact regexes.
func StripTags(html string) string {
	s := scriptRe.ReplaceAllString(html, " ")
	s = styleRe.ReplaceAllString(s, " ")
	s = noscriptRe.ReplaceAllString(s, " ")
	s = anyTagRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'",
	).Replace(s)
	return strings.TrimSpace(spacesRe.ReplaceAllString(s, " "))
}
