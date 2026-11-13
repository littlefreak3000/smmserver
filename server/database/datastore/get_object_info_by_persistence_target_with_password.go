package datastore_db

import (
	"database/sql"
	"time"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
)

func GetObjectInfoByPersistenceTargetWithPassword(persistenceTarget datastore_types.DataStorePersistenceTarget, password types.UInt64) (datastore_types.DataStoreMetaInfo, *nex.Error) {
	meta, e := getObjectInfoByPersistenceTarget(persistenceTarget, password, false)

	if e != nil {
		globals.Tracef("GetMeta by owner %d slot %d: %s", persistenceTarget.OwnerID, persistenceTarget.PersistenceSlotID, e.Message)
	} else {
		globals.Tracef("GetMeta by owner %d slot %d: data %d type %d name %q meta_binary %d bytes size %d", persistenceTarget.OwnerID, persistenceTarget.PersistenceSlotID, meta.DataID, meta.DataType, meta.Name, len(meta.MetaBinary), meta.Size)
	}

	return meta, e
}

func getObjectInfoByPersistenceTarget(persistenceTarget datastore_types.DataStorePersistenceTarget, password types.UInt64, retried bool) (datastore_types.DataStoreMetaInfo, *nex.Error) {
	metaInfo := datastore_types.NewDataStoreMetaInfo()
	metaInfo.Permission = datastore_types.NewDataStorePermission()
	metaInfo.DelPermission = datastore_types.NewDataStorePermission()
	metaInfo.ExpireTime = types.NewDateTime(0x9C3F3E0000) // * 9999-12-31T00:00:00.000Z. This is what the real server sends
	metaInfo.Ratings = types.NewList[datastore_types.DataStoreRatingInfoWithSlot]()

	var accessPassword types.UInt64
	var underReview bool
	var createdDate time.Time
	var updatedDate time.Time
	var tagArray []string

	err := database.DB.QueryRow(`SELECT
		data_id,
		owner,
		size,
		name,
		data_type,
		meta_binary,
		permission,
		permission_recipients,
		delete_permission,
		delete_permission_recipients,
		period,
		refer_data_id,
		flag,
		tags,
		creation_date,
		update_date,
		access_password,
		under_review
	FROM datastore.objects WHERE owner=$1 AND persistence_slot_id=$2 AND upload_completed=TRUE AND deleted=FALSE`, persistenceTarget.OwnerID, persistenceTarget.PersistenceSlotID).Scan(
		&metaInfo.DataID,
		&metaInfo.OwnerID,
		&metaInfo.Size,
		&metaInfo.Name,
		&metaInfo.DataType,
		&metaInfo.MetaBinary,
		&metaInfo.Permission.Permission,
		database.Array(&metaInfo.Permission.RecipientIDs),
		&metaInfo.DelPermission.Permission,
		database.Array(&metaInfo.DelPermission.RecipientIDs),
		&metaInfo.Period,
		&metaInfo.ReferDataID,
		&metaInfo.Flag,
		database.Array(&tagArray),
		&createdDate,
		&updatedDate,
		&accessPassword,
		&underReview,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			// * a maker the archive knows (creator, record holder) gets a profile object made on the spot
			if !retried && database.OnMissingProfile != nil && persistenceTarget.PersistenceSlotID == 0 && database.OnMissingProfile(uint32(persistenceTarget.OwnerID)) {
				return getObjectInfoByPersistenceTarget(persistenceTarget, password, true)
			}

			globals.Tracef("profile of player %d (slot %d) not found", persistenceTarget.OwnerID, persistenceTarget.PersistenceSlotID)

			return datastore_types.NewDataStoreMetaInfo(), nex.NewError(nex.ResultCodes.DataStore.NotFound, "Object not found")
		}

		globals.Logger.Error(err.Error())

		// TODO - Send more specific errors?
		return datastore_types.NewDataStoreMetaInfo(), nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	if accessPassword != 0 && accessPassword != password {
		return datastore_types.NewDataStoreMetaInfo(), nex.NewError(nex.ResultCodes.DataStore.InvalidPassword, "Invalid password")
	}

	if underReview {
		return datastore_types.NewDataStoreMetaInfo(), nex.NewError(nex.ResultCodes.DataStore.UnderReviewing, "This object is currently under review")
	}

	ratings, nexError := GetObjectRatingsWithSlotByDataIDWithPassword(metaInfo.DataID, password)
	if nexError != nil {
		globals.Logger.Errorf("Failed to get ratings for object %d with password %d", metaInfo.DataID, password)
		return datastore_types.NewDataStoreMetaInfo(), nexError
	}

	metaInfo.Tags = make(types.List[types.String], 0, len(tagArray))
	for i := range tagArray {
		metaInfo.Tags = append(metaInfo.Tags, types.String(tagArray[i]))
	}

	metaInfo.Ratings = ratings

	metaInfo.CreatedTime.FromTimestamp(createdDate)
	metaInfo.UpdatedTime.FromTimestamp(updatedDate)
	metaInfo.ReferredTime.FromTimestamp(createdDate) // * This is what the real server does

	return metaInfo, nil
}
