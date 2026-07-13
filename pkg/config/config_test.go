package config

import (
	"context"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Port != 8080 {
		t.Fatalf("expected 8080, got %d", cfg.Port)
	}

	if cfg.Version != "0.1.0" {
		t.Fatalf("expected 0.1.0, got %s", cfg.Version)
	}
}
