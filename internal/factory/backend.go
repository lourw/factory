package factory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	if v := claudeEffort(r.s, name); v != "" {
		budget += " --effort " + shellQuote(v)
	}
	return "claude -p --model " + shellQuote(claudeModel(r.s, name)) +
		" --output-format stream-json --verbose --permission-mode dontAsk --no-session-persistence --append-system-prompt-file " +
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

// toolCall is one tool_use from a claude stream: tool name and a short summary.
type toolCall struct{ Name, Summary string }

// parseClaudeJSON decodes `claude -p` json or stream-json output into the
// final result text, usage and (stream only) tool calls. ok is false when out
// is not claude JSON. A stream without a usable result (error or killed run)
// yields the last assistant text plus the result subtype, never the raw stream.
func parseClaudeJSON(out string) (text string, u agentUsage, calls []toolCall, ok bool) {
	out = strings.TrimSpace(out)
	if !strings.Contains(out, "\n") {
		text, u, ok = parseClaudeResult(out)
		return text, u, nil, ok
	}
	last, lastText, subtype, seen := "", "", "", false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		var ev struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Message struct {
				Content []struct {
					Type  string         `json:"type"`
					Text  string         `json:"text"`
					Name  string         `json:"name"`
					Input map[string]any `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil || ev.Type == "" {
			continue
		}
		seen = true
		switch ev.Type {
		case "result":
			last, subtype = line, ev.Subtype
		case "assistant":
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "tool_use":
					calls = append(calls, toolCall{c.Name, toolSummary(c.Input)})
				case "text":
					if strings.TrimSpace(c.Text) != "" {
						lastText = c.Text
					}
				}
			}
		}
	}
	if !seen {
		text, u, ok = parseClaudeResult(out)
		return text, u, nil, ok
	}
	if last != "" {
		if text, u, ok = parseClaudeResult(last); ok {
			return text, u, calls, true
		}
	}
	text = lastText
	if subtype != "" {
		text = strings.TrimSpace(text + "\n[result: " + subtype + "]")
	}
	return text, u, calls, true
}

// formatTools is the job log's "--- tools ---" section; "" when no calls.
func formatTools(calls []toolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n--- tools ---\n")
	for i, c := range calls {
		fmt.Fprintf(&b, "%d. %s %s\n", i+1, c.Name, c.Summary)
	}
	return b.String()
}

// toolSummary is the salient input of a tool call (command, file_path or
// pattern), single-lined and capped at 100 chars.
func toolSummary(in map[string]any) string {
	for _, k := range []string{"command", "file_path", "pattern"} {
		if s, ok := in[k].(string); ok {
			s = strings.Join(strings.Fields(s), " ")
			if r := []rune(s); len(r) > 100 {
				s = string(r[:100])
			}
			return s
		}
	}
	return ""
}

// toolCounts summarizes calls as "Bash:9 Read:5", by count desc then name.
func toolCounts(calls []toolCall) string {
	n := map[string]int{}
	for _, c := range calls {
		n[c.Name]++
	}
	names := make([]string, 0, len(n))
	for k := range n {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if n[names[i]] != n[names[j]] {
			return n[names[i]] > n[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = fmt.Sprintf("%s:%d", k, n[k])
	}
	return strings.Join(parts, " ")
}

// parseClaudeResult decodes one `{"result":...}` object into text and usage.
func parseClaudeResult(out string) (text string, u agentUsage, ok bool) {
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
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", u, false
	}
	u = agentUsage{Cost: v.Cost, In: v.Usage.In, Out: v.Usage.Out,
		CacheRead: v.Usage.CacheRead, CacheCreate: v.Usage.CacheCreate, Turns: v.Turns}
	if v.Result == nil {
		return "", u, false
	}
	return *v.Result, u, true
}

// claudeModel is the claude model for an agent: claude_model_<role> if set,
// else claude_model, else opus. role is the agent name without "factory-".
func claudeModel(s *Store, agent string) string {
	if m := s.cfg("claude_model_"+strings.TrimPrefix(agent, "factory-"), ""); m != "" {
		return m
	}
	return s.cfg("claude_model", "opus")
}

// claudeEffort is the --effort level for an agent: claude_effort_<role> if
// set, else claude_effort, else "" (flag not passed).
func claudeEffort(s *Store, agent string) string {
	if v := s.cfg("claude_effort_"+strings.TrimPrefix(agent, "factory-"), ""); v != "" {
		return v
	}
	return s.cfg("claude_effort", "")
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
