package main

import (
	"testing"
)

// Events for one AskUserQuestion handoff: the asking turn's derived question
// card, then the question turn's submitted + awaiting_input.
func questionHandoffEvents() []map[string]any {
	awaiting := map[string]any{
		"asking_turn_id":   "turn_ask",
		"question_turn_id": "turn_question-q1",
		"provider_item_id": "item-1",
		"timeline_id":      "turn_question-q1:item-1",
		"questions":        []any{map[string]any{"question": "Proceed?"}},
	}
	return []map[string]any{
		{
			"event_id": "e1", "type": "user_message.created", "actor": "user", "source": "tank",
			"turn_id": "turn_ask", "order_key": "001", "session_id": "63",
			"payload": map[string]any{"text": "do it", "message": map[string]any{"role": "user", "content": "do it"}},
		},
		{
			"event_id": "e2", "type": "turn.submitted", "actor": "user", "source": "tank",
			"turn_id": "turn_ask", "order_key": "002", "session_id": "63",
		},
		{
			"event_id": "e3", "type": "assistant_message.created", "actor": "assistant", "source": "claude",
			"turn_id": "turn_ask", "timeline_id": "turn_ask:qmsg", "order_key": "003", "session_id": "63",
			"payload": map[string]any{
				"text":           "Proceed?",
				"message":        map[string]any{"role": "assistant", "content": "Proceed?"},
				"display":        map[string]any{"kind": "ask_user_question"},
				"awaiting_input": awaiting,
			},
		},
		{
			"event_id": "e4", "type": "turn.submitted", "actor": "runner", "source": "tank",
			"turn_id": "turn_question-q1", "client_nonce": "question-q1", "order_key": "004", "session_id": "63",
		},
		{
			"event_id": "e5", "type": "turn.awaiting_input", "actor": "runner", "source": "claude",
			"turn_id": "turn_question-q1", "client_nonce": "question-q1", "order_key": "005", "session_id": "63",
			"payload": awaiting,
		},
	}
}

func findEntryAwaiting(t *testing.T, entries []map[string]any, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	for _, entry := range entries {
		awaiting, _ := entry["awaitingInput"].(map[string]any)
		if awaiting != nil && predicate(entry) {
			return entry
		}
	}
	t.Fatal("no matching awaiting entry")
	return nil
}

// TestQuestionDismissalFlipsCardsAndAdvancesContentOrderKey pins issue
// #1077 item 4 + the #1078 stop semantics: a non-answer terminal on the
// question turn marks every matching card dismissed and lifts
// contentOrderKey past the terminal, so the materialized rows' SSE cursors
// move and open tabs receive the flip.
func TestQuestionDismissalFlipsCardsAndAdvancesContentOrderKey(t *testing.T) {
	events := append(questionHandoffEvents(), map[string]any{
		"event_id": "e6", "type": "turn.interrupted", "actor": "runner", "source": "claude",
		"turn_id": "turn_question-q1", "client_nonce": "question-q1", "order_key": "006", "session_id": "63",
		"payload": map[string]any{"reason": "question_dismissed_by_stop"},
	})
	projection := projectTranscriptEvents(events)

	// The main transcript filters question-turn rows; the awaiting CARD
	// renders on the question turn's pages. Both surfaces must flip.
	questionTurnEvents := []map[string]any{}
	for _, event := range events {
		if event["turn_id"] == "turn_question-q1" {
			questionTurnEvents = append(questionTurnEvents, event)
		}
	}
	pages := projectTurnPages("turn_question-q1", questionTurnEvents)
	var card map[string]any
	for _, page := range pages.Pages {
		for _, entry := range page.Entries {
			if entry["metaKind"] == "awaiting_input" {
				card = entry
			}
		}
	}
	if card == nil {
		t.Fatalf("no awaiting card on the question turn's pages: %+v", pages.Pages)
	}
	awaiting := card["awaitingInput"].(map[string]any)
	if dismissed, _ := awaiting["dismissed"].(bool); !dismissed {
		t.Fatalf("awaiting card not dismissed: %v", awaiting)
	}
	if answered, _ := awaiting["answered"].(bool); answered {
		t.Fatal("dismissed card must not claim answered")
	}
	if got, _ := card["contentOrderKey"].(string); got != "006" {
		t.Fatalf("card contentOrderKey = %q, want 006 (the dismissing terminal)", got)
	}

	message := findEntryAwaiting(t, projection.Entries, func(entry map[string]any) bool {
		return entry["kind"] == "message"
	})
	messageAwaiting := message["awaitingInput"].(map[string]any)
	if dismissed, _ := messageAwaiting["dismissed"].(bool); !dismissed {
		t.Fatal("derived question message not dismissed")
	}
	if got, _ := message["contentOrderKey"].(string); got != "006" {
		t.Fatalf("message contentOrderKey = %q, want 006", got)
	}
}

