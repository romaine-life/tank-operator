package main

import "testing"

func TestDefaultTokenAudienceMatchesPlatformAudience(t *testing.T) {
	if got, want := defaultTokenAudience, "https://auth.romaine.life"; got != want {
		t.Fatalf("defaultTokenAudience = %q, want %q", got, want)
	}
}
