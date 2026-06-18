package sessionbus

import "testing"

// TestNormalizeCommandAttachments proves the additive answer-attachment field
// is normalized defensively: kind defaults to "file" unless it is exactly
// "image", strings are trimmed, negative sizes are clamped, and entries the
// runner could never act on (no path at all, or no label/name) are dropped so
// they never reach the runner's file-read path.
func TestNormalizeCommandAttachments(t *testing.T) {
	in := Command{
		Type: "input_reply",
		Attachments: []CommandAttachment{
			{Label: "  Screenshot 1 ", Name: " shot.png ", Kind: "image", Path: " screenshots/3.png ", AbsPath: " /workspace/screenshots/3.png ", Size: 4096},
			{Label: "notes.txt", Kind: "weird", Path: "notes.txt"}, // kind coerced to file
			{Label: "dropped — no path", Name: "x"},                // dropped: no path and no abs_path
			{Path: "orphan.bin"},                                   // dropped: no label and no name
			{Label: "neg", Path: "n", Size: -5},                    // size clamped to 0
		},
	}

	got := in.Normalize().Attachments
	if len(got) != 3 {
		t.Fatalf("normalized attachments = %#v, want 3 kept", got)
	}
	if got[0].Label != "Screenshot 1" || got[0].Name != "shot.png" ||
		got[0].Kind != "image" || got[0].Path != "screenshots/3.png" ||
		got[0].AbsPath != "/workspace/screenshots/3.png" {
		t.Fatalf("attachment[0] not trimmed/kept correctly: %#v", got[0])
	}
	if got[1].Kind != "file" {
		t.Fatalf("attachment[1].Kind = %q, want coerced to file", got[1].Kind)
	}
	if got[2].Size != 0 {
		t.Fatalf("attachment[2].Size = %d, want clamped to 0", got[2].Size)
	}
}

// TestNormalizeCommandAttachmentsEmptyStaysNil keeps the field absent on the
// wire for text-only answers (and every non-input_reply command).
func TestNormalizeCommandAttachmentsEmptyStaysNil(t *testing.T) {
	if got := (Command{Type: "submit_turn"}).Normalize().Attachments; got != nil {
		t.Fatalf("attachments = %#v, want nil for a command with none", got)
	}
	none := Command{Attachments: []CommandAttachment{{Name: "only-name-no-path"}}}
	if got := none.Normalize().Attachments; got != nil {
		t.Fatalf("attachments = %#v, want nil when every entry is dropped", got)
	}
}
