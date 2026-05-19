package conversation

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type conversationSchema struct {
	Properties map[string]struct {
		Enum []string `json:"enum"`
	} `json:"properties"`
}

type conversationFixtures struct {
	Events []struct {
		Name  string         `json:"name"`
		Event map[string]any `json:"event"`
	} `json:"events"`
}

func TestContractEnumsMatchSchema(t *testing.T) {
	schemaBytes, err := os.ReadFile("../../../schemas/tank-conversation-event.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema conversationSchema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		schemaProperty string
		goType         string
	}{
		{name: "actor", schemaProperty: "actor", goType: "Actor"},
		{name: "source", schemaProperty: "source", goType: "Source"},
		{name: "visibility", schemaProperty: "visibility", goType: "Visibility"},
		{name: "event type", schemaProperty: "type", goType: "EventType"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := schema.Properties[tt.schemaProperty].Enum
			if len(expected) == 0 {
				t.Fatalf("schema property %q has no enum", tt.schemaProperty)
			}
			actual := goStringConstants(t, tt.goType)
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("%s enum drift:\nGo:     %#v\nSchema: %#v", tt.name, actual, expected)
			}
		})
	}
}

func TestFixtureEventsValidate(t *testing.T) {
	fixtureBytes, err := os.ReadFile("../../../schemas/tank-conversation-event.fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures conversationFixtures
	if err := json.Unmarshal(fixtureBytes, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Events) == 0 {
		t.Fatal("expected fixtures")
	}
	for _, fixture := range fixtures.Events {
		t.Run(fixture.Name, func(t *testing.T) {
			if err := ValidateEventMap(fixture.Event); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserSubmissionEventMapsStampsOriginSessionID(t *testing.T) {
	// Origin-stamping case: a sibling tank-operator session posted this
	// turn via the mcp-tank-operator handoff path. The orchestrator
	// stamps `origin_session_id` on both emitted events so the frontend
	// can pick the parent session's deterministic avatar for the user
	// bubble. Self-handoff (origin == target) and absent origin both
	// leave the field off, mirroring how a human-typed browser turn
	// looks today.
	target := "63"
	tests := []struct {
		name     string
		origin   string
		expected string
	}{
		{name: "sibling handoff", origin: "42", expected: "42"},
		{name: "absent origin", origin: "", expected: ""},
		{name: "self handoff", origin: target, expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, events, err := UserSubmissionEventMaps(UserSubmissionArgs{
				SessionID:       target,
				Email:           "human@example.com",
				ClientNonce:     "nonce-1",
				Text:            "hello",
				Runtime:         "claude",
				OriginSessionID: tt.origin,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 {
				t.Fatalf("want 2 events, got %d", len(events))
			}
			for _, event := range events {
				got, _ := event["origin_session_id"].(string)
				if got != tt.expected {
					t.Fatalf("event %q origin_session_id = %q, want %q", event["type"], got, tt.expected)
				}
			}
		})
	}
}

func TestValidateEventMapRejectsMalformedPerTypeEvents(t *testing.T) {
	valid := map[string]any{
		"event_id":     "evt-1",
		"order_key":    "order-1",
		"session_id":   "63",
		"turn_id":      "turn-1",
		"timeline_id":  "turn-1:user",
		"client_nonce": "client-1",
		"actor":        "user",
		"source":       "tank",
		"type":         "user_message.created",
		"created_at":   "2026-05-12T00:00:00.000Z",
		"visibility":   "durable",
		"payload": map[string]any{
			"text": "hello",
			"display": map[string]any{
				"kind": "plain",
			},
		},
	}

	tests := []struct {
		name  string
		edit  func(map[string]any)
		error string
	}{
		{
			name:  "missing client nonce",
			edit:  func(event map[string]any) { delete(event, "client_nonce") },
			error: "client_nonce",
		},
		{
			name:  "missing timeline id",
			edit:  func(event map[string]any) { delete(event, "timeline_id") },
			error: "timeline_id",
		},
		{
			name: "missing text",
			edit: func(event map[string]any) {
				event["client_nonce"] = "client-1"
				event["payload"] = map[string]any{}
			},
			error: "payload.text",
		},
		{
			name: "bad skill display",
			edit: func(event map[string]any) {
				event["client_nonce"] = "client-1"
				event["payload"] = map[string]any{
					"text": "hello",
					"display": map[string]any{
						"kind":       "skill_invocation",
						"skill_name": "",
					},
				}
			},
			error: "skill_name",
		},
		{
			name: "bad item outcome",
			edit: func(event map[string]any) {
				event["actor"] = "tool"
				event["source"] = "codex"
				event["type"] = "item.completed"
				delete(event, "client_nonce")
				event["payload"] = map[string]any{
					"kind": "command_execution",
					"outcome": map[string]any{
						"kind": "result_failed",
					},
				}
			},
			error: "payload.outcome.reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := cloneMap(valid)
			tt.edit(event)
			err := ValidateEventMap(event)
			if err == nil {
				t.Fatalf("ValidateEventMap succeeded, want error containing %q", tt.error)
			}
			if !strings.Contains(err.Error(), tt.error) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.error)
			}
		})
	}
}

func goStringConstants(t *testing.T, typeName string) []string {
	t.Helper()

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "types.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	var values []string
	for _, decl := range file.Decls {
		genericDecl, ok := decl.(*ast.GenDecl)
		if !ok || genericDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genericDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || !isIdentNamed(valueSpec.Type, typeName) {
				continue
			}
			for _, value := range valueSpec.Values {
				literal, ok := value.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s constant has non-string value: %#v", typeName, value)
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				values = append(values, unquoted)
			}
		}
	}

	if len(values) == 0 {
		t.Fatalf("found no string constants for %s", typeName)
	}
	return values
}

func isIdentNamed(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		if nested, ok := value.(map[string]any); ok {
			output[key] = cloneMap(nested)
			continue
		}
		output[key] = value
	}
	return output
}
