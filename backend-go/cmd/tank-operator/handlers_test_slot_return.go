package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/glimmung"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

const defaultGlimmungProject = "tank-operator"

var glimmungSlotNamePattern = regexp.MustCompile(`^(.+)-slot-(\d+)$`)

type AppServerGlimmung interface {
	State(ctx context.Context, actorEmail string) (glimmung.StateSnapshot, error)
	ReturnTestSlot(ctx context.Context, actorEmail string, body glimmung.ReturnTestSlotRequest) (glimmung.ReturnTestSlotResult, error)
}

func (s *appServer) handleReturnTestSlot(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.glimmung == nil {
		writeError(w, http.StatusServiceUnavailable, "glimmung client not configured")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	owner := user.OwnerEmail()
	info, err := s.mgr.GetRegisteredByOwner(r.Context(), owner, sessionID)
	if err != nil {
		switch {
		case errors.Is(err, sessions.ErrNotFound), errors.Is(err, sessions.ErrNotOwned):
			writeError(w, http.StatusNotFound, "session not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if !boolFromState(info.TestState, "active") {
		writeError(w, http.StatusBadRequest, "session has no active test slot")
		return
	}
	slotIndex, ok := intFromState(info.TestState, "slot_index")
	if !ok {
		writeError(w, http.StatusBadRequest, "active test slot is missing slot_index")
		return
	}
	project := inferGlimmungProject(info.TestState)
	slotName := inferGlimmungSlotName(info.TestState)
	snapshot, err := s.glimmung.State(r.Context(), owner)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	lease, ok := matchingTestSlotLease(snapshot.ActiveLeases, project, slotIndex, slotName)
	if !ok {
		writeError(w, http.StatusNotFound, "active Glimmung test-slot lease not found")
		return
	}
	if !glimmungLeaseMatchesSession(lease, sessionID) {
		writeError(w, http.StatusForbidden, "active Glimmung test-slot lease belongs to a different session")
		return
	}
	callerSessionID := sessionID
	body := glimmung.ReturnTestSlotRequest{
		Project:         project,
		SlotIndex:       &slotIndex,
		CallerSessionID: &callerSessionID,
		Source:          "tank-operator.session-data",
		Reason:          "returned from Tank Session Data",
	}
	if slotName != "" {
		body.SlotName = &slotName
	}
	if _, err := s.glimmung.ReturnTestSlot(r.Context(), owner, body); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	updated, err := s.mgr.SetTestState(r.Context(), owner, sessionID, false, nil, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("clearing test state after lease return: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func inferGlimmungProject(state map[string]any) string {
	if value := stringFromState(state, "project"); value != "" {
		return value
	}
	if slotName := inferGlimmungSlotName(state); slotName != "" {
		if match := glimmungSlotNamePattern.FindStringSubmatch(slotName); len(match) == 3 {
			return match[1]
		}
	}
	return defaultGlimmungProject
}

func inferGlimmungSlotName(state map[string]any) string {
	if value := stringFromState(state, "slot_name"); value != "" {
		return value
	}
	rawURL := stringFromState(state, "url")
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return ""
	}
	firstLabel := strings.Split(host, ".")[0]
	if glimmungSlotNamePattern.MatchString(firstLabel) {
		return firstLabel
	}
	return ""
}

func matchingTestSlotLease(leases []glimmung.Lease, project string, slotIndex int, slotName string) (glimmung.Lease, bool) {
	for _, lease := range leases {
		if strings.TrimSpace(lease.Project) != project {
			continue
		}
		if !boolFromMap(lease.Metadata, "test_slot_checkout") {
			continue
		}
		if state := strings.TrimSpace(lease.State); state != "claimed" && state != "pending" {
			continue
		}
		if idx, ok := intFromMap(lease.Metadata, "native_slot_index"); ok && idx == slotIndex {
			return lease, true
		}
		if slotName != "" && strings.EqualFold(strings.TrimSpace(stringFromMapValue(lease.Metadata, "native_slot_name")), slotName) {
			return lease, true
		}
	}
	return glimmung.Lease{}, false
}

func glimmungLeaseMatchesSession(lease glimmung.Lease, sessionID string) bool {
	target := normalizeGlimmungTankSessionID(sessionID)
	if target == "" {
		return false
	}
	for _, value := range []string{
		stringFromMapValue(lease.Metadata, "tank_session_id"),
		stringFromMapValue(lease.Metadata, "tankSessionId"),
		stringFromMapValue(lease.Metadata, "requester_ref"),
	} {
		if glimmungRequesterValueMatchesSession(value, target) {
			return true
		}
	}
	if lease.Requester != nil {
		if glimmungRequesterValueMatchesSession(lease.Requester.Ref, target) {
			return true
		}
		for _, value := range []string{
			lease.Requester.Metadata["tank_session_id"],
			lease.Requester.Metadata["tankSessionId"],
		} {
			if glimmungRequesterValueMatchesSession(value, target) {
				return true
			}
		}
	}
	return false
}

func glimmungRequesterValueMatchesSession(value string, target string) bool {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return false
	}
	return normalizeGlimmungTankSessionID(clean) == target ||
		clean == "tank-session-"+target ||
		clean == "tank-operator/session/"+target
}

func normalizeGlimmungTankSessionID(value string) string {
	clean := strings.TrimSpace(value)
	clean = strings.TrimPrefix(clean, "session-")
	clean = strings.TrimPrefix(clean, "tank-session-")
	clean = strings.TrimPrefix(clean, "tank-operator/session/")
	return clean
}

func boolFromState(state map[string]any, key string) bool {
	if state == nil {
		return false
	}
	return boolFromMap(state, key)
}

func boolFromMap(values map[string]any, key string) bool {
	value, ok := values[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func intFromState(state map[string]any, key string) (int, bool) {
	if state == nil {
		return 0, false
	}
	return intFromMap(state, key)
}

func intFromMap(values map[string]any, key string) (int, bool) {
	value, ok := values[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
	case json.Number:
		parsed, err := strconv.Atoi(typed.String())
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	}
	return 0, false
}

func stringFromState(state map[string]any, key string) string {
	if state == nil {
		return ""
	}
	return stringFromMapValue(state, key)
}

func stringFromMapValue(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