// TestQuestionAnswerAdvancesContentOrderKey — the answered flip is the
// audit's literal example of a payload update without a key advance; both
// the derived message card and the awaiting card must move.
func TestQuestionAnswerAdvancesContentOrderKey(t *testing.T) {
	events := append(questionHandoffEvents(), map[string]any{
		"event_id": "e6", "type": "turn.input_answered", "actor": "user", "source": "tank",
		"turn_id": "turn_question-q1", "timeline_id": "turn_question-q1:item-1", "client_nonce": "answer-1",
		"order_key": "006", "session_id": "63",
		"payload": map[string]any{
			"question_timeline_id": "turn_question-q1:item-1",
			"answers":              map[string]any{"Proceed?": []any{"Yes"}},
		},
	})
	projection := projectTranscriptEvents(events)

	questionTurnEvents := []map[string]any{}
	for _, event := range events {
		if event["turn_id"] == "turn_question-q1" {
			questionTurnEvents = append(questionTurnEvents, event)
		}
	}
	pages := projectTurnPages("turn_question-q1", questionTurnEvents)
	var card map[string]any
	for _, page := range pages.Pages {
		for _, entry := range page.Entries {
			if entry["metaKind"] == "awaiting_input" {
				card = entry
			}
		}
	}
	if card == nil {
		t.Fatalf("no awaiting card on the question turn's pages: %+v", pages.Pages)
	}
	awaiting := card["awaitingInput"].(map[string]any)
	if answered, _ := awaiting["answered"].(bool); !answered {
		t.Fatalf("awaiting card not answered: %v", awaiting)
	}
	if dismissed, _ := awaiting["dismissed"].(bool); dismissed {
		t.Fatal("answered card must not be dismissed — the answer wins")
	}
	if got, _ := card["contentOrderKey"].(string); got != "006" {
		t.Fatalf("card contentOrderKey = %q, want 006 (the answer event)", got)
	}

	message := findEntryAwaiting(t, projection.Entries, func(entry map[string]any) bool {
		return entry["kind"] == "message"
	})
	if got, _ := message["contentOrderKey"].(string); got != "006" {
		t.Fatalf("message contentOrderKey = %q, want 006", got)
	}
}

// TestQuestionAnswerThenLateTerminalStaysAnswered — an answered question
// turn whose shell later carries a terminal (the rotation flow) must keep
// rendering as answered, never flip to dismissed.
func TestQuestionAnswerThenLateTerminalStaysAnswered(t *testing.T) {
	events := append(questionHandoffEvents(),
		map[string]any{
			"event_id": "e6", "type": "turn.input_answered", "actor": "user", "source": "tank",
			"turn_id": "turn_question-q1", "timeline_id": "turn_question-q1:item-1", "client_nonce": "answer-1",
			"order_key": "006", "session_id": "63",
			"payload": map[string]any{
				"question_timeline_id": "turn_question-q1:item-1",
				"answers":              map[string]any{"Proceed?": []any{"Yes"}},
			},
		},
		map[string]any{
			"event_id": "e7", "type": "turn.interrupted", "actor": "runner", "source": "claude",
			"turn_id": "turn_question-q1", "client_nonce": "question-q1", "order_key": "007", "session_id": "63",
			"payload": map[string]any{"reason": "superseded_by_answer"},
		},
	)
	projection := projectTranscriptEvents(events)
	message := findEntryAwaiting(t, projection.Entries, func(entry map[string]any) bool {
		return entry["kind"] == "message"
	})
	awaiting := message["awaitingInput"].(map[string]any)
	if answered, _ := awaiting["answered"].(bool); !answered {
		t.Fatal("answered state lost")
	}
	if dismissed, _ := awaiting["dismissed"].(bool); dismissed {
		t.Fatal("answered question must not flip to dismissed on a later terminal")
	}
}
