package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExcerptContext(t *testing.T) {
	log := filepath.Join(t.TempDir(), "x.log")
	body := "=== RUN   TestX\n--- FAIL: TestX (0.00s)\n    x_test.go:12: got 1, want 2\nok  other\nfiller\nfiller\nfiller\nFAIL\tpkg\t0.01s\n"
	if err := os.WriteFile(log, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := excerpt(log)
	for _, want := range []string{"2: --- FAIL: TestX", "3:     x_test.go:12: got 1, want 2", "  | …", "8: FAIL\tpkg"} {
		if !strings.Contains(got, want) {
			t.Fatalf("excerpt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1: === RUN") || strings.Contains(got, "7: filler") {
		t.Fatalf("excerpt has non-context lines:\n%s", got)
	}
	t.Setenv("FACTORY_TAIL", "")
	old := tailLines
	tailLines = 2
	defer func() { tailLines = old }()
	if got := excerpt(log); strings.Count(got, "\n") != 1 || !strings.Contains(got, "8: FAIL") {
		t.Fatalf("cap not applied to last lines:\n%s", got)
	}
}

func TestBuilderPromptNoPipe(t *testing.T) {
	for _, f := range []string{"agents/factory-builder.md", "agents/factory-verifier.md"} {
		b, err := opencodeFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "head/tail") {
			t.Fatalf("%s lacks the no-pipe rule", f)
		}
	}
}
