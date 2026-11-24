package main

import (
	"fmt"
	"strconv"
	"strings"
)

// cmdRetro prints what changed since the code map was last saved (or --since).
func cmdRetro(a Args) {
	s := cur()
	since := s.docUpdated("map", 0, 0)
	if v := a.Flags["since"]; v != "" {
		// A unix timestamp is a large number; anything else (id, slug) names an item.
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 1e6 {
			since = f
		} else {
			since = s.item(v).Created
		}
	}
	fmt.Print(retroDigest(s, since))
}

// capped renders up to max lines, then "+N more".
func capped(lines []string, max int) []string {
	if len(lines) <= max {
		return lines
	}
	return append(lines[:max:max], fmt.Sprintf("+%d more", len(lines)-max))
}

// shortStat compresses `git show --shortstat` output to " (N files, +I/-D)"; "" if unparsable.
func shortStat(out string) string {
	files, ins, del := -1, 0, 0
	for _, part := range strings.Split(strings.TrimSpace(out), ",") {
		f := strings.Fields(part)
		if len(f) < 2 {
			return ""
		}
		n, err := strconv.Atoi(f[0])
		if err != nil {
			return ""
		}
		switch {
		case strings.HasPrefix(f[1], "file"):
			files = n
		case strings.HasPrefix(f[1], "insertion"):
			ins = n
		case strings.HasPrefix(f[1], "deletion"):
			del = n
		}
	}
	if files < 0 {
		return ""
	}
	unit := "files"
	if files == 1 {
		unit = "file"
	}
	return fmt.Sprintf(" (%d %s, +%d/-%d)", files, unit, ins, del)
}

// retroDigest builds the retro digest; git sections are omitted when git fails.
func retroDigest(s *Store, since float64) string {
	var b strings.Builder
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			lines = []string{"none"}
		}
		fmt.Fprintf(&b, "## %s\n%s\n\n", title, strings.Join(lines, "\n"))
	}
	merged := s.mergedSince(since)
	var ms, parks, fixes []string
	firstSha := ""
	for _, it := range merged {
		line := fmt.Sprintf("%s %s: %s", fid(it.ID), kindScope(it), it.Title)
		if it.MergedSha != "" {
			if out, ok := gitRun(s.root, "show", "--shortstat", "--format=", it.MergedSha); ok {
				line += shortStat(out)
			}
		}
		ms = append(ms, line)
		if firstSha == "" && it.MergedSha != "" {
			firstSha = it.MergedSha
		}
		if it.ParkReason != "" {
			parks = append(parks, fmt.Sprintf("%s parked: %s", fid(it.ID), it.ParkReason))
		}
		for _, t := range s.tasks(it) {
			if t.Fixes > 0 {
				fixes = append(fixes, fmt.Sprintf("%s T%d: %d (%s)", fid(it.ID), t.N, t.Fixes, t.Title))
			}
			for _, l := range strings.Split(s.getDoc("verify", it.ID, t.N), "\n") {
				if l = strings.TrimSpace(l); strings.HasPrefix(l, "- [blocking]") {
					parks = append(parks, fmt.Sprintf("%s T%d %s", fid(it.ID), t.N, l))
				}
			}
		}
	}
	section("Merged since", capped(ms, 20))
	if firstSha != "" {
		if out, ok := gitRun(s.root, "diff", "--stat=100", firstSha+"^", integrationBranch); ok && out != "" {
			lines := strings.Split(out, "\n")
			last := lines[len(lines)-1]
			section("Files changed", append([]string{last}, capped(lines[:len(lines)-1], 15)...))
		}
	}
	section("Parks and findings", capped(parks, 10))
	section("Fix rounds", capped(fixes, 10))
	m := "none"
	if body := s.getDoc("map", 0, 0); body != "" {
		m = fmt.Sprintf("%d lines", len(strings.Split(strings.TrimRight(body, "\n"), "\n")))
	}
	section("Map", []string{m})
	return b.String()
}
