package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// agentCmdTemplate returns the command template for agent name: FACTORY_AGENT_CMD
// when set, else the opencode default, else (backend claude) a `claude -p` line
// whose prompt body and permission settings are written under .factory/agents.
func (r *runner) agentCmdTemplate(name string) (string, error) {
	if t := os.Getenv("FACTORY_AGENT_CMD"); t != "" {
		return t, nil
	}
	if r.backend != "claude" {
		return defaultAgentCmd, nil
	}
	src, err := agentSource(name)
	if err != nil {
		return "", err
	}
	front, body := splitFrontmatter(string(src))
	dir := filepath.Join(r.root, ".factory", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	promptPath := filepath.Join(dir, name+".prompt.md")
	if err := writeFileAtomic(promptPath, []byte(body)); err != nil {
		return "", err
	}
	allow := claudeAllowedTools(front)
	settings := map[string]any{"permissions": map[string]any{"allow": allow}}
	js, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	settingsPath := filepath.Join(dir, name+".settings.json")
	if err := writeFileAtomic(settingsPath, append(js, '\n')); err != nil {
		return "", err
	}
	budget := ""
	if v := r.s.cfg("claude_max_usd", ""); v != "" {
		budget = " --max-budget-usd " + shellQuote(v)
	}
	return "claude -p --model " + shellQuote(claudeModel(r.s, name)) +
		" --output-format json --permission-mode dontAsk --no-session-persistence --append-system-prompt-file " +
		shellQuote(promptPath) + " --settings " + shellQuote(settingsPath) +
		" --strict-mcp-config --disable-slash-commands --setting-sources project --tools " +
		shellQuote(claudeToolList(allow)) + budget + " -- {message}", nil
}

// claudeToolList is the --tools value for an allowlist: each non-Bash(...)
// entry, plus Bash when any Bash(...) pattern exists, in order, comma-joined.
func claudeToolList(allow []string) string {
	var out []string
	seen := map[string]bool{}
	for _, t := range allow {
		if strings.HasPrefix(t, "Bash(") {
			t = "Bash"
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return strings.Join(out, ",")
}

// agentUsage is the cost and token usage of one agent job (zero when unknown).
type agentUsage struct {
	Cost                                   float64
	In, Out, CacheRead, CacheCreate, Turns int
}

// parseClaudeJSON decodes `claude -p --output-format json` output into the
// final result text and usage. ok is false when out isn't such a JSON object.
func parseClaudeJSON(out string) (text string, u agentUsage, ok bool) {
	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "{") {
		return "", u, false
	}
	var v struct {
		Result *string `json:"result"`
		Cost   float64 `json:"total_cost_usd"`
		Turns  int     `json:"num_turns"`
		Usage  struct {
			In          int `json:"input_tokens"`
			Out         int `json:"output_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Result == nil {
		return "", u, false
	}
	return *v.Result, agentUsage{Cost: v.Cost, In: v.Usage.In, Out: v.Usage.Out,
		CacheRead: v.Usage.CacheRead, CacheCreate: v.Usage.CacheCreate, Turns: v.Turns}, true
}

// claudeModel is the claude model for an agent: claude_model_<role> if set,
// else claude_model, else opus. role is the agent name without "factory-".
func claudeModel(s *Store, agent string) string {
	if m := s.cfg("claude_model_"+strings.TrimPrefix(agent, "factory-"), ""); m != "" {
		return m
	}
	return s.cfg("claude_model", "opus")
}

// writeFileAtomic writes data to a temp file beside path and renames it over
// path, so concurrent readers never see a partially written file.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// agentSource returns the opencode agent file for name, preferring the installed
// copy under opencodeDir()/agents and falling back to the embedded one.
func agentSource(name string) ([]byte, error) {
	if b, err := os.ReadFile(filepath.Join(opencodeDir(), "agents", name+".md")); err == nil {
		return b, nil
	}
	return opencodeFS.ReadFile("agents/" + name + ".md")
}

// splitFrontmatter separates a leading `---` delimited block from the body.
// The body has leading blank lines trimmed. Without frontmatter, body is src.
func splitFrontmatter(src string) (front, body string) {
	lines := strings.SplitAfter(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", src
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			front = strings.Join(lines[1:i], "")
			body = strings.TrimLeft(strings.Join(lines[i+1:], ""), "\r\n")
			return front, body
		}
	}
	return "", src
}

// claudeAllowedTools derives a Claude Code tool allowlist from an opencode
// frontmatter `permission` block: edit: allow → Edit, Write; each bash pattern
// marked allow → Bash(<pattern>). Read, Grep and Glob are always allowed.
func claudeAllowedTools(front string) []string {
	tools := []string{"Read", "Grep", "Glob"}
	seen := map[string]bool{"Read": true, "Grep": true, "Glob": true}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			tools = append(tools, t)
		}
	}
	permIndent, bashIndent := -1, -1
	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if bashIndent >= 0 && indent <= bashIndent {
			bashIndent = -1
		}
		if permIndent >= 0 && indent <= permIndent {
			permIndent = -1
		}
		key, val, ok := splitKV(trimmed)
		if !ok {
			continue
		}
		switch {
		case permIndent < 0:
			if key == "permission" && val == "" {
				permIndent = indent
			}
		case bashIndent >= 0:
			if key != "*" && val == "allow" {
				add("Bash(" + key + ")")
			}
		case key == "bash" && val == "":
			bashIndent = indent
		case key == "edit" && val == "allow":
			add("Edit")
			add("Write")
		}
	}
	return tools
}

// splitKV splits a `key: value` YAML line, unquoting a double-quoted key.
func splitKV(s string) (key, val string, ok bool) {
	if strings.HasPrefix(s, `"`) {
		end := strings.Index(s[1:], `"`)
		if end < 0 {
			return "", "", false
		}
		key, rest := s[1:end+1], strings.TrimSpace(s[end+2:])
		if !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		return key, strings.Trim(strings.TrimSpace(rest[1:]), `"'`), true
	}
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.Trim(strings.TrimSpace(s[i+1:]), `"'`), true
}
