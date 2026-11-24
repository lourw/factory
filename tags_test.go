package main

import "testing"

func TestNextVersion(t *testing.T) {
	cases := []struct{ base, kind, want string }{
		{"v1.2.3", "feat", "v1.3.0"},
		{"v1.2.3", "fix", "v1.2.4"},
		{"v1.2.3", "perf", "v1.2.4"},
		{"v1.2.3", "docs", ""},
		{"", "feat", "v0.1.0"},
		{"", "fix", "v0.0.1"},
		{"", "chore", ""},
		{"1.9.0", "feat", "v1.10.0"},
	}
	for _, c := range cases {
		if got := nextVersion(c.base, c.kind); got != c.want {
			t.Errorf("nextVersion(%q,%q) = %q, want %q", c.base, c.kind, got, c.want)
		}
	}
}

func TestSemverLess(t *testing.T) {
	if !semverLess("v0.9.0", "v0.10.0") || semverLess("v1.0.0", "v0.10.0") {
		t.Fatal("semverLess ordering wrong")
	}
}
