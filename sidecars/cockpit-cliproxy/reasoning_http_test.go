package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestHTTPResponsesPreservesReasoningEffortThroughCodexRuntime(t *testing.T) {
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_tmp","object":"response","created_at":1,"status":"completed","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		AuthDir: t.TempDir(),
		CodexKey: []config.CodexKey{
			{
				APIKey:  "mock-upstream-key",
				BaseURL: upstream.URL,
				Models:  []internalconfig.CodexModel{{Name: "gpt-5.2", Alias: "gpt-5.2-codex"}},
			},
		},
	}
	configPath := t.TempDir() + "/config.json"
	m := &manifest{
		APIKeys: []apiKeySpec{
			{ID: "client", Label: "Client", Key: "client-key", Enabled: true},
		},
		ModelIDs: []string{"gpt-5.2"},
		ModelAliases: []modelAliasSpec{
			{SourceModel: "gpt-5.2", Alias: "gpt-5.2-codex", Fork: true},
		},
		apiKeyByValue:   map[string]*apiKeySpec{},
		accountByID:     map[string]*accountSpec{},
		accountByAuthID: map[string]*accountSpec{},
		accountByAPIKey: map[string]*accountSpec{},
		aliasToSource:   map[string]string{"gpt-5.2-codex": "gpt-5.2"},
	}
	m.apiKeyByValue["client-key"] = &m.APIKeys[0]

	manager := buildCoreAuthManager(cfg, &cockpitSelector{manifest: m}, &authHook{manifest: m})
	runtime, err := newSidecarRuntime(context.Background(), configPath, cfg, m, manager)
	if err != nil {
		t.Fatalf("start sidecar runtime: %v", err)
	}
	defer runtime.Stop()
	codexAuthRegistered := false
	for _, auth := range manager.List() {
		if auth != nil && auth.Provider == "codex" {
			codexAuthRegistered = true
			break
		}
	}
	if !codexAuthRegistered {
		t.Fatal("codex API key auth was not registered")
	}

	server := &relayServer{
		runtime:  runtime,
		cfg:      cfg,
		manifest: m,
		policy:   &requestPolicy{manifest: m},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.2-codex","input":"hello","stream":false,"reasoning":{"effort":"high"}}`))
	req.Header.Set("Authorization", "Bearer client-key")
	req.Header.Set("Content-Type", "application/json")
	server.router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(upstreamBody) == 0 {
		t.Fatal("upstream did not receive request body")
	}
	var got map[string]any
	if err := json.Unmarshal(upstreamBody, &got); err != nil {
		t.Fatalf("decode upstream body: %v; body=%s", err, string(upstreamBody))
	}
	reasoning, _ := got["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("upstream reasoning.effort = %#v; body=%s", reasoning["effort"], string(upstreamBody))
	}
}
