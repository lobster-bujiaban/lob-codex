package appserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lobster-bujiaban/lob-codex/internal/appserver"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
)

func TestChatStreamsModelResponse(t *testing.T) {
	server := httptest.NewServer(appserver.NewHandler(model.NewFakeClient()))
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"prompt":"hello"}`),
	)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got, want := string(body), "Fake model: hello"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
