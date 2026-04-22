package codaexit

import "testing"

func TestExitCodeStability(t *testing.T) {
	// These values are documented in docs/v2-lifecycle.md and
	// consumed by external callers. Changing any of them is a
	// BREAKING CHANGE that requires a major version bump.
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"Success", Success, 0},
		{"UserError", UserError, 1},
		{"DBError", DBError, 2},
		{"LifecycleBlocked", LifecycleBlocked, 3},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (breaking change)", c.name, c.got, c.want)
		}
	}
}
