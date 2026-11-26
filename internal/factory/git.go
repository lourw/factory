package factory

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

const integrationBranch = "factory"

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// worktreeTop is the toplevel of the worktree containing the cwd (where gates and tests run).
// The DB always lives at the main repo root (findRoot).
func worktreeTop() string {
	wd, _ := os.Getwd()
	if top, err := gitOut(wd, "rev-parse", "--show-toplevel"); err == nil {
		return top
	}
	return findRoot()
}

// worktreesBase is where this project's worktrees live: config worktree_root, else
// $XDG_DATA_HOME/factory/worktrees/<project>-<hash>.
func worktreesBase(s *Store) string {
	if p := s.cfg("worktree_root", ""); p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(s.root, p)
		}
		return filepath.Clean(p)
	}
	h := sha1.Sum([]byte(s.root))
	return filepath.Join(dataDir(), "worktrees", filepath.Base(s.root)+"-"+hex.EncodeToString(h[:])[:6])
}

// integrationWorktree is the transient worktree for `factory` (slot "int"), used by run and land.
func integrationWorktree(s *Store) string { return filepath.Join(worktreesBase(s), "_factory") }

func featureDirName(it Item) string { return fid(it.ID) + "-" + it.Slug }

func slotPath(s *Store, n int) string {
	return filepath.Join(worktreesBase(s), fmt.Sprintf("slot-%d", n))
}

func parkedPath(s *Store, it Item) string {
	return filepath.Join(worktreesBase(s), "parked-"+featureDirName(it))
}

// featureWorktree is where an item's worktree is: its slot path while in flight,
// parked-NNN-slug while parked, else the legacy NNN-slug path.
func featureWorktree(s *Store, it Item) string {
	switch {
	case it.Slot > 0:
		return slotPath(s, it.Slot)
	case it.Status == "parked":
		return parkedPath(s, it)
	}
	return filepath.Join(worktreesBase(s), featureDirName(it))
}

// parkWorktree frees an item's slot by moving its worktree to parked-NNN-slug
// (or removing it, keeping the branch, if the move fails).
func parkWorktree(s *Store, it Item) {
	if it.Slot == 0 {
		return
	}
	src, dst := slotPath(s, it.Slot), parkedPath(s, it)
	if !dirExists(src) {
		return
	}
	if dirExists(dst) {
		removeWorktree(s.root, dst)
	}
	if _, ok := gitRun(s.root, "worktree", "move", src, dst); !ok {
		removeWorktree(s.root, src)
	}
}

// slotOf names the slot a worktree directory belongs to: "N" for slot-N, "int" for the
// integration worktree, "main" for the user's checkout, else the directory name.
func slotOf(s *Store, dir string) string {
	base := filepath.Base(dir)
	switch {
	case strings.HasPrefix(base, "slot-"):
		return strings.TrimPrefix(base, "slot-")
	case base == "_factory":
		return "int"
	case filepath.Clean(dir) == filepath.Clean(s.root):
		return "main"
	}
	return base
}

func branchExists(root, name string) bool {
	return exec.Command("git", "-C", root, "show-ref", "--verify", "--quiet", "refs/heads/"+name).Run() == nil
}

func isAncestor(dir, a, b string) bool {
	return exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", a, b).Run() == nil
}

