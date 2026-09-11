package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func loadHooks(pluginRoot, manifestPath string) []Hook {
	paths := []string{filepath.Join(pluginRoot, "hooks.json")}
	if resolved := resolvePluginPath(pluginRoot, manifestPath); resolved != "" {
		paths = append([]string{resolved}, paths...)
	}
	seen := map[string]bool{}
	var hooks []Hook
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		hooks = append(hooks, parseHooks(path, data)...)
	}
	return hooks
}

func parseHooks(path string, data []byte) []Hook {
	var asList []Hook
	if json.Unmarshal(data, &asList) == nil && len(asList) > 0 {
		for i := range asList {
			if asList[i].Path == "" {
				asList[i].Path = path
			}
			if asList[i].Type == "" {
				asList[i].Type = "command"
			}
		}
		return asList
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	if nested, ok := raw["hooks"]; ok {
		return parseHooks(path, nested)
	}
	var hooks []Hook
	for event, value := range raw {
		var items []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		if json.Unmarshal(value, &items) != nil {
			continue
		}
		for _, item := range items {
			hookType := item.Type
			if hookType == "" {
				hookType = "command"
			}
			hooks = append(hooks, Hook{Event: event, Type: hookType, Command: item.Command, Path: path})
		}
	}
	return hooks
}

func loadApps(pluginRoot, manifestPath string) []App {
	paths := []string{filepath.Join(pluginRoot, ".app.json")}
	if resolved := resolvePluginPath(pluginRoot, manifestPath); resolved != "" {
		paths = append([]string{resolved}, paths...)
	}
	var apps []App
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var list []App
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			for i := range list {
				if list[i].Path == "" {
					list[i].Path = path
				}
			}
			apps = append(apps, list...)
			continue
		}
		var single App
		if json.Unmarshal(data, &single) == nil && (single.Name != "" || single.Description != "") {
			single.Path = path
			if single.Name == "" {
				single.Name = filepath.Base(pluginRoot)
			}
			apps = append(apps, single)
		}
	}
	return apps
}

func loadCommands(pluginRoot, manifestPath string) []Command {
	roots := []string{filepath.Join(pluginRoot, "commands")}
	if resolved := resolvePluginPath(pluginRoot, manifestPath); resolved != "" {
		roots = append([]string{resolved}, roots...)
	}
	var commands []Command
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if command, ok := readCommandFile(root); ok {
				commands = append(commands, command)
			}
			continue
		}
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".md") && !strings.HasSuffix(entry.Name(), ".toml")) {
				continue
			}
			if command, ok := readCommandFile(filepath.Join(root, entry.Name())); ok {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

func readCommandFile(path string) (Command, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Command{}, false
	}
	name, description := parseFrontmatter(string(data))
	if name == "" {
		name = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".md"), ".toml")
	}
	return Command{Name: name, Description: description, Path: path, Prompt: strings.TrimSpace(string(data))}, true
}

func loadAgents(pluginRoot, manifestPath string) []Agent {
	roots := []string{filepath.Join(pluginRoot, "agents")}
	if resolved := resolvePluginPath(pluginRoot, manifestPath); resolved != "" {
		roots = append([]string{resolved}, roots...)
	}
	var agents []Agent
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".toml") && !strings.HasSuffix(entry.Name(), ".md")) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			name, description := parseFrontmatter(string(data))
			if name == "" {
				name = parseTOMLString(string(data), "name")
			}
			if description == "" {
				description = parseTOMLString(string(data), "description")
			}
			if name == "" {
				name = strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".toml"), ".md")
			}
			agents = append(agents, Agent{Name: name, Description: description, Path: path})
		}
	}
	return agents
}

func parseTOMLString(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		prefix := key + " "
		if !strings.HasPrefix(line, key+"=") && !strings.HasPrefix(line, prefix+"=") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}
