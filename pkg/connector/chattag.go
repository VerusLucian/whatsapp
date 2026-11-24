package connector

import (
	"context"
	"errors"
	"slices"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/matrix"
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

// shouldRemoveTag decides whether the bridge removes a tag from a room after an explicit unpin or
// unarchive. This mirrors the conditions under which bridgev2 would manage the tag at all (a
// non-empty only_bridge_tags containing it, and tag_only_on_create disabled), and only touches
// rooms that actually have the tag.
func shouldRemoveTag(tag event.RoomTag, onlyBridgeTags []event.RoomTag, tagOnlyOnCreate, roomHasTag bool) bool {
	return roomHasTag && !tagOnlyOnCreate && tag != "" && slices.Contains(onlyBridgeTags, tag)
}

// removeTagInfoChange removes a single tag from the room of the user's double puppet when handled.
// bridgev2's TagRoom can't be used for this: it removes both m.favourite and m.lowpriority.
type removeTagInfoChange struct {
	*simplevent.ChatInfoChange
	wa  *WhatsAppClient
	tag event.RoomTag
}

func (evt *removeTagInfoChange) GetChatInfoChange(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfoChange, error) {
	log := zerolog.Ctx(ctx).With().Str("tag", string(evt.tag)).Logger()
	bridgeConfig := evt.wa.Main.Bridge.Config
	if portal.MXID == "" || !shouldRemoveTag(evt.tag, bridgeConfig.OnlyBridgeTags, bridgeConfig.TagOnlyOnCreate, true) {
		return nil, nil
	}
	intent, ok := evt.wa.UserLogin.User.DoublePuppet(ctx).(*matrix.ASIntent)
	if !ok {
		// No double puppet (or a different Matrix connector), so there's no way to change the user's tags.
		return nil, nil
	}
	tags, err := intent.Matrix().GetTags(ctx, portal.MXID)
	if err != nil {
		log.Err(err).Msg("Failed to get room tags to remove tag")
		return nil, nil
	}
	if !shouldRemoveTag(evt.tag, bridgeConfig.OnlyBridgeTags, bridgeConfig.TagOnlyOnCreate, hasRoomTag(tags, evt.tag)) {
		return nil, nil
	}
	if err = intent.Matrix().RemoveTag(ctx, portal.MXID, evt.tag); err != nil {
		log.Err(err).Msg("Failed to remove room tag")
	}
	return nil, nil
}

func hasRoomTag(tags event.TagEventContent, tag event.RoomTag) bool {
	_, ok := tags.Tags[tag]
	return ok
}
