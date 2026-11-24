package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestMergeChatTags(t *testing.T) {
	const pinned, archived = event.RoomTag("m.favourite"), event.RoomTag("m.lowpriority")
	tests := []struct {
		name   string
		states []chatTagState
		want   *event.RoomTag
	}{
		{name: "no states"},
		{name: "single plain"},
		{name: "single archived", states: []chatTagState{{Archived: true}}, want: ptrTo(archived)},
		{name: "single pinned", states: []chatTagState{{Pinned: true}}, want: ptrTo(pinned)},
		{name: "all archived", states: []chatTagState{{Archived: true}, {Archived: true}}, want: ptrTo(archived)},
		{name: "mixed archived", states: []chatTagState{{Archived: true}, {}}},
		{name: "any pinned wins", states: []chatTagState{{Archived: true}, {Pinned: true}}, want: ptrTo(pinned)},
		{name: "pinned and archived", states: []chatTagState{{Pinned: true, Archived: true}}, want: ptrTo(pinned)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeChatTags(tt.states, pinned, archived)
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOtherStateMatters(t *testing.T) {
	tests := []struct {
		name       string
		own, other chatTagState
		want       bool
	}{
		{name: "other pinned, own plain", other: chatTagState{Pinned: true}, want: true},
		{name: "other pinned, own pinned", own: chatTagState{Pinned: true}, other: chatTagState{Pinned: true}},
		{name: "own archived, other plain", own: chatTagState{Archived: true}, want: true},
		{name: "own archived, other archived", own: chatTagState{Archived: true}, other: chatTagState{Archived: true}},
		{name: "own plain, other archived", other: chatTagState{Archived: true}},
		{name: "own plain, other plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := otherStateMatters(tt.own, tt.other); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func ptrTo(tag event.RoomTag) *event.RoomTag { return &tag }
