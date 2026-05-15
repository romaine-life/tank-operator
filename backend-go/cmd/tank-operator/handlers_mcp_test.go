package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseMCPServerEntries(t *testing.T) {
	var config map[string]any
	if err := json.Unmarshal([]byte(`{
		"mcpServers": {
			"github": {"type": "http", "url": "http://127.0.0.1:9992/"},
			"localfs": {"command": "localfs-mcp"},
			"broken": "skip me",
			"k8s": {"url": "http://127.0.0.1:9993/"}
		}
	}`), &config); err != nil {
		t.Fatal(err)
	}

	got := parseMCPServerEntries(config, "/workspace/.mcp.json")
	want := []mcpServerEntry{
		{
			Name:      "github",
			Transport: "http",
			Target:    "http://127.0.0.1:9992/",
			Source:    "/workspace/.mcp.json",
			Enabled:   true,
		},
		{
			Name:      "k8s",
			Transport: "unknown",
			Target:    "http://127.0.0.1:9993/",
			Source:    "/workspace/.mcp.json",
			Enabled:   true,
		},
		{
			Name:      "localfs",
			Transport: "stdio",
			Target:    "localfs-mcp",
			Source:    "/workspace/.mcp.json",
			Enabled:   true,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMCPServerEntries() mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseMCPServerEntriesMissingConfig(t *testing.T) {
	got := parseMCPServerEntries(map[string]any{}, "/workspace/.mcp.json")
	if len(got) != 0 {
		t.Fatalf("expected empty entries, got %#v", got)
	}
}
