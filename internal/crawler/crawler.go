package crawler

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/makeforme/leadscraper/pkg/config"
)

// Page is one fetched and parsed document.
type Page struct {
	URL        string
	StatusCode int
	HTML       string
	Title      string
	MetaTags   map[string]string
	Links      []string
	Headers    http.Header
	LoadTime   time.Duration
	Err        error
}

// Result is everything a single-site crawl produced.
type Result struct {
	BaseURL      string
	Pages        []Page
	Reachable    bool
	HasSSL       bool
	StatusCode   int
	LoadTimeMs   int
	Title        string
	MetaDesc     string
	HasViewport  bool
	HasContactForm bool
	HasBooking   bool
	Error        string
}

type Crawler struct {
	client *http.Client
	cfg    config.CrawlerConfig
	robots *robotsCache
}

func New(cfg config.CrawlerConfig) *Crawler {
	// A lead scraper touches thousands of unknown hosts. Bound every dial and
	// read so one tarpit host cannot pin a worker forever.
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 15 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     false,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			return nil
		},
	}

	return &Crawler{
		client: client,
		cfg:    cfg,
		robots: newRobotsCache(client, cfg.UserAgent),
	}
}

// Crawl fetches the landing page plus up to MaxPages-1 same-host pages that
// look like they carry contact details.
func (c *Crawler) Crawl(ctx context.Context, rawURL string) (*Result, error) {
	base, err := normalizeURL(rawURL)
	if err != nil {
		return &Result{BaseURL: rawURL, Error: err.Error()}, nil
	}

	res := &Result{BaseURL: base.String()}

	if c.cfg.RespectRobots {
		allowed, err := c.robots.allowed(ctx, base)
		if err == nil && !allowed {
			res.Error = "disallowed by robots.txt"
			return res, nil
		}
	}

	// SSL is a property of the URL, not of the fetch. Deriving it after the
	// error check meant a site that is https but simply down got reported as
	// "no HTTPS", which is a wrong reason to put in front of a salesperson.
	res.HasSSL = strings.EqualFold(base.Scheme, "https")

	landing := c.fetch(ctx, base.String())
	res.Pages = append(res.Pages, landing)

	if landing.Err != nil {
		res.Error = landing.Err.Error()
		return res, nil
	}

	res.Reachable = landing.StatusCode > 0 && landing.StatusCode < 400
	res.StatusCode = landing.StatusCode
	res.LoadTimeMs = int(landing.LoadTime.Milliseconds())
	res.Title = landing.Title
	res.MetaDesc = landing.MetaTags["description"]
	_, res.HasViewport = landing.MetaTags["viewport"]
	res.HasContactForm = hasContactForm(landing.HTML)
	res.HasBooking = hasBookingSignal(landing.HTML)

	for _, next := range c.interestingLinks(base, landing.Links) {
		if len(res.Pages) >= c.cfg.MaxPages {
			break
		}

		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(c.cfg.DelayBetween):
		}

		page := c.fetch(ctx, next)
		res.Pages = append(res.Pages, page)

		if page.Err == nil {
			res.HasContactForm = res.HasContactForm || hasContactForm(page.HTML)
			res.HasBooking = res.HasBooking || hasBookingSignal(page.HTML)
		}
	}

	return res, nil
}

func (c *Crawler) fetch(ctx context.Context, rawURL string) Page {
	page := Page{URL: rawURL, MetaTags: map[string]string{}}
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		page.Err = fmt.Errorf("build request: %w", err)
		return page
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-IN,en;q=0.9")

	resp, err := c.client.Do(req)
	if err != nil {
		page.Err = fmt.Errorf("fetch %s: %w", rawURL, err)
		page.LoadTime = time.Since(start)
		return page
	}
	defer resp.Body.Close()

	page.StatusCode = resp.StatusCode
	page.Headers = resp.Header

	// Cap the read: some hosts stream unbounded bodies, and this process has
	// only a couple of gigabytes to play with.
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes))
	page.LoadTime = time.Since(start)
	if err != nil {
		page.Err = fmt.Errorf("read body %s: %w", rawURL, err)
		return page
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "html") {
		page.Err = fmt.Errorf("skipping non-html content-type %q", ct)
		return page
	}

	page.HTML = string(body)
	page.Title, page.MetaTags, page.Links = parseHTML(page.HTML, rawURL)
	return page
}

// parseHTML pulls the title, meta tags, and absolute links out of a document.
func parseHTML(body, baseURL string) (title string, metas map[string]string, links []string) {
	metas = map[string]string{}

	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return "", metas, nil
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return "", metas, nil
	}

	seen := map[string]bool{}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if title == "" && n.FirstChild != nil {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				name, content := "", ""
				for _, a := range n.Attr {
					switch strings.ToLower(a.Key) {
					case "name", "property":
						name = strings.ToLower(a.Val)
					case "content":
						content = a.Val
					}
				}
				if name != "" && content != "" {
					metas[name] = content
				}
			case "a":
				for _, a := range n.Attr {
					if strings.ToLower(a.Key) != "href" {
						continue
					}
					abs, err := base.Parse(a.Val)
					if err != nil {
						continue
					}
					abs.Fragment = ""
					s := abs.String()
					if !seen[s] {
						seen[s] = true
						links = append(links, s)
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	return title, metas, links
}

// interestingLinks keeps same-host pages whose path suggests contact details,
// which is where emails and phone numbers actually live.
func (c *Crawler) interestingLinks(base *url.URL, links []string) []string {
	keywords := []string{"contact", "about", "reach", "connect", "support",
		"enquiry", "inquiry", "book", "appointment", "team", "impressum"}

	var out []string
	seen := map[string]bool{}

	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil || !strings.EqualFold(u.Host, base.Host) {
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}

		path := strings.ToLower(u.Path)
		for _, kw := range keywords {
			if strings.Contains(path, kw) && !seen[u.String()] {
				seen[u.String()] = true
				out = append(out, u.String())
				break
			}
		}
	}
	return out
}

// normalizeURL fills in a scheme when the source gave a bare host, and rejects
// anything that is not an http(s) URL.
func normalizeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty url")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host in %q", raw)
	}
	return u, nil
}

func hasContactForm(body string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "<form") {
		return false
	}
	for _, hint := range []string{"email", "message", "contact", "name=\"name\"", "phone", "enquiry"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func hasBookingSignal(body string) bool {
	lower := strings.ToLower(body)
	for _, hint := range []string{"book now", "book a", "schedule a", "calendly",
		"appointment", "reserve a table", "make a reservation", "booking"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}
