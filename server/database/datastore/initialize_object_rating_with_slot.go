package datastore_db

import (
	"github.com/PretendoNetwork/nex-go/v2"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
)

// ProfileExpertSlot and ProfileSuperExpertSlot are the rating slots of a player's own profile object (data type 1) that the game
// reads to decide whether the 100 Mario Challenge's Expert and Super Expert are unlocked: 1 means unlocked, 0 locked. The
// original servers raised them when a player earned the unlocks; a server that starts a profile at zero, as every new server
// does, makes the game forget the unlocks it had (found by setting both to 1 on a Wii U's profile: both difficulties unlocked).
// The home server has nothing to earn them with, so a profile starts with both unlocked.
const (
	ProfileExpertSlot      = 5
	ProfileSuperExpertSlot = 6
)

func InitializeObjectRatingWithSlot(dataID uint64, param datastore_types.DataStoreRatingInitParamWithSlot) *nex.Error {
	_, err := database.DB.Exec(`INSERT INTO datastore.object_ratings (
		data_id,
		slot,
		flag,
		internal_flag,
		lock_type,
		initial_value,
		range_min,
		range_max,
		period_hour,
		period_duration,
		total_value
	) VALUES (
		$1,
		$2,
		$3,
		$4,
		$5,
		$6,
		$7,
		$8,
		$9,
		$10,
		$11
	)`,
		dataID,
		param.Slot,
		param.Param.Flag,
		param.Param.InternalFlag,
		param.Param.LockType,
		param.Param.InitialValue,
		param.Param.RangeMin,
		param.Param.RangeMax,
		param.Param.PeriodHour,
		param.Param.PeriodDuration,
		param.Param.InitialValue, // * Start the value off at the initial value
	)

	if err != nil {
		globals.Logger.Error(err.Error())
		// TODO - Send more specific errors?
		return nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	// * a new profile starts with Expert and Super Expert unlocked (see ProfileExpertSlot)
	if param.Slot == ProfileExpertSlot || param.Slot == ProfileSuperExpertSlot {
		if _, err := database.DB.Exec(`UPDATE datastore.object_ratings SET total_value = 1, count = 1
			WHERE data_id = $1 AND slot = $2 AND EXISTS (SELECT 1 FROM datastore.objects WHERE data_id = $1 AND data_type = 1)`,
			dataID, param.Slot); err != nil {
			globals.Logger.Warningf("Could not unlock the 100 Mario Challenge difficulties on the new profile %d: %s", dataID, err.Error())
		}
	}

	return nil
}
