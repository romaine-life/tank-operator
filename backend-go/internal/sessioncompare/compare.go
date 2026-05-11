package sessioncompare

import (
	"encoding/json"
	"sort"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

type Diff struct {
	SessionID string `json:"session_id"`
	Field     string `json:"field"`
	Python    any    `json:"python"`
	Go        any    `json:"go"`
}

type Result struct {
	Match       bool   `json:"match"`
	PythonCount int    `json:"python_count"`
	GoCount     int    `json:"go_count"`
	Diffs       []Diff `json:"diffs"`
}

func Compare(pythonSessions, goSessions []sessions.Info) Result {
	pythonByID := byID(pythonSessions)
	goByID := byID(goSessions)
	ids := sortedIDs(pythonByID, goByID)

	var diffs []Diff
	for _, id := range ids {
		pythonSession, hasPython := pythonByID[id]
		goSession, hasGo := goByID[id]
		switch {
		case !hasPython:
			diffs = append(diffs, Diff{SessionID: id, Field: "_session", Python: nil, Go: fields(goSession)})
			continue
		case !hasGo:
			diffs = append(diffs, Diff{SessionID: id, Field: "_session", Python: fields(pythonSession), Go: nil})
			continue
		}

		pythonFields := fields(pythonSession)
		goFields := fields(goSession)
		for _, field := range sortedFieldNames(pythonFields, goFields) {
			if canonical(pythonFields[field]) == canonical(goFields[field]) {
				continue
			}
			diffs = append(diffs, Diff{
				SessionID: id,
				Field:     field,
				Python:    pythonFields[field],
				Go:        goFields[field],
			})
		}
	}

	return Result{
		Match:       len(diffs) == 0,
		PythonCount: len(pythonSessions),
		GoCount:     len(goSessions),
		Diffs:       diffs,
	}
}

func byID(items []sessions.Info) map[string]sessions.Info {
	out := make(map[string]sessions.Info, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func fields(item sessions.Info) map[string]any {
	return map[string]any{
		"id":            item.ID,
		"pod_name":      stringValue(item.PodName),
		"owner":         item.Owner,
		"status":        item.Status,
		"mode":          item.Mode,
		"requested_at":  stringValue(item.RequestedAt),
		"created_at":    stringValue(item.CreatedAt),
		"ready_at":      stringValue(item.ReadyAt),
		"name":          stringValue(item.Name),
		"test_state":    item.TestState,
		"rollout_state": item.RolloutState,
	}
}

func stringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func sortedIDs(left, right map[string]sessions.Info) []string {
	seen := map[string]struct{}{}
	for id := range left {
		seen[id] = struct{}{}
	}
	for id := range right {
		seen[id] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func sortedFieldNames(left, right map[string]any) []string {
	seen := map[string]struct{}{}
	for field := range left {
		seen[field] = struct{}{}
	}
	for field := range right {
		seen[field] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for field := range seen {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func canonical(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}
