package sessionregistry

import (
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func TestValidateSessionRecordForWriteRejectsVisibleRowsWithoutAgentAvatar(t *testing.T) {
	err := validateSessionRecordForWrite(sessionmodel.SessionRecord{
		ID:      "223",
		Visible: true,
	})
	if err == nil || !strings.Contains(err.Error(), "missing durable agent avatar id") {
		t.Fatalf("err = %v, want missing durable agent avatar id", err)
	}
}

func TestValidateSessionRecordForWriteAllowsInvisibleRowsWithoutAgentAvatar(t *testing.T) {
	if err := validateSessionRecordForWrite(sessionmodel.SessionRecord{
		ID:      "223",
		Visible: false,
	}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestValidateSessionRecordForWriteAllowsVisibleRowsWithAgentAvatar(t *testing.T) {
	if err := validateSessionRecordForWrite(sessionmodel.SessionRecord{
		ID:            "223",
		Visible:       true,
		AgentAvatarID: "av_agent",
	}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}
