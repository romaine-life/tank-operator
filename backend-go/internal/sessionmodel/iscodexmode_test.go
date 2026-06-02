package sessionmodel

import "testing"

func TestIsCodexMode(t *testing.T) {
	for _, mode := range []string{
		CodexConfigMode, CodexCLIMode, CodexGUIMode, CodexExecGUIMode, CodexAppServerMode,
	} {
		if !IsCodexMode(mode) {
			t.Errorf("IsCodexMode(%q) = false, want true", mode)
		}
	}
	for _, mode := range []string{
		ClaudeGUIMode, ClaudeCLIMode, APIKeyMode, ConfigMode, "", "bogus",
	} {
		if IsCodexMode(mode) {
			t.Errorf("IsCodexMode(%q) = true, want false", mode)
		}
	}
}
