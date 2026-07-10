package config

import (
	"testing"
	"time"
)

func TestString(t *testing.T) {
	t.Setenv("TEST_STRING_VALUE", " 127.0.0.1:8080 ")
	if got := String("TEST_STRING_VALUE", ":8080"); got != "127.0.0.1:8080" {
		t.Fatalf("String returned %q", got)
	}

	t.Setenv("TEST_STRING_BLANK", " ")
	if got := String("TEST_STRING_BLANK", "fallback"); got != "fallback" {
		t.Fatalf("String blank returned %q", got)
	}
}

func TestBool(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "yes", value: "yes", want: true},
		{name: "on", value: "on", want: true},
		{name: "false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "no", value: "no", want: false},
		{name: "off", value: "off", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_BOOL_VALUE", tt.value)
			if got := Bool("TEST_BOOL_VALUE", !tt.want); got != tt.want {
				t.Fatalf("Bool(%q) returned %v", tt.value, got)
			}
		})
	}

	t.Setenv("TEST_BOOL_INVALID", "maybe")
	if got := Bool("TEST_BOOL_INVALID", true); got != true {
		t.Fatalf("Bool invalid returned %v", got)
	}
}

func TestInt64(t *testing.T) {
	t.Setenv("TEST_INT64_VALUE", "42")
	if got := Int64("TEST_INT64_VALUE", 7); got != 42 {
		t.Fatalf("Int64 returned %d", got)
	}

	t.Setenv("TEST_INT64_INVALID", "not-a-number")
	if got := Int64("TEST_INT64_INVALID", 7); got != 7 {
		t.Fatalf("Int64 invalid returned %d", got)
	}
}

func TestDuration(t *testing.T) {
	t.Setenv("TEST_DURATION_VALUE", "3s")
	if got := Duration("TEST_DURATION_VALUE", time.Second); got != 3*time.Second {
		t.Fatalf("Duration returned %v", got)
	}

	t.Setenv("TEST_DURATION_INVALID", "3000")
	if got := Duration("TEST_DURATION_INVALID", time.Second); got != time.Second {
		t.Fatalf("Duration invalid returned %v", got)
	}
}

func TestDurationMillis(t *testing.T) {
	t.Setenv("TEST_DURATION_MS_VALUE", "250")
	if got := DurationMillis("TEST_DURATION_MS_VALUE", time.Second); got != 250*time.Millisecond {
		t.Fatalf("DurationMillis returned %v", got)
	}

	t.Setenv("TEST_DURATION_MS_NEGATIVE", "-1")
	if got := DurationMillis("TEST_DURATION_MS_NEGATIVE", time.Second); got != time.Second {
		t.Fatalf("DurationMillis negative returned %v", got)
	}
}
