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
