package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateCache(t *testing.T) {
	s := newTestStore(t)
	dir := s.root
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".factory/\ncounter\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	exec.Command("git", "-C", dir, "add", "-A").Run()
	exec.Command("git", "-C", dir, "commit", "-qm", "init").Run()
	counter := filepath.Join(dir, "counter")
	s.exec("INSERT INTO gate(name,cmd,ord,known_failing) VALUES('test',?,0,0)", "echo x >> "+counter)
	runs := func() int { b, _ := os.ReadFile(counter); return strings.Count(string(b), "x") }

	k1 := treeKey(dir)
	if k1 == "" || treeKey(dir) != k1 {
		t.Fatalf("treeKey not stable: %q", k1)
	}
	if ok, out := runGate(s, dir, 0, 0, nil); !ok || strings.Contains(out, "cached") || runs() != 1 {
		t.Fatalf("first run: ok=%v runs=%d\n%s", ok, runs(), out)
	}
	if ok, out := runGate(s, dir, 0, 0, nil); !ok || !strings.Contains(out, "PASS test (cached)") || runs() != 1 {
		t.Fatalf("second run should be cached: runs=%d\n%s", runs(), out)
	}
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new untracked\n"), 0o644)
	if treeKey(dir) == k1 {
		t.Fatal("untracked edit should change key")
	}
	if ok, out := runGate(s, dir, 0, 0, nil); !ok || strings.Contains(out, "cached") || runs() != 2 {
		t.Fatalf("after edit should rerun: runs=%d\n%s", runs(), out)
	}
	s.setCfg("gate_cache", "off")
	if _, out := runGate(s, dir, 0, 0, nil); strings.Contains(out, "cached") || runs() != 3 {
		t.Fatalf("gate_cache off should rerun: runs=%d\n%s", runs(), out)
	}
	if st, _ := gitRun(dir, "status", "--porcelain"); !strings.Contains(st, "?? b.txt") {
		t.Fatalf("real index touched: %q", st)
	}
}
