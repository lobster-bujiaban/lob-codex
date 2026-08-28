package appserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/extensions"
	"github.com/lobster-bujiaban/lob-codex/internal/mcp"
	"github.com/lobster-bujiaban/lob-codex/internal/session"
)

func (h *Handler) listMarketplace(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	marketplaces, err := extensions.LoadMarketplaces(runtime.metadata.WorkspaceRoot)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"marketplaces": marketplaces})
}

func (h *Handler) installPlugin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Path            string `json:"path"`
		MarketplacePath string `json:"marketplace_path"`
		PluginName      string `json:"plugin_name"`
	}
	if json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10)).Decode(&input) != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	source := strings.TrimSpace(input.Path)
	if source == "" && input.MarketplacePath != "" && input.PluginName != "" {
		marketplaces, err := extensions.LoadMarketplaces(runtime.metadata.WorkspaceRoot)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, marketplace := range marketplaces {
			if marketplace.Path != input.MarketplacePath && marketplace.Name != input.MarketplacePath {
				continue
			}
			for _, entry := range marketplace.Plugins {
				if entry.Name == input.PluginName {
					source = entry.Path
				}
			}
		}
	}
	if source == "" {
		http.Error(writer, "path or marketplace_path+plugin_name is required", http.StatusBadRequest)
		return
	}
	plugin, err := extensions.InstallPlugin(runtime.metadata.WorkspaceRoot, source)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.refreshThreadExtensions(request, runtime); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(plugin)
}

func (h *Handler) uninstallPlugin(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	if err := extensions.UninstallPlugin(runtime.metadata.WorkspaceRoot, request.PathValue("pluginName")); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.refreshThreadExtensions(request, runtime); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) startMCPOAuth(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	serverName := request.PathValue("serverName")
	if _, err := h.sessionIO(runtime); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	runtime.mu.Lock()
	sess := runtime.session
	runtime.mu.Unlock()
	if sess == nil {
		http.Error(writer, "session unavailable", http.StatusConflict)
		return
	}
	var serverURL string
	for _, status := range sess.ExtensionInventory().MCPServers {
		if status.Name == serverName {
			serverURL = strings.TrimSpace(status.SourcePath)
		}
	}
	catalog, err := extensions.Load(runtime.metadata.WorkspaceRoot)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, server := range catalog.MCPServers {
		if server.Name == serverName {
			serverURL = server.URL
			break
		}
	}
	if serverURL == "" {
		http.Error(writer, "MCP server URL is required for OAuth", http.StatusBadRequest)
		return
	}
	host := request.Host
	if host == "" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	redirect := fmt.Sprintf("%s://%s/oauth/mcp/callback", scheme, host)
	login, err := mcp.BeginLogin(request.Context(), http.DefaultClient, serverName, serverURL, runtime.metadata.WorkspaceRoot, redirect)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	h.oauthMu.Lock()
	h.oauthLogins[login.State] = pendingOAuth{login: login, threadID: runtime.metadata.ID}
	h.oauthMu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]string{"authorization_url": login.AuthorizeURL, "state": login.State})
}

func (h *Handler) completeMCPOAuth(writer http.ResponseWriter, request *http.Request) {
	state := request.URL.Query().Get("state")
	code := request.URL.Query().Get("code")
	h.oauthMu.Lock()
	pending, ok := h.oauthLogins[state]
	if ok {
		delete(h.oauthLogins, state)
	}
	h.oauthMu.Unlock()
	if !ok || code == "" {
		http.Error(writer, "unknown OAuth login", http.StatusBadRequest)
		return
	}
	if _, err := pending.login.Complete(request.Context(), http.DefaultClient, code); err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	runtime, err := h.thread(pending.threadID)
	if err == nil {
		_ = h.refreshThreadExtensions(request, runtime)
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte("<!doctype html><title>MCP 登录完成</title><p>MCP OAuth 已完成，可以关闭此窗口。</p>"))
}

func (h *Handler) respondElicitation(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		ThreadID string         `json:"thread_id"`
		Action   string         `json:"action"`
		Content  map[string]any `json:"content"`
	}
	if json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&input) != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	runtime, err := h.thread(input.ThreadID)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	action := input.Action
	if action == "" {
		action = "accept"
	}
	if err := sessionIO.RespondElicitation(request.Context(), session.ElicitationResponse{
		ElicitationID: request.PathValue("elicitationID"), Action: action, Content: input.Content,
	}); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) refreshThreadExtensions(request *http.Request, runtime *threadRuntime) error {
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		return err
	}
	return sessionIO.RefreshExtensions(request.Context())
}
