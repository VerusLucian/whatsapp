package wadb

import (
	"context"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const getDMPortalKeysByReceiverQuery = `
	SELECT id, receiver FROM portal
	WHERE bridge_id=$1 AND receiver=$2 AND room_type='dm' AND mxid IS NOT NULL AND mxid<>''
`

func scanPortalKey(row dbutil.Scannable) (key networkid.PortalKey, err error) {
	err = row.Scan(&key.ID, &key.Receiver)
	return
}

// GetDMPortalKeysByReceiver returns the keys of all DM portals with a Matrix room that belong to
// the given login. It reads the bridgev2 portal table, independent of user_portal rows.
func (db *Database) GetDMPortalKeysByReceiver(ctx context.Context, bridgeID networkid.BridgeID, receiver networkid.UserLoginID) ([]networkid.PortalKey, error) {
	return dbutil.ConvertRowFn[networkid.PortalKey](scanPortalKey).
		NewRowIter(db.Query(ctx, getDMPortalKeysByReceiverQuery, bridgeID, receiver)).
		AsList()
}
