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
