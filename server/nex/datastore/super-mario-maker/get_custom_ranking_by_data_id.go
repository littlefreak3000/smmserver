package nex_datastore_super_mario_maker

import (
	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_super_mario_maker "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker"
	datastore_super_mario_maker_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker/types"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_smm_db "github.com/PretendoNetwork/super-mario-maker/database/datastore/super-mario-maker"
	"github.com/PretendoNetwork/super-mario-maker/globals"
)

// maxOnDemandPerSearch is how many levels one by-ID request may fetch from the archive before it answers with what it has.
const maxOnDemandPerSearch = 3

// bringIntoCatalog gives on-demand fetching (when it is enabled) a chance to fetch the levels that a by-ID request names and the
// catalog does not hold, so searching for the course ID of an archived level that has not been downloaded yet finds it. This
// request omits what it cannot find, so without this the game would just say the course does not exist. At most
// maxOnDemandPerSearch levels are fetched per request, so a long list cannot stall the game.
func bringIntoCatalog(ids types.List[types.UInt64]) {
	if database.OnMissing == nil || database.DB == nil {
		return
	}

	attempts := 0

	for _, id := range ids {
		var one int
		if database.DB.QueryRow(`SELECT 1 FROM datastore.objects WHERE data_id = $1`, id).Scan(&one) == nil {
			continue
		}

		if attempts >= maxOnDemandPerSearch {
			return
		}

		attempts++

		database.OnMissing(uint64(id))
	}
}

func GetCustomRankingByDataID(err error, packet nex.PacketInterface, callID uint32, param datastore_super_mario_maker_types.DataStoreGetCustomRankingByDataIDParam) (*nex.RMCMessage, *nex.Error) {
	if err != nil {
		globals.Logger.Error(err.Error())
		return nil, nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	bringIntoCatalog(param.DataIDList)

	pRankingResult := datastore_smm_db.GetCustomRankingsByDataIDs(param.ApplicationID, param.DataIDList)
	pResults := make(types.List[types.QResult], 0, len(param.DataIDList))


	for i := range pRankingResult {
		// * This is kind of backwards.
		// * The database pulls this data
		// * by default, so it can be done
		// * in a single query. So instead
		// * of checking if a flag *IS*
		// * set, and conditionally *ADDING*
		// * the fields, we check if a flag
		// * is *NOT* set and conditionally
		// * *REMOVE* the field
		if param.ResultOption&0x1 == 0 {
			pRankingResult[i].MetaInfo.Tags = types.NewList[types.String]()
		}

		if param.ResultOption&0x2 == 0 {
			pRankingResult[i].MetaInfo.Ratings = types.NewList[datastore_types.DataStoreRatingInfoWithSlot]()
		}

		if param.ResultOption&0x4 == 0 {
			pRankingResult[i].MetaInfo.MetaBinary = types.NewQBuffer(nil)
		}

		if param.ResultOption&0x20 == 0 {
			pRankingResult[i].Score = 0
		}

		// * Since all errors are thrown away in
		// * datastore_smm_db.GetCustomRankingsByDataIDs
		// * assume all objects returned were a success
		pResults = append(pResults, types.NewQResultSuccess(nex.ResultCodes.Core.Unknown))
	}

	rmcResponseStream := nex.NewByteStreamOut(globals.SecureServer.LibraryVersions, globals.SecureServer.ByteStreamSettings)

	pRankingResult.WriteTo(rmcResponseStream)
	pResults.WriteTo(rmcResponseStream)

	rmcResponse := nex.NewRMCSuccess(globals.SecureEndpoint, rmcResponseStream.Bytes())
	rmcResponse.ProtocolID = datastore_super_mario_maker.ProtocolID
	rmcResponse.MethodID = datastore_super_mario_maker.MethodGetCustomRankingByDataID
	rmcResponse.CallID = callID

	return rmcResponse, nil
}
