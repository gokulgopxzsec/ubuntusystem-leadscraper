package postgres

import (
	"strings"
	"testing"
)

// pgx has no native codec for pgvector, so the value has to arrive as a text
// literal. Getting this format wrong fails loudly; getting the precision wrong
// fails silently, quietly shifting every similarity score.
func TestVectorLiteralFormat(t *testing.T) {
	tests := []struct {
		name string
		in   []float32
		want string
	}{
		{"empty", nil, "[]"},
		{"single", []float32{0.5}, "[0.5]"},
		{"several", []float32{1, -0.25, 0}, "[1,-0.25,0]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vector(tc.in); got != tc.want {
				t.Errorf("vector(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestVectorRoundTripsFloat32Exactly(t *testing.T) {
	// FormatFloat with -1 precision gives the shortest string that parses back to
	// the same float32. A fixed precision would round, and a rounded embedding is
	// a subtly wrong embedding.
	got := vector([]float32{0.1, 0.123456789})

	if strings.Contains(got, "0.10000000149") {
		t.Errorf("float64 widening leaked into the literal: %s", got)
	}
	if !strings.HasPrefix(got, "[0.1,") {
		t.Errorf("0.1 should render as 0.1, got %s", got)
	}
}

func TestVectorHasNoSpaces(t *testing.T) {
	// pgvector's parser is strict about its literal syntax.
	if got := vector([]float32{1, 2, 3}); strings.Contains(got, " ") {
		t.Errorf("vector literal must not contain spaces, got %q", got)
	}
}

// paginate used to fall back to the DEFAULT when the limit was out of range, so
// asking for 500 silently returned 50. A caller trying to act on every lead would
// quietly act on only the first fifty -- which is exactly what happened when
// re-scanning 104 leads queued 50 of them and said nothing.
func TestPaginateClampsRatherThanSilentlyShrinking(t *testing.T) {
	tests := []struct {
		name       string
		page, size int
		wantLimit  int
		wantOffset int
	}{
		{"normal", 1, 50, 50, 0},
		{"second page", 2, 50, 50, 50},
		{"zero means default", 1, 0, defaultPageSize, 0},
		{"negative means default", 1, -5, defaultPageSize, 0},

		// The bug: these used to come back as 50.
		{"over the ceiling clamps to the ceiling", 1, 5000, maxPageSize, 0},
		{"far over the ceiling still clamps", 1, 100000, maxPageSize, 0},

		{"page zero is page one", 0, 50, 50, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limit, offset := paginate(tc.page, tc.size)
			if limit != tc.wantLimit || offset != tc.wantOffset {
				t.Errorf("paginate(%d, %d) = (%d, %d), want (%d, %d)",
					tc.page, tc.size, limit, offset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

func TestPaginateNeverReturnsFewerThanAskedWithinTheCeiling(t *testing.T) {
	// Anything at or under the ceiling must be honoured exactly.
	for _, size := range []int{1, 50, 100, 200, 499, 500} {
		if limit, _ := paginate(1, size); limit != size {
			t.Errorf("paginate(1, %d) gave %d; a limit within the ceiling must be honoured", size, limit)
		}
	}
}
