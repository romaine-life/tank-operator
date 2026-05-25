package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTankStaticFilePrefersOverride(t *testing.T) {
	base := t.TempDir()
	override := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "index.html"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, "index.html"), []byte("override"), 0644); err != nil {
		t.Fatal(err)
	}
	found, ok := tankStaticFile(tankStaticRootSet{override: override, base: base}, "index.html")
	if !ok {
		t.Fatal("expected static file")
	}
	body, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "override" {
		t.Fatalf("body=%q", string(body))
	}
}

func TestTankStaticFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := tankStaticFile(tankStaticRootSet{base: root}, "..", filepath.Base(outside)); ok {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestTankStaticIndexInjectsMessageLinkContract(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html><head></head><body><div id=\"root\"></div></body></html>"), 0644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/?session=93&message=turn_1%3Aitem%3Amsg_1", nil)
	req.Host = "tank.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	res := httptest.NewRecorder()

	serveTankStaticFile(res, req, tankStaticRootSet{base: root}, "index.html")

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`id="tank-message-link"`,
		`rel="alternate"`,
		`tank.message_link`,
		`https://tank.example.test/api/sessions/93/timeline`,
		`turn_1:item:msg_1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("injected HTML missing %q: %s", want, body)
		}
	}
	if got := res.Header().Values("Link"); len(got) != 2 {
		t.Fatalf("Link headers = %#v, want alternate + related", got)
	}
}

func TestTankMessageLinkJSONWithoutAuthReturnsContract(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?session=93&message=turn_1%3Aitem%3Amsg_1&format=json", nil)
	req.Host = "tank.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	res := httptest.NewRecorder()

	(&appServer{}).handleTankMessageLink(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["kind"] != "tank.message_link" || body["session_id"] != "93" || body["timeline_id"] != "turn_1:item:msg_1" {
		t.Fatalf("unexpected contract: %#v", body)
	}
	if body["auth_required"] != true || body["resolved"] != false {
		t.Fatalf("auth flags = %#v", body)
	}
	api, ok := body["api"].(map[string]any)
	if !ok {
		t.Fatalf("api missing: %#v", body["api"])
	}
	timelineURL, _ := api["timeline_url"].(string)
	if !strings.Contains(timelineURL, "/api/sessions/93/timeline") || !strings.Contains(timelineURL, "message=turn_1%3Aitem%3Amsg_1") {
		t.Fatalf("timeline_url = %q", timelineURL)
	}
	if got, _ := api["page_before_url"].(string); !strings.Contains(got, "before_cursor=%3Cprev_cursor%3E") {
		t.Fatalf("page_before_url = %q", got)
	}
	recipe, ok := body["agent_recipe"].([]any)
	if !ok || len(recipe) == 0 {
		t.Fatalf("agent_recipe missing: %#v", body["agent_recipe"])
	}
	recipeJSON, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Authorization: Bearer $(cat /run/secrets/auth.romaine.life/token)",
		"before_cursor",
		"do not send it as JSON",
	} {
		if !strings.Contains(string(recipeJSON), want) {
			t.Fatalf("agent_recipe missing %q: %s", want, recipeJSON)
		}
	}
}

func TestTankMessageLinkContentNegotiationDefaultsNonBrowserToJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?session=93&message=turn_1%3Aitem%3Amsg_1", nil)
	if !wantsTankMessageLinkJSON(req) {
		t.Fatal("missing Accept should default message links to JSON")
	}
	req.Header.Set("Accept", "*/*")
	if !wantsTankMessageLinkJSON(req) {
		t.Fatal("generic Accept */* should default message links to JSON")
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if wantsTankMessageLinkJSON(req) {
		t.Fatal("browser navigation Accept should keep serving HTML")
	}
}

func TestRetiredAgentWSRouteIsNotRegistered(t *testing.T) {
	body, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "agent-ws") {
		t.Fatal("server.go must not register the retired agent-ws chat route")
	}
}
