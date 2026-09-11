package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Skill struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Path         string `json:"path"`
	Instructions string `json:"-"`
}

type MCPServer struct {
	Name             string
	Command          string
	Args             []string
	Env              map[string]string
	URL              string
	Headers          map[string]string
	StartupTimeout   time.Duration
	SourcePath       string
	WorkingDirectory string
}

type Hook struct {
	Event   string `json:"event"`
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
}

type App struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	Prompt      string `json:"prompt,omitempty"`
}

type Agent struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

type Plugin struct {
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	Description string    `json:"description,omitempty"`
	Path        string    `json:"path"`
	Enabled     bool      `json:"enabled"`
	Installed   bool      `json:"installed"`
	Source      string    `json:"source,omitempty"`
	Hooks       []Hook    `json:"hooks,omitempty"`
	Apps        []App     `json:"apps,omitempty"`
	Commands    []Command `json:"commands,omitempty"`
	Agents      []Agent   `json:"agents,omitempty"`
}

type Catalog struct {
	Skills     map[string]Skill
	MCPServers []MCPServer
	Plugins    []Plugin
}

type pluginManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Skills      string          `json:"skills"`
	MCPServers  json.RawMessage `json:"mcpServers"`
	Hooks       string          `json:"hooks"`
	Apps        string          `json:"apps"`
	Commands    string          `json:"commands"`
	Agents      string          `json:"agents"`
}

func Load(workspace string) (Catalog, error) {
	catalog := Catalog{Skills: map[string]Skill{}}
	disabled := loadDisabledPlugins(workspace)
	for _, root := range []string{filepath.Join(workspace, ".codex", "skills"), filepath.Join(workspace, ".agents", "skills")} {
		_ = loadSkills(root, "", &catalog)
	}
	if err := loadMCPFile(filepath.Join(workspace, ".mcp.json"), &catalog); err != nil {
		return catalog, err
	}
	for index := range catalog.MCPServers {
		catalog.MCPServers[index].WorkingDirectory = workspace
	}
	pluginRoots := []string{filepath.Join(workspace, "plugins"), filepath.Join(workspace, ".agents", "plugins")}
	for _, root := range pluginRoots {
		matches, _ := filepath.Glob(filepath.Join(root, "*", ".codex-plugin", "plugin.json"))
		for _, path := range matches {
			if err := loadPlugin(path, workspace, disabled, &catalog); err != nil {
				return catalog, err
			}
		}
	}
	return catalog, nil
}

func loadPlugin(manifestPath, workspace string, disabled map[string]bool, catalog *Catalog) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	var manifest pluginManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Name == "" {
		return nil
	}
	pluginRoot := filepath.Dir(filepath.Dir(manifestPath))
	enabled := !disabled[manifest.Name]
	plugin := Plugin{
		Name: manifest.Name, Version: manifest.Version, Description: manifest.Description,
		Path: pluginRoot, Enabled: enabled, Installed: true, Source: pluginSource(workspace, pluginRoot),
	}
	if enabled {
		_ = loadSkills(filepath.Join(pluginRoot, "skills"), manifest.Name, catalog)
		if manifest.Skills != "" {
			_ = loadSkills(resolvePluginPath(pluginRoot, manifest.Skills), manifest.Name, catalog)
		}
		mcpPath := filepath.Join(pluginRoot, ".mcp.json")
		mcpStart := len(catalog.MCPServers)
		if len(manifest.MCPServers) > 0 {
			if err := loadManifestMCP(pluginRoot, manifest.MCPServers, catalog); err != nil {
				return err
			}
		} else if err := loadMCPFile(mcpPath, catalog); err != nil {
			return err
		}
		for index := mcpStart; index < len(catalog.MCPServers); index++ {
			catalog.MCPServers[index].WorkingDirectory = pluginRoot
		}
		plugin.Hooks = loadHooks(pluginRoot, manifest.Hooks)
		plugin.Apps = loadApps(pluginRoot, manifest.Apps)
		plugin.Commands = loadCommands(pluginRoot, manifest.Commands)
		plugin.Agents = loadAgents(pluginRoot, manifest.Agents)
	}
	catalog.Plugins = append(catalog.Plugins, plugin)
	return nil
}

func pluginSource(workspace, pluginRoot string) string {
	installed := filepath.Join(workspace, ".agents", "plugins")
	rel, err := filepath.Rel(installed, pluginRoot)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "marketplace"
	}
	return "local"
}

func resolvePluginPath(pluginRoot, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "./") {
		return filepath.Join(pluginRoot, filepath.Clean(value))
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(pluginRoot, value)
}

func loadManifestMCP(pluginRoot string, raw json.RawMessage, catalog *Catalog) error {
	var path string
	if json.Unmarshal(raw, &path) == nil && path != "" {
		return loadMCPFile(resolvePluginPath(pluginRoot, path), catalog)
	}
	return loadMCPBytes(raw, filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), catalog)
}

type extensionSettings struct {
	DisabledPlugins []string `json:"disabled_plugins"`
}

func settingsPath(workspace string) string {
	return filepath.Join(workspace, ".codex", "extensions.json")
}

func loadDisabledPlugins(workspace string) map[string]bool {
	data, err := os.ReadFile(settingsPath(workspace))
	if err != nil {
		return map[string]bool{}
	}
	var settings extensionSettings
	_ = json.Unmarshal(data, &settings)
	disabled := map[string]bool{}
	for _, name := range settings.DisabledPlugins {
		disabled[name] = true
	}
	return disabled
}

func SetPluginEnabled(workspace, name string, enabled bool) error {
	disabled := loadDisabledPlugins(workspace)
	if enabled {
		delete(disabled, name)
	} else {
		disabled[name] = true
	}
	names := make([]string, 0, len(disabled))
	for item := range disabled {
		names = append(names, item)
	}
	data, err := json.MarshalIndent(extensionSettings{DisabledPlugins: names}, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Join(workspace, ".codex")
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	temporary := filepath.Join(directory, "extensions.json.tmp")
	if err := os.WriteFile(temporary, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, settingsPath(workspace))
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
	return loadMCPBytes(data, path, catalog)
}

func loadMCPBytes(data []byte, path string, catalog *Catalog) error {
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
			Command          string            `json:"command"`
			Args             []string          `json:"args"`
			Env              map[string]string `json:"env"`
			URL              string            `json:"url"`
			Headers          map[string]string `json:"headers"`
			StartupTimeoutMS int               `json:"startup_timeout_ms"`
		}
		if json.Unmarshal(value, &config) != nil || (config.Command == "" && config.URL == "") {
			continue
		}
		timeout := time.Duration(config.StartupTimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		catalog.MCPServers = append(catalog.MCPServers, MCPServer{
			Name: name, Command: config.Command, Args: config.Args, Env: config.Env,
			URL: config.URL, Headers: config.Headers, StartupTimeout: timeout, SourcePath: path,
		})
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

func (catalog Catalog) Hooks() []Hook {
	var hooks []Hook
	for _, plugin := range catalog.Plugins {
		if plugin.Enabled {
			hooks = append(hooks, plugin.Hooks...)
		}
	}
	return hooks
}
