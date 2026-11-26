package factory

import (
	"fmt"
	"strconv"
	"strings"
)

// parseSemver parses "vX.Y.Z" (the "v" is optional); ok=false if malformed.
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func semverLess(a, b string) bool {
	x, _ := parseSemver(a)
	y, _ := parseSemver(b)
	for i := range x {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

// nextVersion bumps base for an item kind: feat → minor, fix/perf → patch,
// anything else → "" (no tag). An empty base counts as v0.0.0.
func nextVersion(base, kind string) string {
	v, _ := parseSemver(base)
	switch kind {
	case "feat":
		v[1], v[2] = v[1]+1, 0
	case "fix", "perf":
		v[2]++
	default:
		return ""
	}
	return fmt.Sprintf("v%d.%d.%d", v[0], v[1], v[2])
}

// baseVersion is the highest semver among the repo's v* tags and factory_tags ("" if none).
func baseVersion(s *Store, dir string) string {
	var cands []string
	if out, ok := gitRun(dir, "tag", "--list", "v*"); ok {
		cands = append(cands, strings.Fields(out)...)
	}
	if rows, err := s.db.Query("SELECT tag FROM factory_tags"); err == nil {
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil {
				cands = append(cands, t)
			}
		}
		rows.Close()
	}
	best := ""
	for _, c := range cands {
		if _, ok := parseSemver(c); ok && (best == "" || semverLess(best, c)) {
			best = c
		}
	}
	return best
}

// tagCommit records and creates the next version tag for the item's squash commit
// sha. Returns the tag ("" when the kind gets no tag) and any git error output.
func tagCommit(s *Store, dir string, it Item, sha, subject string) (string, error) {
	v := nextVersion(baseVersion(s, dir), it.Kind)
	if v == "" {
		return "", nil
	}
	if out, ok := gitRun(dir, "tag", "-f", "-a", v, "-m", subject, sha); !ok {
		return v, fmt.Errorf("git tag %s: %s", v, firstLine(out))
	}
	s.exec("INSERT OR REPLACE INTO factory_tags(tag,sha,item_id) VALUES(?,?,?)", v, sha, it.ID)
	return v, nil
}

// syncTags re-points recorded tags at the factory commit carrying their
// item's Factory-Item trailer after factory's history was rewritten.
// Tags whose commit can't be found are reported, never deleted.
func syncTags(s *Store, dir string, logf func(string, ...any)) {
	type row struct {
		tag, sha string
		id       int
	}
	var tags []row
	rows, err := s.db.Query("SELECT tag, COALESCE(sha,''), item_id FROM factory_tags")
	if err != nil {
		return
	}
	for rows.Next() {
		var r row
		if rows.Scan(&r.tag, &r.sha, &r.id) == nil {
			tags = append(tags, r)
		}
	}
	rows.Close()
	for _, r := range tags {
		sha, _ := gitRun(dir, "log", "-1", "--format=%H", "--grep=^Factory-Item: "+fid(r.id)+"$", integrationBranch)
		if sha == "" || strings.ContainsAny(sha, " \n") {
			logf("warning: tag %s: no commit for item %s on %s; left as is", r.tag, fid(r.id), integrationBranch)
			continue
		}
		if sha == r.sha {
			continue
		}
		subject, _ := gitRun(dir, "log", "-1", "--format=%s", sha)
		if out, ok := gitRun(dir, "tag", "-f", "-a", r.tag, "-m", subject, sha); !ok {
			logf("warning: re-pointing tag %s failed: %s", r.tag, firstLine(out))
			continue
		}
		s.exec("UPDATE factory_tags SET sha=? WHERE tag=?", sha, r.tag)
		logf("tag %s → %s", r.tag, sha[:8])
	}
}