// gitRun runs git in dir and returns combined output and success.
func gitRun(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_MERGE_AUTOEDIT=no")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// treeKey hashes the worktree's full content (tracked + untracked, honoring
// .gitignore) via a temporary index, leaving the real index alone. "" on error.
func treeKey(dir string) string {
	f, err := os.CreateTemp("", "factory-index-*")
	if err != nil {
		return ""
	}
	idx := f.Name()
	f.Close()
	os.Remove(idx) // git wants to create it itself
	defer os.Remove(idx)
	defer os.Remove(idx + ".lock")
	var out []byte
	for _, args := range [][]string{{"read-tree", "HEAD"}, {"add", "-A"}, {"write-tree"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+idx)
		if out, err = cmd.Output(); err != nil {
			return ""
		}
	}
	return strings.TrimSpace(string(out))
}

func isClean(dir string) bool {
	out, ok := gitRun(dir, "status", "--porcelain")
	return ok && out == ""
}

func gitPathExists(dir, name string) bool {
	p, ok := gitRun(dir, "rev-parse", "--git-path", name)
	if !ok {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	_, err := os.Stat(p)
	return err == nil
}

func rebaseInProgress(dir string) bool {
	return gitPathExists(dir, "rebase-merge") || gitPathExists(dir, "rebase-apply")
}

func mergeInProgress(dir string) bool { return gitPathExists(dir, "MERGE_HEAD") }

func conflictedFiles(dir string) []string {
	out, _ := gitRun(dir, "diff", "--name-only", "--diff-filter=U")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// mainBranch returns the branch factory treats as main (meta main_branch, else main/master/current).
func mainBranch(s *Store) string {
	if b := s.meta("main_branch"); b != "" {
		return b
	}
	b := ""
	switch {
	case branchExists(s.root, "main"):
		b = "main"
	case branchExists(s.root, "master"):
		b = "master"
	default:
		cur, err := gitOut(s.root, "symbolic-ref", "--short", "HEAD")
		if err != nil || cur == "" {
			die("can't determine the main branch; pass --main <branch>")
		}
		b = cur
	}
	s.setMeta("main_branch", b)
	return b
}

// ensureIntegration creates the `factory` branch (from main) if missing and a fresh, transient
// worktree for it. Any leftover worktree from a crashed run is removed first.
func ensureIntegration(s *Store) string {
	mb := mainBranch(s)
	if !branchExists(s.root, integrationBranch) {
		git(s.root, "branch", integrationBranch, mb)
	}
	wt := integrationWorktree(s)
	if owner, ok := worktreeOwners(s.root)[integrationBranch]; ok && owner != wt {
		die("the factory branch is checked out in %s; switch away from the factory branch first", owner)
	}
	if dirExists(wt) {
		removeWorktree(s.root, wt)
	}
	gitRun(s.root, "worktree", "prune")
	check(os.MkdirAll(filepath.Dir(wt), 0o755))
	git(s.root, "worktree", "add", "-q", wt, integrationBranch)
	return wt
}

func teardownIntegration(s *Store) {
	removeWorktree(s.root, integrationWorktree(s))
	gitRun(s.root, "worktree", "prune")
}

// ---------------------------------------------------------------- run lock

func lockPath(root string) string { return filepath.Join(root, ".factory", "run.lock") }

// activeRun returns the pid of a live `factory run`, or 0.
func activeRun(root string) int {
	b, err := os.ReadFile(lockPath(root))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0
	}
	return pid
}

func acquireRunLock(root string) {
	if pid := activeRun(root); pid != 0 {
		die("a factory run is already active (pid %d)", pid)
	}
	check(os.WriteFile(lockPath(root), []byte(strconv.Itoa(os.Getpid())), 0o644))
}

func releaseRunLock(root string) { os.Remove(lockPath(root)) }

// worktreeOwners maps branch name → worktree path for every checked-out worktree.
func worktreeOwners(root string) map[string]string {
	out, _ := gitRun(root, "worktree", "list", "--porcelain")
	owners := map[string]string{}
	path := ""
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "worktree "):
			path = strings.TrimPrefix(l, "worktree ")
		case strings.HasPrefix(l, "branch refs/heads/"):
			owners[strings.TrimPrefix(l, "branch refs/heads/")] = path
		}
	}
	return owners
}

func removeWorktree(root, path string) {
	if _, ok := gitRun(root, "worktree", "remove", "--force", path); !ok {
		os.RemoveAll(path)
	}
}

// ---------------------------------------------------------------- land

func cmdLand(a Args) {
	s := cur()
	if pid := activeRun(s.root); pid != 0 {
		die("a factory run is active (pid %d); wait for it to finish", pid)
	}
	mb := mainBranch(s)
	if !branchExists(s.root, integrationBranch) {
		die("no `factory` branch yet (nothing has been merged)")
	}
	owners := worktreeOwners(s.root)
	mainWT, mainOut := owners[mb]
	if mainOut && !isClean(mainWT) {
		die("%s is checked out in %s with uncommitted changes; commit or stash them first", mb, mainWT)
	}
	f := git(s.root, "rev-parse", integrationBranch)
	m := git(s.root, "rev-parse", mb)
	if f == m {
		fmt.Printf("%s is already at factory (%s)\n", mb, f[:8])
		return
	}
	if !isAncestor(s.root, m, f) {
		if owner, ok := owners[integrationBranch]; ok {
			die("%s has diverged and factory must be rebased, but factory is checked out in %s; switch away from the factory branch first", mb, owner)
		}
		fmt.Printf("%s has diverged; rebasing factory onto it in a temporary worktree\n", mb)
		tmp := integrationWorktree(s) // slot "int": same path as during runs, so caches stay valid
		removeWorktree(s.root, tmp)
		gitRun(s.root, "worktree", "prune")
		check(os.MkdirAll(filepath.Dir(tmp), 0o755))
		git(s.root, "worktree", "add", "-q", "--detach", tmp, integrationBranch)
		defer teardownLand(s, tmp)
		if out, ok := gitRun(tmp, "rebase", "-q", mb); !ok {
			gitRun(tmp, "rebase", "--abort")
			die("rebasing factory onto %s hit conflicts (%s).\nRun `factory run` (the overseer resolves them in its pre-merge rebase) or rebase manually, then land again.", mb, firstLine(out))
		}
		if ok, summary := runSetup(s, tmp, 0); !ok {
			die("setup failed in the rebased tree:\n%s", summary)
		}
		ok, summary := runGate(s, tmp, 0, 0, nil)
		fmt.Println(summary)
		if !ok {
			die("gate failed on factory rebased onto %s; not landing", mb)
		}
		nf := git(tmp, "rev-parse", "HEAD")
		git(s.root, "update-ref", "refs/heads/"+integrationBranch, nf, f)
		f = nf
		if s.cfg("tagging", "off") == "semver" {
			syncTags(s, s.root, func(format string, a ...any) { fmt.Printf(format+"\n", a...) })
		}
	}
	n := git(s.root, "rev-list", "--count", m+".."+f)
	if mainOut {
		if out, ok := gitRun(mainWT, "merge", "--ff-only", "-q", integrationBranch); !ok {
			die("fast-forward failed: %s", out)
		}
	} else {
		git(s.root, "update-ref", "refs/heads/"+mb, f, m)
	}
	fmt.Printf("%s → %s (%s commit(s) landed)\n", mb, f[:8], n)
	dir := s.root
	if mainOut {
		dir = mainWT
	}
	if hook := s.cfg("post_land", ""); hook != "" {
		cmd := exec.Command("bash", "-c", hook)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "FACTORY_LANDED_RANGE="+m+".."+f)
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: post_land failed: %v (land already happened)\n", err)
		}
	}
	if a.Bools["push"] {
		remote := s.cfg("remote", "origin")
		if out, ok := gitRun(s.root, "push", "--follow-tags", remote, mb); !ok {
			die("git push %s %s failed: %s", remote, mb, out)
		}
		fmt.Printf("pushed %s to %s\n", mb, remote)
	}
}

