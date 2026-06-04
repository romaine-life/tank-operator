package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

func TestHandleSessionQuestionsProjectsDurableQuestionSets(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"
	app.transcriptRows = &fakeSessionTranscriptRowStore{needsBackfill: false}
	app.turns = fakeSessionTurnStore{byTurnID: map[string]int64{"turn_a": 7, "turn_b": 8}}
	app.sessionEvents = fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {
				Events: []map[string]any{
					questionAwaitingEvent("001", "turn_a", "tl-a", "tool-a", "Choose deployment"),
					questionAnsweredEvent("002", "turn_a", "tl-a", "production"),
					questionAwaitingEvent("003", "turn_b", "tl-b", "tool-b", "Pick region"),
					questionTerminalEvent("004", "turn_a", "turn.completed"),
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/questions", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleSessionQuestions(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var body struct {
		Projection   string `json:"projection"`
		PendingCount int    `json:"pending_count"`
		Sets         []struct {
			ID         string         `json:"id"`
			TurnID     string         `json:"turn_id"`
			TurnNumber int64          `json:"turn_number"`
			Status     string         `json:"status"`
			Answers    map[string]any `json:"answers"`
			Questions  []any          `json:"questions"`
		} `json:"sets"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Projection != "server_question_sets_v1" || body.PendingCount != 1 || len(body.Sets) != 2 {
		t.Fatalf("questions body = %#v", body)
	}
	if body.Sets[0].ID != "tl-b:awaiting_input" || body.Sets[0].Status != "waiting" || body.Sets[0].TurnNumber != 8 {
		t.Fatalf("pending set = %#v", body.Sets[0])
	}
	if body.Sets[1].ID != "tl-a:awaiting_input" || body.Sets[1].Status != "answered" || body.Sets[1].TurnNumber != 7 {
		t.Fatalf("answered set = %#v", body.Sets[1])
	}
	if got := body.Sets[1].Answers["Choose deployment"]; got == nil {
		t.Fatalf("answered set missing durable answers: %#v", body.Sets[1].Answers)
	}
}

func questionAwaitingEvent(orderKey, turnID, timelineID, providerItemID, question string) map[string]any {
	return map[string]any{
		"type":       "turn.awaiting_input",
		"turn_id":    turnID,
		"order_key":  orderKey,
		"created_at": "2026-06-04T00:00:00Z",
		"event_id":   "ev-" + orderKey,
		"payload": map[string]any{
			"timeline_id":      timelineID,
			"provider_item_id": providerItemID,
			"questions": []any{
				map[string]any{
					"question":      question,
					"multiSelect":   false,
					"allowFreeForm": true,
					"options": []any{
						map[string]any{"label": "production"},
						map[string]any{"label": "staging"},
					},
				},
			},
		},
	}
}

func questionAnsweredEvent(orderKey, turnID, timelineID, answer string) map[string]any {
	return map[string]any{
		"type":       "turn.input_answered",
		"turn_id":    turnID,
		"order_key":  orderKey,
		"created_at": "2026-06-04T00:01:00Z",
		"event_id":   "ev-" + orderKey,
		"payload": map[string]any{
			"question_timeline_id": timelineID,
			"answers": map[string]any{
				"Choose deployment": []any{answer},
			},
		},
	}
}

func questionTerminalEvent(orderKey, turnID, typ string) map[string]any {
	return map[string]any{
		"type":       typ,
		"turn_id":    turnID,
		"order_key":  orderKey,
		"created_at": "2026-06-04T00:02:00Z",
		"event_id":   "ev-" + orderKey,
	}
}
