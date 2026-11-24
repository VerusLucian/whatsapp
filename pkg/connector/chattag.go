package connector

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

// chatTagState is the pinned/archived state of a chat in one WhatsApp login.
type chatTagState struct {
	Pinned   bool
	Archived bool
}

// mergeChatTags decides the room tag for a portal shared by several logins. Any login pinning the
// chat pins it, the chat is only archived when every login in it has archived it, and otherwise
// there is no opinion (nil), so tags the user set manually are left alone.
func mergeChatTags(states []chatTagState, pinnedTag, archiveTag event.RoomTag) *event.RoomTag {
	if len(states) == 0 {
		return nil
	}
	allArchived := true
	for _, state := range states {
		if state.Pinned {
			return &pinnedTag
		}
		allArchived = allArchived && state.Archived
	}
	if allArchived {
		return &archiveTag
	}
	return nil
}

// otherStateMatters returns whether another login's state can change the merged tag compared to
// that login not being in the chat at all. It is used to skip membership checks that can't matter.
func otherStateMatters(own, other chatTagState) bool {
	if other.Pinned {
		return !own.Pinned
	}
	return own.Archived && !own.Pinned && !other.Archived
}

// getChatTagState reads this login's pinned/archived state for the chat from the whatsmeow store.
func (wa *WhatsAppClient) getChatTagState(ctx context.Context, chatJID types.JID) (chatTagState, error) {
	settings, err := wa.GetStore().ChatSettings.GetChatSettings(ctx, chatJID)
	if err != nil {
		return chatTagState{}, err
	}
	return chatTagState{Pinned: settings.Pinned, Archived: settings.Archived}, nil
}

// computeChatTag merges the given state of this login with the state of the user's other logins
// that really are in the group. Non-group chats are private to a login, so only own is used.
// A nil result means the chat has neither pinned nor archived consensus.
func (wa *WhatsAppClient) computeChatTag(ctx context.Context, chatJID types.JID, own chatTagState) *event.RoomTag {
	states := []chatTagState{own}
	if chatJID.Server == types.GroupServer {
		states = append(states, wa.otherLoginTagStates(ctx, chatJID, own)...)
	}
	return mergeChatTags(states, wa.Main.Config.PinnedTag, wa.Main.Config.ArchiveTag)
}

func (wa *WhatsAppClient) otherLoginTagStates(ctx context.Context, chatJID types.JID, own chatTagState) []chatTagState {
	log := zerolog.Ctx(ctx)
	portalKey := wa.makeWAPortalKey(chatJID)
	var states []chatTagState
	for _, other := range wa.UserLogin.User.GetUserLogins() {
		otherWA, ok := other.Client.(*WhatsAppClient)
		if other.ID == wa.UserLogin.ID || !ok || !otherWA.IsLoggedIn() {
			continue
		}
		log := log.With().Str("other_login_id", string(other.ID)).Logger()
		// The user_portal row is only a hint: it is also created for logins that merely received events.
		userPortal, err := wa.Main.Bridge.DB.UserPortal.Get(ctx, other.UserLogin, portalKey)
		if err != nil {
			log.Err(err).Msg("Failed to check if other login is in portal for chat tag")
			continue
		} else if userPortal == nil {
			continue
		}
		otherState, err := otherWA.getChatTagState(ctx, chatJID)
		if err != nil {
			log.Err(err).Msg("Failed to get other login's chat settings for chat tag")
			continue
		} else if !otherStateMatters(own, otherState) {
			continue
		}
		inGroup, err := otherWA.isInGroup(ctx, chatJID)
		if err != nil {
			log.Err(err).Msg("Failed to check if other login is in group for chat tag")
			continue
		} else if !inGroup {
			wa.forgetGroupMembership(ctx, other, portalKey)
			continue
		}
		states = append(states, otherState)
	}
	return states
}

// isInGroup checks with the WhatsApp servers whether this login participates in the group.
func (wa *WhatsAppClient) isInGroup(ctx context.Context, groupJID types.JID) (bool, error) {
	_, err := wa.Client.GetGroupInfo(ctx, groupJID)
	if errors.Is(err, whatsmeow.ErrNotInGroup) || errors.Is(err, whatsmeow.ErrGroupNotFound) {
		return false, nil
	}
	return err == nil, err
}

// forgetGroupMembership deletes the user_portal row of a login that is no longer in the group, so
// it is not mistaken for a participant later.
func (wa *WhatsAppClient) forgetGroupMembership(ctx context.Context, login *bridgev2.UserLogin, key networkid.PortalKey) {
	userPortal, err := wa.Main.Bridge.DB.UserPortal.Get(ctx, login.UserLogin, key)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("login_id", string(login.ID)).Msg("Failed to get user portal to delete after leaving group")
		return
	} else if userPortal == nil {
		return
	}
	if err = wa.Main.Bridge.DB.UserPortal.Delete(ctx, userPortal); err != nil {
		zerolog.Ctx(ctx).Err(err).Str("login_id", string(login.ID)).Msg("Failed to delete user portal after leaving group")
	}
}

// ownLeaveInfoChange is a group info change in which this login itself left or was removed.
// Once the event is being handled, the login's user_portal row is deleted, because the row is
// created when the event is queued and would otherwise keep the login looking like a participant.
type ownLeaveInfoChange struct {
	*simplevent.ChatInfoChange
	wa *WhatsAppClient
}

func (evt *ownLeaveInfoChange) GetChatInfoChange(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfoChange, error) {
	evt.wa.forgetGroupMembership(ctx, evt.wa.UserLogin, evt.PortalKey)
	return evt.ChatInfoChange.GetChatInfoChange(ctx, portal)
}
