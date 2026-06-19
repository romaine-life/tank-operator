package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/glimmung"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

type fakeGlimmungClient struct {
	stateReqEmail    string
	returnReqEmail   string
	checkoutReqEmail string
	deployReqEmail   string
	returnReq        glimmung.ReturnTestSlotRequest
	checkoutReq      glimmung.CheckoutTestSlotRequest
	deployReq        glimmung.DeployImageToTestSlotRequest
	state            glimmung.StateSnapshot
	checkoutResult   glimmung.CheckoutTestSlotResult
	deployResult     glimmung.DeployImageToTestSlotResult
	checkoutCalls    int
	deployCalls      int
}

func (f *fakeGlimmungClient) State(_ context.Context, actorEmail string) (glimmung.StateSnapshot, error) {
	f.stateReqEmail = actorEmail
	return f.state, nil
}

func (f *fakeGlimmungClient) ReturnTestSlot(_ context.Context, actorEmail string, body glimmung.ReturnTestSlotRequest) (glimmung.ReturnTestSlotResult, error) {
	f.returnReqEmail = actorEmail
	f.returnReq = body
	return glimmung.ReturnTestSlotResult{State: "cleaning", Project: body.Project, CleanupStarted: true}, nil
}

func (f *fakeGlimmungClient) CheckoutTestSlot(_ context.Context, actorEmail string, body glimmung.CheckoutTestSlotRequest) (glimmung.CheckoutTestSlotResult, error) {
	f.checkoutCalls++
	f.checkoutReqEmail = actorEmail
	f.checkoutReq = body
	if f.checkoutResult.Lease == "" {
		idx := 1
		name := "tank-operator-slot-1"
		url := "https://tank-operator-slot-1.tank.dev.romaine.life/"
		f.checkoutResult = glimmung.CheckoutTestSlotResult{
			State: "active", Project: body.Project, SlotIndex: &idx, SlotName: &name,
			URL: &url, Lease: "lease-1", Usable: true,
		}
	}
	return f.checkoutResult, nil
}

func (f *fakeGlimmungClient) DeployImageToTestSlot(_ context.Context, actorEmail string, body glimmung.DeployImageToTestSlotRequest) (glimmung.DeployImageToTestSlotResult, error) {
	f.deployCalls++
	f.deployReqEmail = actorEmail
	f.deployReq = body
	if f.deployResult.Job == "" {
		f.deployResult = glimmung.DeployImageToTestSlotResult{Lease: "lease-1", Job: "deploy-1", Status: "running", GitRef: body.GitRef}
	}
	return f.deployResult, nil
}

func TestHandleReturnTestSlotReturnsOwnedLeaseAndClearsState(t *testing.T) {
	registry := newTestSessionRegistry(sessionmodel.SessionRecord{
		ID:      "99",
		Email:   otherUser,
		Mode:    "claude_gui",
		Scope:   prodSessionScope,
		PodName: "session-99",
		Name:    "lease session",
		Visible: true,
		Status:  "Active",
		TestState: map[string]any{
			"active":     true,
			"slot_index": 3,
			"url":        "https://tank-operator-slot-3.tank.dev.romaine.life/",
		},
	})
	glim := &fakeGlimmungClient{
		state: glimmung.StateSnapshot{
			ActiveLeases: []glimmung.Lease{{
				Project: "tank-operator",
				State:   "claimed",
				Metadata: map[string]any{
					"test_slot_checkout": true,
					"runner_slot_index":  "3",
					"runner_slot_name":   "tank-operator-slot-3",
					"requester_ref":      "tank-session-99",
				},
			}},
		},
	}
	app := &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		mgr: sessions.NewManager(
			fake.NewSimpleClientset(activitySessionPod("99", otherUser)),
			nil,
			sessionmodel.SessionsNamespace,
			registry,
			nil,
			sessions.ManagerOptions{},
		),
		glimmung: glim,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/99/test-slot/return", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "99")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleReturnTestSlot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if glim.stateReqEmail != otherUser || glim.returnReqEmail != otherUser {
		t.Fatalf("glimmung actor emails state=%q return=%q", glim.stateReqEmail, glim.returnReqEmail)
	}
	if glim.returnReq.Project != "tank-operator" || glim.returnReq.SlotIndex == nil || *glim.returnReq.SlotIndex != 3 {
		t.Fatalf("return request = %#v", glim.returnReq)
	}
	if glim.returnReq.SlotName == nil || *glim.returnReq.SlotName != "tank-operator-slot-3" {
		t.Fatalf("return slot name = %#v", glim.returnReq.SlotName)
	}
	if got := registry.records[otherUser]["99"].TestState["active"]; got != false {
		t.Fatalf("registry active=%#v, want false", got)
	}
}

func TestHandleReturnTestSlotRejectsLeaseFromDifferentSession(t *testing.T) {
	registry := newTestSessionRegistry(sessionmodel.SessionRecord{
		ID:      "99",
		Email:   otherUser,
		Mode:    "claude_gui",
		Scope:   prodSessionScope,
		PodName: "session-99",
		Name:    "lease session",
		Visible: true,
		Status:  "Active",
		TestState: map[string]any{
			"active":     true,
			"slot_index": 3,
			"url":        "https://tank-operator-slot-3.tank.dev.romaine.life/",
		},
	})
	glim := &fakeGlimmungClient{
		state: glimmung.StateSnapshot{
			ActiveLeases: []glimmung.Lease{{
				Project: "tank-operator",
				State:   "claimed",
				Metadata: map[string]any{
					"test_slot_checkout": true,
					"runner_slot_index":  3,
					"requester_ref":      "tank-session-123",
				},
			}},
		},
	}
	app := &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		mgr: sessions.NewManager(
			fake.NewSimpleClientset(activitySessionPod("99", otherUser)),
			nil,
			sessionmodel.SessionsNamespace,
			registry,
			nil,
			sessions.ManagerOptions{},
		),
		glimmung: glim,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/99/test-slot/return", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "99")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleReturnTestSlot(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if glim.returnReq.Project != "" {
		t.Fatalf("return should not be called: %#v", glim.returnReq)
	}
}
