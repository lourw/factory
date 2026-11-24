package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Inlining packet "Read first" code into build briefs, so builders don't spend turns reading.

const symbolBlockMax = 60 // lines for a non-brace (indentation) block

var symIdentRe = regexp.MustCompile(`^[A-Za-z_]\w*$`)

// headLines returns path's lines at dir's HEAD, or nil.
func headLines(dir, path string) []string {
	out, err := exec.Command("git", "-C", dir, "show", "HEAD:"+path).Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

// resolveRef turns a packet ref into a 1-based line range at dir's HEAD.
// path:A-B as is; path (symbol) → its definition block; bare path → not ok.
func resolveRef(dir string, r packetRef) (start, end int, ok bool) {
	lines := headLines(dir, r.Path)
	if lines == nil {
		return 0, 0, false
	}
	if r.Start > 0 {
		end = min(max(r.End, r.Start), len(lines))
		return r.Start, end, r.Start <= end
	}
	if r.Symbol == "" {
		return 0, 0, false
	}
	sym := r.Symbol[strings.LastIndexAny(r.Symbol, ".:")+1:]
	if !symIdentRe.MatchString(sym) {
		return 0, 0, false
	}
	pat := fmt.Sprintf(`(func|type|def|class|function) %[1]s|func \([^)]*\) %[1]s|%[1]s =`, sym)
	out, good := gitRun(dir, "grep", "-n", "-w", "-E", pat, "HEAD", "--", r.Path)
	if !good || out == "" {
		return 0, 0, false
	}
	// HEAD:path:N:text
	f := strings.SplitN(strings.SplitN(out, "\n", 2)[0], ":", 4)
	if len(f) < 4 {
		return 0, 0, false
	}
	start, err := strconv.Atoi(f[2])
	if err != nil || start < 1 || start > len(lines) {
		return 0, 0, false
	}
	return start, blockEnd(lines, start), true
}

// blockEnd finds the last line of the block defined at 1-based line start.
func blockEnd(lines []string, start int) int {
	def := strings.TrimRight(lines[start-1], " \t")
	if strings.HasSuffix(def, "{") || strings.HasSuffix(def, "(") {
		for i := start; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "}") || strings.HasPrefix(lines[i], ")") {
				return i + 1
			}
		}
		return min(start+symbolBlockMax-1, len(lines))
	}
	ind := indentOf(def)
	end := start
	for i := start; i < len(lines) && i < start+symbolBlockMax-1; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if indentOf(lines[i]) <= ind {
			break
		}
		end = i + 1
	}
	return end
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

// codeSection inlines the packet's resolvable Read-first refs, up to budget lines total.
// Refs that don't fit are listed as "(not inlined)". "" when nothing resolves.
func codeSection(dir, packet string, budget int) string {
	if !dirExists(dir) {
		return ""
	}
	// Collect resolved ranges per path (first-seen path order), merging
	// overlapping or adjacent ranges so each line is inlined at most once.
	var paths []string
	ranges := map[string][][2]int{}
	for _, r := range parseRefs(packet) {
		s, e, ok := resolveRef(dir, r)
		if !ok {
			continue
		}
		if _, seen := ranges[r.Path]; !seen {
			paths = append(paths, r.Path)
		}
		ranges[r.Path] = append(ranges[r.Path], [2]int{s, e})
	}
	var out, skipped []string
	left := budget
	for _, p := range paths {
		rs := ranges[p]
		sort.Slice(rs, func(i, j int) bool { return rs[i][0] < rs[j][0] })
		merged := rs[:1]
		for _, r := range rs[1:] {
			if last := &merged[len(merged)-1]; r[0] <= last[1]+1 {
				last[1] = max(last[1], r[1])
			} else {
				merged = append(merged, r)
			}
		}
		for _, r := range merged {
			s, e := r[0], r[1]
			ref := fmt.Sprintf("%s:%d-%d", p, s, e)
			if n := e - s + 1; n <= left {
				left -= n
				body := strings.Join(headLines(dir, p)[s-1:e], "\n")
				out = append(out, fmt.Sprintf("`%s`\n```\n%s\n```", ref, body))
			} else {
				skipped = append(skipped, "- "+ref+" (not inlined)")
			}
		}
	}
	if len(skipped) > 0 {
		out = append(out, strings.Join(skipped, "\n"))
	}
	return strings.Join(out, "\n\n")
}

// diffSection inlines the commit's patch, whole files in diff order, up to budget lines;
// files that don't fit are listed with their numstat counts.
func diffSection(dir, sha string, budget int) string {
	if !dirExists(dir) {
		return ""
	}
	patch, err := gitOut(dir, "show", "--format=", "--patch", sha)
	if err != nil || patch == "" {
		return ""
	}
	stat, _ := gitOut(dir, "show", "--format=", "--numstat", sha)
	stats := strings.Split(stat, "\n")
	var chunks []string
	for _, part := range strings.Split("\n"+patch, "\ndiff --git ")[1:] {
		chunks = append(chunks, "diff --git "+strings.TrimRight(part, "\n"))
	}
	var in, skipped []string
	left := budget
	for i, c := range chunks {
		if n := strings.Count(c, "\n") + 1; n <= left {
			left -= n
			in = append(in, c)
			continue
		}
		label := strings.Fields(strings.SplitN(c, "\n", 2)[0])
		path := strings.TrimPrefix(label[len(label)-1], "b/")
		counts := "?/?"
		if i < len(stats) {
			if f := strings.SplitN(stats[i], "\t", 3); len(f) == 3 {
				path, counts = f[2], f[0]+"/-"+f[1]
			}
		}
		skipped = append(skipped, fmt.Sprintf("- %s (+%s, not inlined)", path, counts))
	}
	var out []string
	if len(in) > 0 {
		out = append(out, "```diff\n"+strings.Join(in, "\n")+"\n```")
	}
	if len(skipped) > 0 {
		out = append(out, strings.Join(skipped, "\n"))
	}
	return strings.Join(out, "\n\n")
}
