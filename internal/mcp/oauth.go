package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrOAuthRequired = errors.New("MCP OAuth login required")

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
}

type Login struct {
	ServerName   string
	ServerURL    string
	Workspace    string
	State        string
	Verifier     string
	RedirectURI  string
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
}

func tokenPath(workspace, server string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, server)
	return filepath.Join(workspace, "tmp", "mcp-oauth", safe+".json")
}

func LoadToken(workspace, server string) (Token, bool) {
	data, err := os.ReadFile(tokenPath(workspace, server))
	if err != nil {
		return Token{}, false
	}
	var token Token
	if json.Unmarshal(data, &token) != nil || token.AccessToken == "" {
		return Token{}, false
	}
	return token, true
}

func SaveToken(workspace, server string, token Token) error {
	path := tokenPath(workspace, server)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (token Token) expired() bool {
	return !token.ExpiresAt.IsZero() && time.Now().After(token.ExpiresAt.Add(-30*time.Second))
}

func (token Token) AuthorizationHeader() string {
	kind := token.TokenType
	if kind == "" {
		kind = "Bearer"
	}
	return kind + " " + token.AccessToken
}

func RefreshToken(ctx context.Context, workspace, server string, httpClient *http.Client) (Token, error) {
	token, ok := LoadToken(workspace, server)
	if !ok {
		return Token{}, ErrOAuthRequired
	}
	if !token.expired() {
		return token, nil
	}
	if token.RefreshToken == "" || token.TokenURL == "" {
		return Token{}, ErrOAuthRequired
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token.RefreshToken},
		"client_id":     {token.ClientID},
	}
	if token.ClientSecret != "" {
		form.Set("client_secret", token.ClientSecret)
	}
	refreshed, err := exchangeToken(ctx, httpClient, token.TokenURL, form)
	if err != nil {
		return Token{}, err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = token.RefreshToken
	}
	refreshed.TokenURL = token.TokenURL
	refreshed.ClientID = token.ClientID
	refreshed.ClientSecret = token.ClientSecret
	if err := SaveToken(workspace, server, refreshed); err != nil {
		return Token{}, err
	}
	return refreshed, nil
}

func BeginLogin(ctx context.Context, httpClient *http.Client, serverName, serverURL, workspace, redirectURI string) (Login, error) {
	metadata, err := discoverAuthServer(ctx, httpClient, serverURL)
	if err != nil {
		return Login{}, err
	}
	clientID, clientSecret, err := registerClient(ctx, httpClient, metadata.RegistrationEndpoint, redirectURI)
	if err != nil {
		return Login{}, err
	}
	verifier, challenge, err := pkce()
	if err != nil {
		return Login{}, err
	}
	state, err := randomString(16)
	if err != nil {
		return Login{}, err
	}
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {serverURL},
	}
	if len(metadata.ScopesSupported) > 0 {
		query.Set("scope", strings.Join(metadata.ScopesSupported, " "))
	}
	authorize, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return Login{}, err
	}
	authorize.RawQuery = query.Encode()
	return Login{
		ServerName: serverName, ServerURL: serverURL, Workspace: workspace,
		State: state, Verifier: verifier, RedirectURI: redirectURI,
		AuthorizeURL: authorize.String(), TokenURL: metadata.TokenEndpoint,
		ClientID: clientID, ClientSecret: clientSecret,
	}, nil
}

func (login Login) Complete(ctx context.Context, httpClient *http.Client, code string) (Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {login.RedirectURI},
		"client_id":     {login.ClientID},
		"code_verifier": {login.Verifier},
		"resource":      {login.ServerURL},
	}
	if login.ClientSecret != "" {
		form.Set("client_secret", login.ClientSecret)
	}
	token, err := exchangeToken(ctx, httpClient, login.TokenURL, form)
	if err != nil {
		return Token{}, err
	}
	token.TokenURL = login.TokenURL
	token.ClientID = login.ClientID
	token.ClientSecret = login.ClientSecret
	if err := SaveToken(login.Workspace, login.ServerName, token); err != nil {
		return Token{}, err
	}
	return token, nil
}

type authServerMetadata struct {
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

func discoverAuthServer(ctx context.Context, httpClient *http.Client, serverURL string) (authServerMetadata, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return authServerMetadata{}, err
	}
	resourceURL := parsed.Scheme + "://" + parsed.Host + "/.well-known/oauth-protected-resource"
	var resource struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	_ = getJSON(ctx, httpClient, resourceURL, &resource)
	issuer := parsed.Scheme + "://" + parsed.Host
	if len(resource.AuthorizationServers) > 0 {
		issuer = strings.TrimRight(resource.AuthorizationServers[0], "/")
	}
	var metadata authServerMetadata
	for _, candidate := range []string{
		issuer + "/.well-known/oauth-authorization-server",
		issuer + "/.well-known/openid-configuration",
	} {
		if err := getJSON(ctx, httpClient, candidate, &metadata); err == nil && metadata.AuthorizationEndpoint != "" && metadata.TokenEndpoint != "" {
			return metadata, nil
		}
	}
	return authServerMetadata{}, fmt.Errorf("MCP OAuth metadata not found for %s", serverURL)
}

func registerClient(ctx context.Context, httpClient *http.Client, endpoint, redirectURI string) (string, string, error) {
	if endpoint == "" {
		return "lob-codex", "", nil
	}
	payload, _ := json.Marshal(map[string]any{
		"client_name":                "LOB Codex",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "lob-codex", "", nil
	}
	var registered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if json.Unmarshal(body, &registered) != nil || registered.ClientID == "" {
		return "lob-codex", "", nil
	}
	return registered.ClientID, registered.ClientSecret, nil
}

func exchangeToken(ctx context.Context, httpClient *http.Client, tokenURL string, form url.Values) (Token, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := httpClient.Do(request)
	if err != nil {
		return Token{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Token{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Token{}, fmt.Errorf("OAuth token exchange %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Token{}, err
	}
	if raw.AccessToken == "" {
		return Token{}, errors.New("OAuth token response missing access_token")
	}
	token := Token{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, TokenType: raw.TokenType}
	if raw.ExpiresIn > 0 {
		token.ExpiresAt = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	}
	return token, nil
}

func getJSON(ctx context.Context, httpClient *http.Client, rawURL string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s", rawURL, response.Status)
	}
	return json.Unmarshal(body, target)
}

func pkce() (verifier, challenge string, err error) {
	verifier, err = randomString(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomString(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func oauthWWWAuthenticate(header string) bool {
	return strings.Contains(strings.ToLower(header), "bearer")
}
