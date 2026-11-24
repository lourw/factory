package main

import "testing"

// TestRemovedSurface pins the cleanup: no notes doc, review template, or task-next.
func TestRemovedSurface(t *testing.T) {
	if itemDocs["notes"] {
		t.Fatal("notes must not be a doc kind")
	}
	if _, err := templatesFS.ReadFile("templates/review.md"); err == nil {
		t.Fatal("templates/review.md must be gone")
	}
	if _, ok := commands["task-next"]; ok {
		t.Fatal("task-next must be removed")
	}
}
