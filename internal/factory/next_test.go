package factory

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNextForMerged(t *testing.T) {
	s := newTestStore(t)
	dir := s.root
	g := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	g("init", "-q", "-b", "main")
	g("config", "user.email", "t@t")
	g("config", "user.name", "t")
	g("commit", "-q", "--allow-empty", "-m", "base")
	g("checkout", "-q", "-b", "factory")
	g("commit", "-q", "--allow-empty", "-m", "feature")
	sha := g("rev-parse", "HEAD")
	g("checkout", "-q", "main")

	it := Item{ID: 1, Status: "merged", MergedSha: sha}
	if got := nextFor(s, it); got != "`factory review`, then `factory land`" {
		t.Fatalf("unlanded: %q", got)
	}
	g("merge", "-q", "--ff-only", "factory")
	if got := nextFor(s, it); got != "-" {
		t.Fatalf("landed: %q", got)
	}
	if got := nextFor(s, Item{ID: 2, Status: "ready"}); got != "`factory run`" {
		t.Fatalf("ready: %q", got)
	}
}
