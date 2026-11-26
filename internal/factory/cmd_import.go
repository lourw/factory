package factory

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type planTask struct {
	title, covers, deps, body string
}

type plan struct {
	title, kind, scope, slug string
	prio                     int
	spec                     string
	tasks                    []planTask
}

var planTaskRe = regexp.MustCompile(`^=== T(\d+): (.+?)(?: \[covers ([^\]]+)\])?(?: \[deps ([^\]]+)\])?$`)
var planDepRe = regexp.MustCompile(`^[Tt]?(\d+)$`)

// parsePlan reads a one-file plan: header lines (title kind scope prio slug)
// until the first `=== ` line, then `=== spec` and `=== T<n>: ...` blocks.
func parsePlan(src string) (plan, error) {
	p := plan{kind: "feat", prio: 2}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	i := 0
	for ; i < len(lines) && !strings.HasPrefix(lines[i], "=== "); i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return p, fmt.Errorf("line %d: header lines are `key: value`", i+1)
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "title":
			p.title = v
		case "kind":
			if !contains(kinds, v) {
				return p, fmt.Errorf("kind must be one of: %s", strings.Join(kinds, ", "))
			}
			p.kind = v
		case "scope":
			p.scope = v
		case "slug":
			p.slug = v
		case "prio":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 4 {
				return p, fmt.Errorf("prio must be 1-4")
			}
			p.prio = n
		default:
			return p, fmt.Errorf("line %d: unknown header key %q (title kind scope prio slug)", i+1, k)
		}
	}
	if p.title == "" {
		return p, fmt.Errorf("missing `title:` header")
	}
	if p.slug == "" {
		p.slug = slugify(p.title)
	}
	hasSpec := false
	var body *string
	for ; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "=== ") {
			*body += l + "\n"
			continue
		}
		h := strings.TrimSpace(l)
		if h == "=== spec" {
			if hasSpec {
				return p, fmt.Errorf("line %d: duplicate === spec", i+1)
			}
			hasSpec, body = true, &p.spec
			continue
		}
		m := planTaskRe.FindStringSubmatch(h)
		if m == nil {
			return p, fmt.Errorf("line %d: expected `=== spec` or `=== T<n>: <title> [covers ...] [deps ...]`", i+1)
		}
		if n, _ := strconv.Atoi(m[1]); n != len(p.tasks)+1 {
			return p, fmt.Errorf("line %d: task T%s out of order (want T%d)", i+1, m[1], len(p.tasks)+1)
		}
		var deps []string
		for _, d := range splitList(m[4]) {
			dm := planDepRe.FindStringSubmatch(d)
			if dm == nil {
				return p, fmt.Errorf("line %d: bad dep %q", i+1, d)
			}
			if n, _ := strconv.Atoi(dm[1]); n < 1 || n > len(p.tasks) {
				return p, fmt.Errorf("line %d: dep %s must be an earlier task", i+1, d)
			}
			deps = append(deps, "T"+dm[1])
		}
		p.tasks = append(p.tasks, planTask{title: strings.TrimSpace(m[2]),
			covers: strings.ToUpper(strings.Join(splitList(m[3]), ",")), deps: strings.Join(deps, ",")})
		body = &p.tasks[len(p.tasks)-1].body
	}
	p.spec = strings.TrimSpace(p.spec)
	if !hasSpec || p.spec == "" {
		return p, fmt.Errorf("missing `=== spec` block")
	}
	for j := range p.tasks {
		p.tasks[j].body = strings.TrimSpace(p.tasks[j].body)
	}
	return p, nil
}

// importPlan creates the item, spec, tasks and packets. p must come from parsePlan.
func importPlan(s *Store, p plan) int {
	r := s.exec("INSERT INTO items(slug,title,kind,scope,priority,status,created,updated) VALUES(?,?,?,?,?,?,?,?)",
		p.slug, p.title, p.kind, p.scope, p.prio, "backlog", now(), now())
	id64, _ := r.LastInsertId()
	id := int(id64)
	s.event(id, "status", "backlog", 0)
	s.putDoc("spec", p.spec, id, 0)
	for j, t := range p.tasks {
		n := j + 1
		s.exec("INSERT INTO tasks(item_id,n,title,covers,deps,updated) VALUES(?,?,?,?,?,?)",
			id, n, t.title, t.covers, t.deps, now())
		if t.body != "" {
			s.putDoc("task", t.body, id, n)
			if len(parseRefs(t.body)) > 0 { // storeRefs needs a git main branch
				s.storeRefs(id, n, t.body)
			}
		}
	}
	s.event(id, "doc", fmt.Sprintf("imported plan (%d tasks)", len(p.tasks)), 0)
	return id
}

func cmdImport(a Args) {
	var src string
	if f := a.pos(0); f != "" && f != "-" {
		b, err := os.ReadFile(f)
		check(err)
		src = string(b)
	} else {
		if !stdinPiped() {
			die("usage: factory import [file|-] [--ready] (plan on stdin when no file)")
		}
		src = readStdin()
	}
	p, err := parsePlan(src)
	if err != nil {
		die("import: %v", err)
	}
	s := cur()
	id := importPlan(s, p)
	fmt.Printf("%s backlog  %s (%d tasks)\n", fid(id), p.title, len(p.tasks))
	if a.Bools["ready"] {
		it := s.item(fid(id))
		if pr := readyProblems(s, it); len(pr) > 0 {
			die("%s created but not ready:\n  - %s\n(fix, then factory ready %d)", fid(id), strings.Join(pr, "\n  - "), id)
		}
		s.setStatus(it, "ready", "")
		fmt.Printf("%s ready. `factory run` will pick it up.\n", fid(id))
	}
}
