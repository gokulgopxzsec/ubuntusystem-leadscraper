package extract

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/makeforme/leadscraper/internal/domain"
)

var socialHosts = map[string]string{
	"instagram.com": "instagram",
	"facebook.com":  "facebook",
	"fb.com":        "facebook",
	"twitter.com":   "twitter",
	"x.com":         "twitter",
	"linkedin.com":  "linkedin",
	"youtube.com":   "youtube",
	"youtu.be":      "youtube",
	"pinterest.com": "pinterest",
	"t.me":          "telegram",
	"telegram.me":   "telegram",
	"wa.me":         "whatsapp",
	"threads.net":   "threads",
	"tiktok.com":    "tiktok",
}

// Paths that are the platform's own chrome (share buttons, login pages, the
// platform's own corporate account) rather than the business's profile.
var socialNoise = map[string]bool{
	"":          true,
	"sharer":    true,
	"share":     true,
	"share.php": true,
	"login":     true,
	"signup":    true,
	"home":      true,
	"intent":    true,
	"tr":        true,
	"plugins":   true,
	"dialog":    true,
	"policies":  true,
	"about":     true,
	"privacy":   true,
	"legal":     true,
	"watch":     true,
	"embed":     true,
	"widgets":   true,
	"hashtag":   true,
	"explore":   true,
	"p":         true, // instagram post, not a profile
	"reel":      true,
	"story":     true,
}

// Socials finds the business's own social profiles among the links on a page.
func Socials(businessID string, links []string) []*domain.SocialProfile {
	var out []*domain.SocialProfile
	seen := map[string]bool{}

	for _, link := range links {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}

		platform, ok := socialHosts[canonicalHost(u.Host)]
		if !ok {
			continue
		}

		handle := profileHandle(u)
		if handle == "" {
			continue
		}

		// One profile per platform; the schema enforces it anyway.
		if seen[platform] {
			continue
		}
		seen[platform] = true

		out = append(out, &domain.SocialProfile{
			BusinessID: businessID,
			Platform:   platform,
			URL:        u.String(),
		})
	}

	return out
}

// SocialsFromHTML is the fallback for pages whose social links are injected by
// script and so never appear as parsed <a> hrefs.
var socialURLRe = regexp.MustCompile(
	`(?i)https?://(?:www\.)?(?:instagram\.com|facebook\.com|twitter\.com|x\.com|linkedin\.com|youtube\.com|t\.me|tiktok\.com)/[A-Za-z0-9._/\-]+`)

func SocialsFromHTML(businessID, html string) []*domain.SocialProfile {
	return Socials(businessID, socialURLRe.FindAllString(html, -1))
}

// linkInBioHosts are the other things that turn up in a business's "website"
// slot: a link aggregator is not a storefront either.
var linkInBioHosts = map[string]bool{
	"linktr.ee":    true,
	"bio.link":     true,
	"beacons.ai":   true,
	"linkin.bio":   true,
	"lnk.bio":      true,
	"campsite.bio": true,
	"solo.to":      true,
	"carrd.co":     true,
	"about.me":     true,
	"tap.bio":      true,
}

// SocialOnly reports whether a business's "website" is really just a social
// profile or a link-in-bio page.
//
// Google Maps lets a business put anything in its website field, and a great
// many small sellers put their Instagram there. Treating that as a website is
// wrong twice over: the crawler gets blocked by Facebook and reports the site as
// "down", and the business gets pitched as though it had a broken storefront.
// In truth it has no storefront at all, which makes it the strongest kind of
// lead — orders are being taken in DMs.
func SocialOnly(rawURL string) (platform string, ok bool) {
	if strings.TrimSpace(rawURL) == "" {
		return "", false
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}

	host := canonicalHost(u.Host)

	if p, found := socialHosts[host]; found {
		return p, true
	}
	if linkInBioHosts[host] {
		return "link-in-bio", true
	}
	return "", false
}

func canonicalHost(host string) string {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.TrimPrefix(host, "www.")
}

// profileHandle returns the first path segment if it looks like a real profile.
func profileHandle(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}

	first := strings.ToLower(parts[0])
	if socialNoise[first] {
		return ""
	}

	// LinkedIn profiles live one level down: /company/foo, /in/foo.
	if canonicalHost(u.Host) == "linkedin.com" {
		if len(parts) < 2 || (first != "company" && first != "in" && first != "school") {
			return ""
		}
		return parts[1]
	}

	return parts[0]
}