func teardownLand(s *Store, tmp string) {
	removeWorktree(s.root, tmp)
	gitRun(s.root, "worktree", "prune")
}

// ---------------------------------------------------------------- gc

var dirIDRe = regexp.MustCompile(`^(\d{3,})-`)

// gc removes worktrees of merged/dropped (and optionally parked) items plus unknown dirs.
// Returns the number of worktrees removed.
func gc(s *Store, parked bool, quiet bool) int {
	base := worktreesBase(s)
	entries, _ := os.ReadDir(base)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		path := filepath.Join(base, e.Name())
		reason := ""
		var it *Item
		name := e.Name()
		if strings.HasPrefix(name, "slot-") {
			// a slot belongs to whichever in-flight item holds that slot number
			n, _ := strconv.Atoi(strings.TrimPrefix(name, "slot-"))
			if rows := s.items("WHERE slot=? AND status IN ('building','judging','merging')", n); len(rows) > 0 {
				continue
			}
			reason = "free slot"
		} else {
			name = strings.TrimPrefix(name, "parked-")
			if m := dirIDRe.FindStringSubmatch(name); m != nil {
				id, _ := strconv.Atoi(m[1])
				if rows := s.items("WHERE id=?", id); len(rows) == 1 && featureDirName(rows[0]) == name {
					it = &rows[0]
				}
			}
			switch {
			case it == nil:
				reason = "unknown"
			case it.Status == "merged" || it.Status == "dropped":
				reason = it.Status
			case it.Status == "parked" && parked:
				reason = "parked"
			case it.Status == "parked" || it.Status == "ready":
				continue // parked, or unparked and waiting for a slot: keep for inspection/resume
			case it.Slot > 0:
				reason = "superseded by its slot worktree"
			default:
				continue
			}
		}
		removeWorktree(s.root, path)
		if it != nil && (it.Status == "merged" || it.Status == "dropped") && branchExists(s.root, branchName(*it)) {
			gitRun(s.root, "branch", "-D", branchName(*it))
		}
		removed++
		if !quiet {
			fmt.Printf("removed %s (%s)\n", path, reason)
		}
	}
	gitRun(s.root, "worktree", "prune")
	// orphan feature branches: item missing, merged or dropped, and not checked out
	refs, _ := gitRun(s.root, "for-each-ref", "--format=%(refname:short)", "refs/heads/factory-wip/")
	owners := worktreeOwners(s.root)
	for _, b := range strings.Fields(refs) {
		if _, out := owners[b]; out {
			continue
		}
		m := dirIDRe.FindStringSubmatch(strings.TrimPrefix(b, "factory-wip/"))
		keep := false
		if m != nil {
			id, _ := strconv.Atoi(m[1])
			if rows := s.items("WHERE id=?", id); len(rows) == 1 && branchName(rows[0]) == b {
				keep = rows[0].Status != "merged" && rows[0].Status != "dropped"
			}
		}
		if !keep {
			gitRun(s.root, "branch", "-D", b)
			if !quiet {
				fmt.Printf("deleted branch %s\n", b)
			}
		}
	}
	return removed
}

func cmdGC(a Args) {
	s := cur()
	n := gc(s, a.Bools["parked"], false)
	fmt.Printf("gc: %d worktree(s) removed\n", n)
}
