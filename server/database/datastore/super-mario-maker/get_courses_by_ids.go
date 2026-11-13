package datastore_smm_db

import (
	"database/sql"

	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_super_mario_maker_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_db "github.com/PretendoNetwork/super-mario-maker/database/datastore"
	"github.com/PretendoNetwork/super-mario-maker/globals"
)

// GetCoursesByIDs renders a list of courses in exactly the order given (a chosen or seeded selection). The score is the
// course's value in the custom ranking applicationID, 0 if it has none (unlike GetCustomRankingsByDataIDs, a course
// without a ranking row is kept). IDs the catalog does not hold are left out. With firstOrder > 0 the entries are numbered
// from there (a ranked list); with 0 every Order is 0 (what random lists use).
func GetCoursesByIDs(applicationID uint32, ids []int64, firstOrder int) types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult] {
	out := make(types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult], 0, len(ids))

	for _, id := range ids {
		info, nexError := datastore_db.GetObjectInfoByDataID(types.UInt64(id))
		if nexError != nil {
			globals.Logger.Warningf("Skipping course %d in a list: result code %d", id, nexError.ResultCode)
			continue
		}

		var score int64

		err := database.DB.QueryRow(`SELECT value FROM datastore.object_custom_rankings WHERE data_id = $1 AND application_id = $2`, id, applicationID).Scan(&score)
		if err != nil && err != sql.ErrNoRows {
			globals.Logger.Error(err.Error())
		}

		result := datastore_super_mario_maker_types.NewDataStoreCustomRankingResult()
		result.Score = types.NewUInt32(uint32(max(score, 0)))
		result.MetaInfo = info

		if firstOrder > 0 {
			result.Order = types.NewUInt32(uint32(firstOrder + len(out)))
		}

		out = append(out, result)
	}

	return out
}
