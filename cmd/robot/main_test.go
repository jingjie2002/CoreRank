package main

import "testing"

func TestEnvIntOrDefault(t *testing.T) {
	t.Setenv("ROBOT_TEST_INT", "12")
	if got := envIntOrDefault("ROBOT_TEST_INT", 3); got != 12 {
		t.Fatalf("expected configured value, got %d", got)
	}

	t.Setenv("ROBOT_TEST_INT", "-1")
	if got := envIntOrDefault("ROBOT_TEST_INT", 3); got != 3 {
		t.Fatalf("expected fallback for invalid value, got %d", got)
	}
}
