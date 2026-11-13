package datastore_db

import (
	"database/sql"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
)

// maxRatingSlot is the highest rating slot created on demand.
const maxRatingSlot = 31

func RateObjectWithPassword(dataID types.UInt64, slot types.UInt8, ratingValue types.Int32, accessPassword types.UInt64) (datastore_types.DataStoreRatingInfo, *nex.Error) {
	nexError := IsObjectAvailableWithPassword(dataID, accessPassword)
	if nexError != nil {
		return datastore_types.NewDataStoreRatingInfo(), nexError
	}

	rating := datastore_types.NewDataStoreRatingInfo()

	err := database.DB.QueryRow(`
		UPDATE datastore.object_ratings
		SET total_value=total_value+$1, count=count+1
		WHERE data_id=$2 AND slot=$3
		RETURNING total_value, count, initial_value`, ratingValue, dataID, slot,
	).Scan(
		&rating.TotalValue,
		&rating.Count,
		&rating.InitialValue,
	)

	if err != nil {
		// * No row: the object exists (checked above) but has no rating in this slot. The real server created the
		// * slots when the course was uploaded; archived levels only carry the counters we know (0-4), and the game
		// * rates others when a level is played (the first run found it does). Create the slot instead of refusing,
		// * so playing an archived level does not end in an error. Slots far outside any sane range still fail.
		if err == sql.ErrNoRows {
			if slot > maxRatingSlot {
				return datastore_types.NewDataStoreRatingInfo(), nex.NewError(nex.ResultCodes.DataStore.InvalidArgument, "Invalid argument")
			}

			globals.Tracef("rating: level %d has no slot %d; created it", dataID, slot)

			_, err = database.DB.Exec(`
				INSERT INTO datastore.object_ratings (data_id, slot, flag, internal_flag, lock_type, initial_value,
					range_min, range_max, period_hour, period_duration, total_value, count)
				VALUES ($1, $2, 0, 0, 0, 0, 0, 2147483647, 0, 0, $3, 1)`, dataID, slot, ratingValue)
			if err != nil {
				globals.Logger.Error(err.Error())

				return datastore_types.NewDataStoreRatingInfo(), nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
			}

			rating.TotalValue = types.NewInt64(int64(ratingValue))
			rating.Count = types.NewUInt32(1)
			rating.InitialValue = types.NewInt64(0)

			return rating, nil
		}

		globals.Logger.Error(err.Error())
		// TODO - Send more specific errors?
		return datastore_types.NewDataStoreRatingInfo(), nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	return rating, nil
}
