package main

import (
	"fmt"
	"sort"
	"strings"
)

const sessionQuestionEventLimit = 1000

type sessionQuestionSet struct {
	ID             string         `json:"id"`
	SessionID      string         `json:"session_id"`
	TurnID         string         `json:"turn_id"`
	TurnNumber     int64          `json:"turn_number,omitempty"`
	ProviderItemID string         `json:"provider_item_id"`
	TimelineID     string         `json:"timeline_id"`
	OrderKey       string         `json:"order_key"`
	CreatedAt      string         `json:"created_at"`
	Status         string         `json:"status"`
	QuestionCount  int            `json:"question_count"`
	Questions      []any          `json:"questions"`
	Answers        map[string]any `json:"answers,omitempty"`
	Annotations    map[string]any `json:"annotations,omitempty"`
	TerminalStatus string         `json:"terminal_status,omitempty"`
	TerminalAt     string         `json:"terminal_at,omitempty"`
	Summary        string         `json:"summary"`
}

type sessionQuestionProjection struct {
	SessionID    string               `json:"session_id"`
	Projection   string               `json:"projection"`
	Sets         []sessionQuestionSet `json:"sets"`
	PendingCount int                  `json:"pending_count"`
	Truncated    bool                 `json:"truncated,omitempty"`
}

func projectSessionQuestionSets(sessionID string, events []map[string]any, turnNumbers map[string]int64, truncated bool) sessionQuestionProjection {
	byTimelineID := map[string]*sessionQuestionSet{}
	sets := make([]*sessionQuestionSet, 0)

	for _, event := range orderedTranscriptEvents(events) {
		switch transcriptString(event, "type") {
		case "turn.awaiting_input":
			turnID := transcriptString(event, "turn_id")
			questions := projectionAwaitingInputQuestions(event)
			if turnID == "" || len(questions) == 0 {
				continue
			}
			timelineID := transcriptPayloadString(event, "timeline_id")
			providerItemID := transcriptPayloadString(event, "provider_item_id")
			id := sessionQuestionSetID(timelineID, turnID, providerItemID)
			set := &sessionQuestionSet{
				ID:             id,
				SessionID:      sessionID,
				TurnID:         turnID,
				TurnNumber:     turnNumbers[turnID],
				ProviderItemID: providerItemID,
				TimelineID:     timelineID,
				OrderKey:       transcriptString(event, "order_key"),
				CreatedAt:      transcriptString(event, "created_at"),
				Status:         "waiting",
				QuestionCount:  len(questions),
				Questions:      questions,
				Summary:        awaitingInputSummary(questions),
			}
			sets = append(sets, set)
			if timelineID != "" {
				byTimelineID[timelineID] = set
			}
		case "turn.input_answered":
			payload := transcriptPayload(event)
			timelineID := transcriptMapString(payload, "question_timeline_id")
			if timelineID == "" {
				continue
			}
			set := byTimelineID[timelineID]
			if set == nil {
				continue
			}
			set.Status = "answered"
			set.Answers = transcriptAnyMap(payload["answers"])
			set.Annotations = transcriptAnyMap(payload["annotations"])
		case "turn.completed", "turn.failed", "turn.command_failed", "turn.interrupted":
			turnID := transcriptString(event, "turn_id")
			if turnID == "" {
				continue
			}
			for _, set := range sets {
				if set.TurnID != turnID {
					continue
				}
				set.TerminalStatus = strings.TrimPrefix(transcriptString(event, "type"), "turn.")
				set.TerminalAt = transcriptString(event, "created_at")
				if set.Status == "waiting" {
					set.Status = set.TerminalStatus
				}
			}
		}
	}

	sort.SliceStable(sets, func(i, j int) bool {
		leftWaiting := sets[i].Status == "waiting"
		rightWaiting := sets[j].Status == "waiting"
		if leftWaiting != rightWaiting {
			return leftWaiting
		}
		return sets[i].OrderKey > sets[j].OrderKey
	})

	out := sessionQuestionProjection{
		SessionID:  sessionID,
		Projection: "server_question_sets_v1",
		Sets:       make([]sessionQuestionSet, 0, len(sets)),
		Truncated:  truncated,
	}
	for _, set := range sets {
		if set.Status == "waiting" {
			out.PendingCount++
		}
		out.Sets = append(out.Sets, *set)
	}
	return out
}

func sessionQuestionSetID(timelineID, turnID, providerItemID string) string {
	if strings.TrimSpace(timelineID) != "" {
		return strings.TrimSpace(timelineID) + ":awaiting_input"
	}
	if strings.TrimSpace(providerItemID) != "" {
		return strings.TrimSpace(turnID) + ":" + strings.TrimSpace(providerItemID) + ":awaiting_input"
	}
	return fmt.Sprintf("%s:awaiting_input", strings.TrimSpace(turnID))
}
