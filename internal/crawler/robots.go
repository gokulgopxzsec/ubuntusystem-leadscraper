package crawler

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// robotsCache fetches and caches robots.txt per host. Without the cache we
// would refetch robots.txt for every page on a site.
type robotsCache struct {
	client    *http.Client
	userAgent string

	mu      sync.RWMutex
	entries map[string]*robotsEntry
}

type robotsEntry struct {
	disallow  []string
	allow     []string
	fetchedAt time.Time
}

const robotsTTL = time.Hour

func newRobotsCache(client *http.Client, userAgent string) *robotsCache {
	return &robotsCache{
		client:    client,
		userAgent: userAgent,
		entries:   make(map[string]*robotsEntry),
	}
}

func (rc *robotsCache) allowed(ctx context.Context, u *url.URL) (bool, error) {
	entry, err := rc.get(ctx, u)
	if err != nil {
		// A missing or unreachable robots.txt means no restrictions.
		return true, err
	}

	path := u.Path
	if path == "" {
		path = "/"
	}

	// Longest matching rule wins, and Allow beats Disallow at equal length.
	best, allowed := 0, true
	for _, rule := range entry.disallow {
		if rule != "" && strings.HasPrefix(path, rule) && len(rule) > best {
			best, allowed = len(rule), false
		}
	}
	for _, rule := range entry.allow {
		if rule != "" && strings.HasPrefix(path, rule) && len(rule) >= best {
			best, allowed = len(rule), true
		}
	}
	return allowed, nil
}

func (rc *robotsCache) get(ctx context.Context, u *url.URL) (*robotsEntry, error) {
	host := u.Scheme + "://" + u.Host

	rc.mu.RLock()
	entry, ok := rc.entries[host]
	rc.mu.RUnlock()

	if ok && time.Since(entry.fetchedAt) < robotsTTL {
		return entry, nil
	}

	entry, err := rc.fetch(ctx, host)
	if err != nil {
		return nil, err
	}

	rc.mu.Lock()
	rc.entries[host] = entry
	rc.mu.Unlock()

	return entry, nil
}

func (rc *robotsCache) fetch(ctx context.Context, host string) (*robotsEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/robots.txt", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", rc.userAgent)

	resp, err := rc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Anything other than a served robots.txt means "crawl freely".
	if resp.StatusCode != http.StatusOK {
		return &robotsEntry{fetchedAt: time.Now()}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	return parseRobots(string(body)), nil
}

// parseRobots reads the directives that apply to us: the wildcard group plus
// any group naming our agent.
func parseRobots(body string) *robotsEntry {
	entry := &robotsEntry{fetchedAt: time.Now()}

	scanner := bufio.NewScanner(strings.NewReader(body))
	applies := false

	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent":
			agent := strings.ToLower(value)
			applies = agent == "*" || strings.Contains(agent, "leadscraper")
		case "disallow":
			if applies {
				entry.disallow = append(entry.disallow, value)
			}
		case "allow":
			if applies {
				entry.allow = append(entry.allow, value)
			}
		}
	}

	return entry
}
