package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Marketplace struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"display_name,omitempty"`
	Path        string             `json:"path"`
	Plugins     []MarketplaceEntry `json:"plugins"`
}

type MarketplaceEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Category    string `json:"category,omitempty"`
	Installed   bool   `json:"installed"`
	Marketplace string `json:"marketplace,omitempty"`
}

func MarketplaceRoots(workspace string) []string {
	roots := []string{filepath.Join(workspace, ".agents", "plugins", "marketplace.json")}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".agents", "plugins", "marketplace.json"))
	}
	return roots
}

func LoadMarketplaces(workspace string) ([]Marketplace, error) {
	catalog, err := Load(workspace)
	if err != nil {
		return nil, err
	}
	installed := map[string]bool{}
	for _, plugin := range catalog.Plugins {
		installed[plugin.Name] = true
	}
	var marketplaces []Marketplace
	for _, path := range MarketplaceRoots(workspace) {
		marketplace, err := loadMarketplaceFile(path, installed)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		marketplaces = append(marketplaces, marketplace)
	}
	return marketplaces, nil
}

func loadMarketplaceFile(path string, installed map[string]bool) (Marketplace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Marketplace{}, err
	}
	var raw struct {
		Name      string `json:"name"`
		Interface struct {
			DisplayName string `json:"displayName"`
		} `json:"interface"`
		Plugins []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
			Source   struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Marketplace{}, fmt.Errorf("decode marketplace %s: %w", path, err)
	}
	root := filepath.Dir(path)
	marketplace := Marketplace{Name: raw.Name, DisplayName: raw.Interface.DisplayName, Path: path}
	if marketplace.Name == "" {
		marketplace.Name = filepath.Base(root)
	}
	for _, entry := range raw.Plugins {
		pluginPath := resolvePluginPath(root, entry.Source.Path)
		if pluginPath == "" {
			pluginPath = filepath.Join(root, "plugins", entry.Name)
		}
		marketplace.Plugins = append(marketplace.Plugins, MarketplaceEntry{
			Name: entry.Name, Path: pluginPath, Category: entry.Category,
			Installed: installed[entry.Name], Marketplace: marketplace.Name,
		})
	}
	return marketplace, nil
}

func InstallPlugin(workspace, sourcePath string) (Plugin, error) {
	sourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		return Plugin{}, err
	}
	manifest := filepath.Join(sourcePath, ".codex-plugin", "plugin.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		return Plugin{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	var parsed struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &parsed) != nil || parsed.Name == "" {
		return Plugin{}, errors.New("plugin.json name is required")
	}
	destination := filepath.Join(workspace, ".agents", "plugins", parsed.Name)
	if _, err := os.Stat(destination); err == nil {
		return Plugin{}, fmt.Errorf("plugin %s is already installed", parsed.Name)
	}
	if err := copyTree(sourcePath, destination); err != nil {
		_ = os.RemoveAll(destination)
		return Plugin{}, err
	}
	if err := SetPluginEnabled(workspace, parsed.Name, true); err != nil {
		return Plugin{}, err
	}
	catalog, err := Load(workspace)
	if err != nil {
		return Plugin{}, err
	}
	for _, plugin := range catalog.Plugins {
		if plugin.Name == parsed.Name {
			return plugin, nil
		}
	}
	return Plugin{Name: parsed.Name, Path: destination, Enabled: true, Installed: true, Source: "marketplace"}, nil
}

func UninstallPlugin(workspace, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("plugin name is required")
	}
	catalog, err := Load(workspace)
	if err != nil {
		return err
	}
	var target *Plugin
	for index := range catalog.Plugins {
		if catalog.Plugins[index].Name == name {
			target = &catalog.Plugins[index]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("unknown plugin %s", name)
	}
	installedRoot := filepath.Join(workspace, ".agents", "plugins")
	rel, err := filepath.Rel(installedRoot, target.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("plugin %s was not installed from marketplace and cannot be uninstalled", name)
	}
	if err := os.RemoveAll(target.Path); err != nil {
		return err
	}
	return SetPluginEnabled(workspace, name, true)
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
