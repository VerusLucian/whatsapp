package waid

import "testing"

func TestUserLoginMetadataCopyFromKeepsLabel(t *testing.T) {
	existing := &UserLoginMetadata{WADeviceID: 1, Label: "Work", WASAResynced: true}
	existing.CopyFrom(&UserLoginMetadata{WADeviceID: 7})
	if existing.Label != "Work" || existing.WADeviceID != 7 || existing.WASAResynced {
		t.Errorf("unexpected metadata after CopyFrom: %+v", existing)
	}
	existing.CopyFrom(&UserLoginMetadata{Label: "Private"})
	if existing.Label != "Private" {
		t.Errorf("explicit label should win, got %q", existing.Label)
	}
}
