package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Skill struct{ Name, Description, Path, Instructions string }
type MCPServer struct {
	Name, Command string
	Args          []string
	Env           map[string]string
}
type Catalog struct {
	Skills     map[string]Skill
	MCPServers []MCPServer
}

type pluginManifest struct {
	Name       string `json:"name"`
	Skills     string `json:"skills"`
	MCPServers string `json:"mcpServers"`
}

func Load(workspace string) (Catalog, error) {
	catalog := Catalog{Skills: map[string]Skill{}}
	for _, root := range []string{filepath.Join(workspace, ".codex", "skills"), filepath.Join(workspace, ".agents", "skills")} {
		_ = loadSkills(root, "", &catalog)
	}
	if err := loadMCPFile(filepath.Join(workspace, ".mcp.json"), &catalog); err != nil {
		return catalog, err
	}
	pluginRoots := []string{filepath.Join(workspace, "plugins"), filepath.Join(workspace, ".agents", "plugins")}
	for _, root := range pluginRoots {
		matches, _ := filepath.Glob(filepath.Join(root, "*", ".codex-plugin", "plugin.json"))
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var manifest pluginManifest
			if json.Unmarshal(data, &manifest) != nil || manifest.Name == "" {
				continue
			}
			pluginRoot := filepath.Dir(filepath.Dir(path))
			_ = loadSkills(filepath.Join(pluginRoot, "skills"), manifest.Name, &catalog)
			if manifest.Skills != "" {
				_ = loadSkills(filepath.Join(pluginRoot, manifest.Skills), manifest.Name, &catalog)
			}
			mcpPath := filepath.Join(pluginRoot, ".mcp.json")
			if manifest.MCPServers != "" {
				mcpPath = filepath.Join(pluginRoot, manifest.MCPServers)
			}
			if err := loadMCPFile(mcpPath, &catalog); err != nil {
				return catalog, err
			}
		}
	}
	return catalog, nil
}

func loadSkills(root, namespace string, catalog *Catalog) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			path = filepath.Join(path, "SKILL.md")
		}
		if filepath.Base(path) != "SKILL.md" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		name, description := parseFrontmatter(string(data))
		if name == "" {
			name = entry.Name()
		}
		if namespace != "" {
			name = namespace + ":" + name
		}
		catalog.Skills[name] = Skill{Name: name, Description: description, Path: path, Instructions: string(data)}
	}
	return nil
}

func parseFrontmatter(content string) (name, description string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", ""
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return "", ""
	}
	for _, line := range strings.Split(content[4:4+end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			name = strings.Trim(strings.TrimSpace(value), `"'`)
		case "description":
			description = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return
}

func loadMCPFile(path string, catalog *Catalog) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode MCP config %s: %w", path, err)
	}
	if nested, ok := raw["mcpServers"]; ok {
		if err := json.Unmarshal(nested, &raw); err != nil {
			return err
		}
	}
	for name, value := range raw {
		var config struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		}
		if json.Unmarshal(value, &config) != nil || config.Command == "" {
			continue
		}
		catalog.MCPServers = append(catalog.MCPServers, MCPServer{Name: name, Command: config.Command, Args: config.Args, Env: config.Env})
	}
	return nil
}

var skillMention = regexp.MustCompile(`\$([A-Za-z0-9][A-Za-z0-9:_-]*)`)

func (catalog Catalog) Instructions(text string) string {
	matches := skillMention.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text
	}
	var injected strings.Builder
	for _, match := range matches {
		if skill, ok := catalog.Skills[match[1]]; ok {
			fmt.Fprintf(&injected, "\n\n<skill name=%q path=%q>\n%s\n</skill>", skill.Name, skill.Path, skill.Instructions)
		}
	}
	return injected.String()
}
