package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func opencodeDir() string {
	if d := os.Getenv("OPENCODE_CONFIG_DIR"); d != "" {
		return d
	}
	x := os.Getenv("XDG_CONFIG_HOME")
	if x == "" {
		h, _ := os.UserHomeDir()
		x = filepath.Join(h, ".config")
	}
	return filepath.Join(x, "opencode")
}

func installedPath() string { return filepath.Join(dataDir(), "installed.txt") }

func installedList() []string {
	b, err := os.ReadFile(installedPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func writeInstalled(paths []string) {
	check(os.MkdirAll(dataDir(), 0o755))
	body := strings.Join(paths, "\n")
	if body != "" {
		body += "\n"
	}
	check(os.WriteFile(installedPath(), []byte(body), 0o644))
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude")
}

type installEntry struct {
	src    string // path inside the embedded FS (and inside a --link checkout)
	target string
	dir    bool
}

// installEntries lists everything `factory install` manages: opencode agents and commands
// (files), and skills (directories) for both Claude Code and opencode.
func installEntries() []installEntry {
	var out []installEntry
	for _, d := range []string{"agents", "commands"} {
		entries, _ := fs.ReadDir(opencodeFS, d)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				out = append(out, installEntry{d + "/" + e.Name(), filepath.Join(opencodeDir(), d, e.Name()), false})
			}
		}
	}
	skills, _ := fs.ReadDir(opencodeFS, "skills")
	for _, e := range skills {
		if e.IsDir() {
			for _, base := range []string{filepath.Join(claudeDir(), "skills"), filepath.Join(opencodeDir(), "skills")} {
				out = append(out, installEntry{"skills/" + e.Name(), filepath.Join(base, e.Name()), true})
			}
		}
	}
	return out
}

func copyEmbeddedDir(src, target string) error {
	return fs.WalkDir(opencodeFS, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(target, strings.TrimPrefix(strings.TrimPrefix(p, src), "/"))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := opencodeFS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// cmdInstall writes the embedded agents/commands/skills into the opencode and Claude config dirs,
// or symlinks them from a source checkout with --link (edits then apply immediately).
// It never overwrites something it didn't install unless --force.
func cmdInstall(a Args) {
	owned := installedList()
	if a.Bools["uninstall"] {
		var keep []string
		for _, p := range owned {
			if err := os.RemoveAll(p); err == nil {
				fmt.Printf("removed  %s\n", p)
			} else {
				fmt.Printf("failed   %s: %v\n", p, err)
				keep = append(keep, p)
			}
		}
		writeInstalled(keep)
		return
	}
	link, linking := a.flag("link")
	if linking {
		abs, err := filepath.Abs(link)
		check(err)
		link = abs
	}
	entries := installEntries()
	if len(entries) == 0 {
		die("nothing embedded to install")
	}
	for _, e := range entries {
		check(os.MkdirAll(filepath.Dir(e.target), 0o755))
		if _, err := os.Lstat(e.target); err == nil && !contains(owned, e.target) && !a.Bools["force"] {
			fmt.Printf("skip     %s (exists and was not installed by factory; --force to overwrite)\n", e.target)
			continue
		}
		check(os.RemoveAll(e.target))
		if linking {
			src := filepath.Join(link, e.src)
			if _, err := os.Stat(src); err != nil {
				die("--link: %s not found", src)
			}
			check(os.Symlink(src, e.target))
			fmt.Printf("linked   %s -> %s\n", e.target, src)
		} else {
			if e.dir {
				check(copyEmbeddedDir(e.src, e.target))
			} else {
				b, err := opencodeFS.ReadFile(e.src)
				check(err)
				check(os.WriteFile(e.target, b, 0o644))
			}
			fmt.Printf("wrote    %s\n", e.target)
		}
		if !contains(owned, e.target) {
			owned = append(owned, e.target)
		}
	}
	writeInstalled(owned)
}
