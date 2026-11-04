package main

import "testing"

func TestLevelForEnvironment(t *testing.T) {
	tests := map[string]string{"development": "debug", "production": "info", "": "info", "staging": "info"}
	for env, want := range tests {
		if got := levelForEnvironment(env); got != want {
			t.Errorf("levelForEnvironment(%q) = %q, want %q", env, got, want)
		}
	}
}
