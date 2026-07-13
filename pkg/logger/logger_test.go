package logger

import (
	"testing"
)

func TestNew(t *testing.T) {
	log := New("info", "development")
	if log == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNewWithDebug(t *testing.T) {
	log := New("debug", "production")
	if log == nil {
		t.Fatal("expected non-nil logger")
	}
}
