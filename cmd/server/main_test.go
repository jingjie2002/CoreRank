package main

import "testing"

func TestEnvBool(t *testing.T) {
	t.Setenv("CORERANK_BOOL_TEST", "true")
	if !envBool("CORERANK_BOOL_TEST") {
		t.Fatal("true should be parsed as enabled")
	}

	t.Setenv("CORERANK_BOOL_TEST", " off ")
	if envBool("CORERANK_BOOL_TEST") {
		t.Fatal("off should be parsed as disabled")
	}
}
