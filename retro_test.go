package main

import (
	"strings"
	"testing"
)

func TestRetroDigest(t *testing.T) {
	s := newTestStore(t)
	s.putDoc("map", "line1\nline2", 0, 0)
	mapTs := s.docUpdated("map", 0, 0)
	if mapTs == 0 {
		t.Fatal("map docUpdated should be set")
	}
	it := addItem(s, "Retro thing")
	old := addItem(s, "Old merge")
	s.exec("UPDATE items SET status='merged', park_reason='flaky gate' WHERE id IN (?,?)", it.ID, old.ID)
	s.exec("INSERT INTO events(ts,item_id,kind,msg) VALUES(?,?,'status','merged')", mapTs+10, it.ID)
	s.exec("INSERT INTO events(ts,item_id,kind,msg) VALUES(?,?,'status','merged')", mapTs-10, old.ID)
	s.exec("INSERT INTO tasks(item_id,n,title,fixes) VALUES(?,1,'a',2)", it.ID)
	s.putDoc("verify", "ok\n[blocking] x\n[minor] y", it.ID, 1)
	ms := s.mergedSince(mapTs)
	if len(ms) != 1 || ms[0].ID != it.ID {
		t.Fatalf("mergedSince: %+v", ms)
	}
	d := retroDigest(s, mapTs)
	for _, want := range []string{"## Merged since", "Retro thing", "[blocking] x", "flaky gate", "## Fix rounds", "2 lines"} {
		if !strings.Contains(d, want) {
			t.Fatalf("digest missing %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "Old merge") || strings.Contains(d, "[minor]") {
		t.Fatalf("digest has extra content:\n%s", d)
	}
}

func TestShortStat(t *testing.T) {
	for in, want := range map[string]string{
		" 3 files changed, 40 insertions(+), 2 deletions(-)\n": " (3 files, +40/-2)",
		"1 file changed, 5 insertions(+)":                      " (1 file, +5/-0)",
		"1 file changed, 4 deletions(-)":                       " (1 file, +0/-4)",
		"":                                                     "",
		"garbage":                                              "",
	} {
		if got := shortStat(in); got != want {
			t.Errorf("shortStat(%q) = %q, want %q", in, got, want)
		}
	}
}
