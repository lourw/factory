package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInlineRefs(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	var big strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&big, "// filler %d\n", i)
	}
	src := "package x\n\n// Add adds.\nfunc Add(a, b int) int {\n\tif a > 0 {\n\t\treturn a + b\n\t}\n\treturn b\n}\n\n" +
		"type T struct{}\n\nfunc (t *T) Run() {\n\tAdd(1, 2)\n}\n" + big.String()
	os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644)
	exec.Command("git", "-C", dir, "add", "-A").Run()
	exec.Command("git", "-C", dir, "commit", "-qm", "init").Run()

	if s, e, ok := resolveRef(dir, packetRef{Path: "x.go", Symbol: "Add"}); !ok || s != 4 || e != 9 {
		t.Fatalf("Add: %d-%d %v", s, e, ok)
	}
	if s, e, ok := resolveRef(dir, packetRef{Path: "x.go", Symbol: "T.Run"}); !ok || s != 13 || e != 15 {
		t.Fatalf("Run: %d-%d %v", s, e, ok)
	}
	if s, e, ok := resolveRef(dir, packetRef{Path: "x.go", Symbol: "T"}); !ok || s != 11 || e != 11 {
		t.Fatalf("T: %d-%d %v", s, e, ok)
	}
	if _, _, ok := resolveRef(dir, packetRef{Path: "x.go"}); ok {
		t.Fatal("bare path should not resolve")
	}

	packet := "## Goal\ng\n## Read first\n- `x.go (Add)`: add\n- `x.go:13-15`: run\n- `x.go`: whole\n- `x.go:20-170`: filler\n## Do\n- d"
	code := codeSection(dir, packet, 400)
	for _, want := range []string{"`x.go:4-9`\n```\nfunc Add(a, b int) int {", "\treturn b\n}\n```", "`x.go:13-15`\n```\nfunc (t *T) Run() {", "// filler 140"} {
		if !strings.Contains(code, want) {
			t.Fatalf("missing %q in:\n%s", want, code)
		}
	}
	if strings.Contains(code, "not inlined") || strings.Contains(code, "package x") {
		t.Fatalf("unexpected:\n%s", code)
	}
	small := codeSection(dir, packet, 120)
	if !strings.Contains(small, "`x.go:4-9`") || !strings.Contains(small, "x.go:20-165 (not inlined)") || strings.Contains(small, "filler 1\n") {
		t.Fatalf("opencode budget:\n%s", small)
	}
	if codeSection(dir, "## Read first\n- `x.go`: whole\n", 120) != "" {
		t.Fatal("nothing resolves: want empty")
	}
}

func TestCodeSectionDedupe(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	var src strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&src, "line %d\n", i)
	}
	os.WriteFile(filepath.Join(dir, "y.go"), []byte(src.String()), 0o644)
	os.WriteFile(filepath.Join(dir, "z.go"), []byte(src.String()), 0o644)
	exec.Command("git", "-C", dir, "add", "-A").Run()
	exec.Command("git", "-C", dir, "commit", "-qm", "init").Run()

	packet := "## Read first\n- `y.go:5-15`: a\n- `z.go:1-2`: z\n- `y.go:10-20`: b\n- `y.go:21-22`: adjacent\n- `y.go:30-31`: apart\n"
	code := codeSection(dir, packet, 400)
	if n := strings.Count(code, "line 12\n"); n != 1 {
		t.Fatalf("line 12 inlined %d times:\n%s", n, code)
	}
	a, b, c := strings.Index(code, "`y.go:5-22`"), strings.Index(code, "`y.go:30-31`"), strings.Index(code, "`z.go:1-2`")
	if a < 0 || b < 0 || c < 0 || a > b || b > c {
		t.Fatalf("want y.go:5-22, y.go:30-31, z.go:1-2 in order:\n%s", code)
	}
}
