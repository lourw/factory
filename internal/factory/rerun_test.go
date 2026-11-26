package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkRerun(t *testing.T) {
	s := newTestStore(t)
	s.setCfg("worktree_root", t.TempDir())
	it := addItem(s, "Rerun me")
	s.exec("UPDATE items SET slot=1, status='judging' WHERE id=?", it.ID)
	it = s.item("1")
	dir := featureWorktree(s, it)
	os.MkdirAll(dir, 0o755)
	gitT := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitT("init", "-q")
	gitT("config", "user.email", "t@t")
	gitT("config", "user.name", "t")
	commit := func(name string) string {
		os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644)
		gitT("add", "-A")
		gitT("commit", "-qm", name)
		return gitT("rev-parse", "HEAD")
	}
	commit("base")
	s1, s2, s3 := commit("t1"), commit("t2"), commit("t3")
	for n, sha := range []string{s1, s2, s3} {
		s.exec("INSERT INTO tasks(item_id,n,title,status,sha,fixes) VALUES(?,?,'x','verified',?,2)", it.ID, n+1, sha)
	}
	got := s.markRerun(it, 2, "packet changed")
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("affected: want [2 3], got %v", got)
	}
	ts := s.tasks(it)
	if ts[0].Status != "verified" || ts[0].Sha != s1 {
		t.Fatalf("T1 untouched: %+v", ts[0])
	}
	for _, tk := range ts[1:] {
		if tk.Status != "todo" || tk.Sha != "" || tk.Fixes != 0 {
			t.Fatalf("reset: %+v", tk)
		}
	}
	if st := s.item("1").Status; st != "building" {
		t.Fatalf("judging → building, got %s", st)
	}
	if rf := s.rerunFrom(it.ID); rf != s1 {
		t.Fatalf("rerun_from: want %s, got %s", s1, rf)
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM events WHERE item_id=? AND msg LIKE 'rerun: packet changed%'", it.ID).Scan(&n)
	if n == 0 {
		t.Fatal("no rerun event")
	}
}

func TestSpecWarnings(t *testing.T) {
	s := newTestStore(t)
	it := addItem(s, "Warn")
	s.putDoc("spec", "- **AC1**: one\n- **AC2**: two", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers) VALUES(?,1,'a','AC1')", it.ID)
	if w := specWarnings(s, it); len(w) != 0 {
		t.Fatalf("backlog: no warnings, got %v", w)
	}
	s.setStatus(it, "building", "")
	w := specWarnings(s, s.item("1"))
	if len(w) != 1 || !strings.Contains(w[0], "AC2") || !strings.Contains(w[0], "factory task-add") {
		t.Fatalf("want AC2 warning with task-add hint, got %v", w)
	}
}

func TestDroppedTask(t *testing.T) {
	if !contains(taskStatuses, "dropped") || mark["dropped"] != "[-]" {
		t.Fatal("dropped status/mark")
	}
	ts := []Task{{N: 1, Status: "dropped"}, {N: 2, Status: "todo", Deps: "T1"}}
	if got := nextRunnable(ts, nil); got == nil || got.N != 2 {
		t.Fatalf("dropped skipped and satisfies deps: got %+v", got)
	}
	if nextRunnable([]Task{{N: 1, Status: "dropped"}}, nil) != nil {
		t.Fatal("dropped task must not run")
	}
	if !allVerified([]Task{{N: 1, Status: "verified"}, {N: 2, Status: "dropped"}}) {
		t.Fatal("dropped counts as done")
	}
	s := newTestStore(t)
	it := addItem(s, "Drop")
	s.putDoc("spec", "- **AC1**: one", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers,status) VALUES(?,1,'a','AC1','dropped')", it.ID)
	s.exec("INSERT INTO tasks(item_id,n,title,covers) VALUES(?,2,'b','')", it.ID)
	if p := readyProblems(s, it); len(p) != 1 || !strings.Contains(p[0], "AC1") {
		t.Fatalf("dropped covers ignored: %v", p)
	}
}
