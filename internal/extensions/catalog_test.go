package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMarketplaceInstallUninstallAndComponents(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "demo-plugin")
	writePlugin(t, source, `{
  "name": "demo-plugin",
  "version": "1.0.0",
  "description": "demo",
  "hooks": "./hooks.json",
  "apps": "./.app.json",
  "commands": "./commands",
  "agents": "./agents"
}`)
	writeFile(t, filepath.Join(source, "hooks.json"), `{"sessionStart":[{"type":"command","command":"echo start"}]}`)
	writeFile(t, filepath.Join(source, ".app.json"), `{"name":"demo-app","description":"app"}`)
	writeFile(t, filepath.Join(source, "commands", "review.md"), "---\nname: review\ndescription: review code\n---\nReview the diff.")
	writeFile(t, filepath.Join(source, "agents", "researcher.toml"), "name = \"researcher\"\ndescription = \"read only\"\n")
	writeFile(t, filepath.Join(source, "skills", "demo", "SKILL.md"), "---\nname: demo\ndescription: demo skill\n---\nDo the demo.")
	writeFile(t, filepath.Join(source, ".mcp.json"), `{"mcpServers":{"demo":{"command":"echo","args":["mcp"]}}}`)

	marketplaceDir := filepath.Join(workspace, ".agents", "plugins")
	payload, _ := json.Marshal(map[string]any{
		"name":      "local",
		"interface": map[string]any{"displayName": "Local"},
		"plugins": []map[string]any{{
			"name":     "demo-plugin",
			"source":   map[string]string{"source": "local", "path": source},
			"category": "Tools",
		}},
	})
	writeFile(t, filepath.Join(marketplaceDir, "marketplace.json"), string(payload))

	marketplaces, err := LoadMarketplaces(workspace)
	if err != nil {
		t.Fatalf("LoadMarketplaces: %v", err)
	}
	if len(marketplaces) != 1 || len(marketplaces[0].Plugins) != 1 || marketplaces[0].Plugins[0].Installed {
		t.Fatalf("marketplace = %+v", marketplaces)
	}

	plugin, err := InstallPlugin(workspace, source)
	if err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}
	if !plugin.Enabled || plugin.Name != "demo-plugin" || len(plugin.Hooks) == 0 || len(plugin.Commands) == 0 || len(plugin.Agents) == 0 {
		t.Fatalf("installed plugin = %+v", plugin)
	}

	catalog, err := Load(workspace)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	installedRoot := filepath.Join(workspace, ".agents", "plugins", "demo-plugin")
	if len(catalog.Plugins) != 1 || catalog.Skills["demo-plugin:demo"].Name == "" || len(catalog.MCPServers) != 1 || catalog.MCPServers[0].WorkingDirectory != installedRoot {
		t.Fatalf("catalog = %+v skills=%+v mcp=%+v", catalog.Plugins, catalog.Skills, catalog.MCPServers)
	}

	if err := UninstallPlugin(workspace, "demo-plugin"); err != nil {
		t.Fatalf("UninstallPlugin: %v", err)
	}
	catalog, err = Load(workspace)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(catalog.Plugins) != 0 {
		t.Fatalf("plugins after uninstall = %+v", catalog.Plugins)
	}
}

func writePlugin(t *testing.T, root, manifest string) {
	t.Helper()
	writeFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), manifest)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}
