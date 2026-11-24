package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const (
	labelSeparator = " · "
	// maxLabelLength is the maximum number of characters in a login label.
	maxLabelLength = 32
	clearLabelArg  = "clear"
)

func nameWithLabel(name, label string) string {
	return name + labelSeparator + label
}

// validateLabel trims the label and checks that it is short and has no control characters
// (the label ends up in room names).
func validateLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", errors.New("the label can't be empty (use `clear` to remove it)")
	}
	if utf8.RuneCountInString(label) > maxLabelLength {
		return "", fmt.Errorf("the label is too long (maximum %d characters)", maxLabelLength)
	}
	for _, r := range label {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", errors.New("the label can't contain control characters or line breaks")
		}
	}
	return label, nil
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// labelCommandArgs is the parsed form of the label command's arguments.
type labelCommandArgs struct {
	LoginID networkid.UserLoginID
	// Rest is what remains after the phone number: empty to show the label, otherwise the label or "clear".
	Rest []string
}

// parseLabelArgs resolves which login a label command targets. The phone number may only be omitted
// when the user has exactly one login and the first argument contains no digits.
func parseLabelArgs(args []string, loginIDs []networkid.UserLoginID) (*labelCommandArgs, error) {
	if len(args) == 0 {
		return nil, errors.New("missing arguments")
	}
	phone := digitsOnly(args[0])
	if phone == "" {
		if len(loginIDs) == 1 {
			return &labelCommandArgs{LoginID: loginIDs[0], Rest: args}, nil
		}
		return nil, errors.New("the first argument must be the phone number of one of your logins")
	}
	for _, id := range loginIDs {
		if string(id) == phone {
			return &labelCommandArgs{LoginID: id, Rest: args[1:]}, nil
		}
	}
	return nil, fmt.Errorf("you're not logged in with +%s", phone)
}

// labeledDMNeedsUpdate returns whether a labeled DM portal's name or avatar is out of date
// compared to the ghost. It makes the ghost update hook idempotent, so it only queues a resync
// when something really differs.
func labeledDMNeedsUpdate(portalName string, portalAvatarID networkid.AvatarID, ghostName string, ghostAvatarID networkid.AvatarID, label string) bool {
	if ghostName != "" && portalName != nameWithLabel(ghostName, label) {
		return true
	}
	return portalAvatarID != ghostAvatarID
}

// hasLabeledLogin returns whether any loaded login has a label.
func (wc *WhatsAppConnector) hasLabeledLogin() bool {
	for _, login := range wc.Bridge.GetAllCachedUserLogins() {
		if meta, ok := login.Metadata.(*waid.UserLoginMetadata); ok && meta.Label != "" {
			return true
		}
	}
	return false
}

// resyncLabeledDMsUpdater is an ExtraUpdater for ghost info, so every ghost update path (including
// the ones bridgev2 triggers itself) refreshes the labeled DMs with the ghost. It runs after the
// name and avatar are applied to the ghost, inside the ghost's sync lock, so the actual work is done
// in the background. It never reports a ghost change.
func (wc *WhatsAppConnector) resyncLabeledDMsUpdater(ctx context.Context, ghost *bridgev2.Ghost) bool {
	if !wc.hasLabeledLogin() {
		return false
	}
	ghostID, name, avatarID := ghost.ID, ghost.Name, ghost.AvatarID
	log := zerolog.Ctx(ctx).With().Logger()
	go wc.resyncLabeledDMs(log.WithContext(wc.Bridge.BackgroundCtx), ghostID, name, avatarID)
	return false
}

// resyncLabeledDMs queues a chat resync for the labeled DM portals with the ghost whose name or
// avatar don't match the ghost anymore. Labeled DMs have a custom name and avatar, so they don't
// follow the ghost automatically. Resyncs are deduplicated per portal until the queued one is processed.
func (wc *WhatsAppConnector) resyncLabeledDMs(ctx context.Context, ghostID networkid.UserID, name string, avatarID networkid.AvatarID) {
	portals, err := wc.Bridge.GetDMPortalsWith(ctx, ghostID)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get DM portals to resync after ghost update")
		return
	}
	for _, portal := range portals {
		if portal.MXID == "" {
			continue
		}
		login := wc.Bridge.GetCachedUserLoginByID(portal.Receiver)
		if login == nil {
			continue
		}
		wa, ok := login.Client.(*WhatsAppClient)
		if !ok {
			continue
		}
		label := wa.UserLogin.Metadata.(*waid.UserLoginMetadata).Label
		if label == "" || !labeledDMNeedsUpdate(portal.Name, portal.AvatarID, name, avatarID, label) {
			continue
		}
		wc.queueDMResync(wa, portal.PortalKey, "ghost update", wa.GetChatInfo)
	}
}

// queueDMResync queues a deduplicated chat resync for a DM portal of the login.
func (wc *WhatsAppConnector) queueDMResync(
	wa *WhatsAppClient,
	key networkid.PortalKey,
	reason string,
	getChatInfo func(context.Context, *bridgev2.Portal) (*bridgev2.ChatInfo, error),
) {
	if !wc.pendingDMResyncs.Add(key) {
		return
	}
	res := wa.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatResync,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("sync_reason", reason)
			},
			PortalKey: key,
		},
		GetChatInfoFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
			wc.pendingDMResyncs.Remove(key)
			return getChatInfo(ctx, portal)
		},
	})
	if !res.Success {
		wc.pendingDMResyncs.Remove(key)
	}
}
