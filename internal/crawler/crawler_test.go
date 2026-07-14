package crawler

import (
	"errors"
	"testing"
)

// A boolean "reachable" conflated three very different situations, and the
// pipeline scored all of them as "their website is down". Two of the three are
// sites that work perfectly well.
func TestClassifyDistinguishesBlockedFromDown(t *testing.T) {
	tests := []struct {
		name string
		code int
		err  error
		want Status
	}{
		// Genuinely broken. These are the leads.
		{"dns failure", 0, errors.New("no such host"), StatusDown},
		{"connection refused", 0, errors.New("connection refused"), StatusDown},
		{"timeout", 0, errors.New("context deadline exceeded"), StatusDown},
		{"server error", 500, nil, StatusDown},
		{"bad gateway", 502, nil, StatusDown},
		{"homepage 404s", 404, nil, StatusDown},

		// Up, and refusing to let us in. Scoring these as broken told real
		// businesses their working website did not load.
		{"forbidden (bot wall)", 403, nil, StatusBlocked},
		{"unauthorized", 401, nil, StatusBlocked},
		{"rate limited", 429, nil, StatusBlocked},
		{"method not allowed", 405, nil, StatusBlocked},

		// Fine.
		{"ok", 200, nil, StatusLive},
		{"redirect", 301, nil, StatusLive},
		{"no content", 204, nil, StatusLive},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.code, tc.err); got != tc.want {
				t.Errorf("classify(%d, %v) = %q, want %q", tc.code, tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyNeverCallsABlockedSiteDown(t *testing.T) {
	// The whole point: a 403 must never become a "your website is down" pitch.
	for _, code := range []int{401, 403, 405, 406, 429, 451} {
		if got := classify(code, nil); got == StatusDown {
			t.Errorf("HTTP %d classified as down; the site answered, so it is up", code)
		}
	}
}
