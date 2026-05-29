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
	// can distinguish an agent-authored handoff from a human-typed turn.
	// Avatar identity still has to come from a durable assigned avatar id,
	// not a client-side hash. Self-handoff (origin == target) and absent
	// origin both leave the field off, mirroring how a human-typed browser
	// turn looks today.
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

func TestUserSubmissionEventMapsStampsAuthorKind(t *testing.T) {
	// A turn submitted by a non-interactive principal (an auth.romaine.life
	// bot token) carries author_kind=system on both boundary events so the
	// frontend attributes the user bubble to the session's system identity
	// instead of the human owner's Gravatar. Human-typed turns leave the
	// field absent, which is indistinguishable from today's behavior.
	tests := []struct {
		name     string
		kind     string
		expected string
	}{
		{name: "bot authored", kind: string(AuthorKindSystem), expected: string(AuthorKindSystem)},
		{name: "human authored", kind: "", expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, events, err := UserSubmissionEventMaps(UserSubmissionArgs{
				SessionID:   "63",
				Email:       "human@example.com",
				ClientNonce: "nonce-1",
				Text:        "hello",
				Runtime:     "claude",
				AuthorKind:  tt.kind,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 {
				t.Fatalf("want 2 events, got %d", len(events))
			}
			for _, event := range events {
				got, _ := event["author_kind"].(string)
				if got != tt.expected {
					t.Fatalf("event %q author_kind = %q, want %q", event["type"], got, tt.expected)
				}
				// validateEventMap must accept the stamped user_message.created.
				if event["type"] == string(EventUserMessageCreated) {
					if err := ValidateEventMap(event); err != nil {
						t.Fatalf("validate user_message.created: %v", err)
					}
				}
			}
		})
	}
}

func TestValidateUserMessageRejectsUnknownAuthorKind(t *testing.T) {
	// Backend is the sole producer of these events and only ever stamps the
	// known value; an unknown author_kind signals a producer regression and
	// must be rejected loudly rather than silently rendered.
	_, events, err := UserSubmissionEventMaps(UserSubmissionArgs{
		SessionID:   "63",
		Email:       "human@example.com",
		ClientNonce: "nonce-1",
		Text:        "hello",
		Runtime:     "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	var userMessage map[string]any
	for _, event := range events {
		if event["type"] == string(EventUserMessageCreated) {
			userMessage = event
			break
		}
	}
	if userMessage == nil {
		t.Fatal("user_message.created event missing")
	}
	userMessage["author_kind"] = "intern"
	if err := ValidateEventMap(userMessage); err == nil {
		t.Fatal("expected validation to reject unknown author_kind, got nil")
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

func TestValidateEventMapAcceptsSessionStatus(t *testing.T) {
	event := map[string]any{
		"event_id":    "session:63:status:ready",
		"order_key":   "1768179848000-00000001-session:63:status:ready",
		"session_id":  "63",
		"timeline_id": "session:63:status:ready",
		"actor":       "system",
		"source":      "tank",
		"type":        "session.status",
		"created_at":  "2026-05-12T00:00:08.000Z",
		"visibility":  "durable",
		"payload": map[string]any{
			"status": "ready",
			"text":   "Session is ready.",
		},
	}
	if err := ValidateEventMap(event); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEventMapAcceptsSessionStatusFailedExtension(t *testing.T) {
	event := map[string]any{
		"event_id":    "session:63:provider:codex:status",
		"order_key":   "1768179848000-00000003-session:63:provider:codex:status",
		"session_id":  "63",
		"timeline_id": "session:63:provider:codex:status",
		"actor":       "system",
		"source":      "tank",
		"type":        "session.status",
		"created_at":  "2026-05-24T18:48:30.000Z",
		"visibility":  "durable",
		"payload": map[string]any{
			"status":          "failed",
			"text":            "Codex sign-in expired. Re-authenticate to continue.",
			"failure_scope":   "provider",
			"failure_subject": "codex",
			"failure_reason":  "refresh_token_reused",
			"action": map[string]any{
				"label": "Re-sign-in to Codex",
				"href":  "/api/auth/codex/login",
			},
		},
	}
	if err := ValidateEventMap(event); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEventMapRejectsContentFreeSessionStatusFailed(t *testing.T) {
	// failure_scope is set so the extension shape kicks in, but no
	// subject is provided. A future writer could not produce a
	// content-free banner: the contract forces (subject + (reason OR
	// action)) once any extension field is set. This pins the
	// "designed and visible" line from docs/quality-timeframes.md.
	tests := []struct {
		name  string
		edit  func(payload map[string]any)
		error string
	}{
		{
			name: "missing failure_subject",
			edit: func(p map[string]any) {
				p["failure_scope"] = "provider"
			},
			error: "failure_subject is required",
		},
		{
			name: "invalid failure_scope",
			edit: func(p map[string]any) {
				p["failure_scope"] = "everything"
				p["failure_subject"] = "codex"
				p["failure_reason"] = "broken"
			},
			error: "failure_scope must be provider, session, or pod",
		},
		{
			name: "no reason and no action",
			edit: func(p map[string]any) {
				p["failure_scope"] = "provider"
				p["failure_subject"] = "codex"
			},
			error: "failure_reason or payload.action is required",
		},
		{
			name: "action missing href",
			edit: func(p map[string]any) {
				p["failure_scope"] = "provider"
				p["failure_subject"] = "codex"
				p["action"] = map[string]any{"label": "Re-sign-in"}
			},
			error: "action.label and payload.action.href are required",
		},
	}
	base := map[string]any{
		"event_id":    "session:63:provider:codex:status",
		"order_key":   "k",
		"session_id":  "63",
		"timeline_id": "session:63:provider:codex:status",
		"actor":       "system",
		"source":      "tank",
		"type":        "session.status",
		"created_at":  "2026-05-24T18:48:30.000Z",
		"visibility":  "durable",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{"status": "failed", "text": "broken"}
			tt.edit(payload)
			event := cloneMap(base)
			event["payload"] = payload
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

func TestValidateEventMapAcceptsBareSessionStatusFailed(t *testing.T) {
	// Legacy bare failed (no extension fields). Pod-lifecycle writers
	// may emit this shape; only when extension fields are present does
	// the stricter contract kick in.
	event := map[string]any{
		"event_id":    "session:63:status:failed",
		"order_key":   "k",
		"session_id":  "63",
		"timeline_id": "session:63:status:failed",
		"actor":       "system",
		"source":      "tank",
		"type":        "session.status",
		"created_at":  "2026-05-24T18:48:30.000Z",
		"visibility":  "durable",
		"payload": map[string]any{
			"status": "failed",
			"text":   "Session failed.",
		},
	}
	if err := ValidateEventMap(event); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEventMapRejectsUnknownSessionStatus(t *testing.T) {
	event := map[string]any{
		"event_id":    "session:63:status:booting",
		"order_key":   "1768179848000-00000099-session:63:status:booting",
		"session_id":  "63",
		"timeline_id": "session:63:status:booting",
		"actor":       "system",
		"source":      "tank",
		"type":        "session.status",
		"created_at":  "2026-05-12T00:00:08.000Z",
		"visibility":  "durable",
		"payload": map[string]any{
			"status": "booting",
			"text":   "Session is booting.",
		},
	}
	err := ValidateEventMap(event)
	if err == nil {
		t.Fatal("ValidateEventMap succeeded, want unknown session.status rejection")
	}
	if !strings.Contains(err.Error(), "loading, ready, or failed") {
		t.Fatalf("error = %q, want session.status enum rejection", err.Error())
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
