package nex_datastore_super_mario_maker

import (
	"strconv"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_super_mario_maker "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker"
	datastore_super_mario_maker_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker/types"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	datastore_smm_db "github.com/PretendoNetwork/super-mario-maker/database/datastore/super-mario-maker"
	"github.com/PretendoNetwork/super-mario-maker/globals"
)

// maxListLength caps one page; the client asks for 25-ish at a time.
const maxListLength = 100

func writeCourseList(callID uint32, methodID uint32, list types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult]) *nex.RMCMessage {
	stream := nex.NewByteStreamOut(globals.SecureServer.LibraryVersions, globals.SecureServer.ByteStreamSettings)
	list.WriteTo(stream)

	rmc := nex.NewRMCSuccess(globals.SecureEndpoint, stream.Bytes())
	rmc.ProtocolID = datastore_super_mario_maker.ProtocolID
	rmc.MethodID = methodID
	rmc.CallID = callID

	return rmc
}

func clampRange(offset, length uint32) (int, int) {
	if length == 0 || length > maxListLength {
		length = maxListLength
	}

	return int(offset), int(length)
}

// LatestCourseSearchObject is the "newest" course list of Course World.
func LatestCourseSearchObject(err error, packet nex.PacketInterface, callID uint32, param datastore_types.DataStoreSearchParam, extraData types.List[types.String]) (*nex.RMCMessage, *nex.Error) {
	if err != nil {
		globals.Logger.Error(err.Error())
		return nil, nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	globals.Logger.Infof("LatestCourseSearchObject range=%d+%d extra=%v", param.ResultRange.Offset, param.ResultRange.Length, extraData)

	offset, length := clampRange(uint32(param.ResultRange.Offset), uint32(param.ResultRange.Length))
	list, nexError := datastore_smm_db.GetCourses(0, true, offset, length)
	if nexError != nil {
		return nil, nexError
	}

	return writeCourseList(callID, datastore_super_mario_maker.MethodLatestCourseSearchObject, list), nil
}

// GetCustomRanking lists courses by a custom ranking (stars, plays, ...) selected by ApplicationID.
func GetCustomRanking(err error, packet nex.PacketInterface, callID uint32, param datastore_super_mario_maker_types.DataStoreGetCustomRankingParam) (*nex.RMCMessage, *nex.Error) {
	if err != nil {
		globals.Logger.Error(err.Error())
		return nil, nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	globals.Logger.Infof("GetCustomRanking app=%d range=%d+%d option=%#x", param.ApplicationID, param.ResultRange.Offset, param.ResultRange.Length, param.ResultOption)

	offset, length := clampRange(uint32(param.ResultRange.Offset), uint32(param.ResultRange.Length))
	appID := uint32(param.ApplicationID)
	list, nexError := datastore_smm_db.GetCourses(appID, false, offset, length)
	if nexError != nil {
		return nil, nexError
	}

	// * Windowed rankings (one application ID per period) are empty until somebody stars a course
	// * in the current period, and the client shows an error for an empty list. Fall back to the
	// * latest earlier period of the same ranking that has data.
	if len(list) == 0 && appID >= 100000000 {
		if prev, ok := datastore_smm_db.PreviousRankingPeriod(appID); ok {
			globals.Logger.Infof("GetCustomRanking app=%d empty, falling back to %d", appID, prev)
			list, nexError = datastore_smm_db.GetCourses(prev, false, offset, length)
			if nexError != nil {
				return nil, nexError
			}
		}
	}

	// * ResultOption bits mirror GetCustomRankingByDataID: tags, ratings, meta binary
	for i := range list {
		if param.ResultOption&0x1 == 0 {
			list[i].MetaInfo.Tags = types.NewList[types.String]()
		}

		if param.ResultOption&0x2 == 0 {
			list[i].MetaInfo.Ratings = types.NewList[datastore_types.DataStoreRatingInfoWithSlot]()
		}

		if param.ResultOption&0x4 == 0 {
			list[i].MetaInfo.MetaBinary = types.NewQBuffer(nil)
		}
	}

	// * Like GetCustomRankingByDataID, the client expects a list of QResults after the rankings
	{
		stream := nex.NewByteStreamOut(globals.SecureServer.LibraryVersions, globals.SecureServer.ByteStreamSettings)
		list.WriteTo(stream)

		results := make(types.List[types.QResult], 0, len(list))
		for range list {
			results = append(results, types.NewQResultSuccess(nex.ResultCodes.Core.Unknown))
		}
		results.WriteTo(stream)

		rmc := nex.NewRMCSuccess(globals.SecureEndpoint, stream.Bytes())
		rmc.ProtocolID = datastore_super_mario_maker.ProtocolID
		rmc.MethodID = datastore_super_mario_maker.MethodGetCustomRanking
		rmc.CallID = callID

		return rmc, nil
	}

}

// ScoreRangeCascadedSearchObject is called by the 100 Mario Challenge with
// extraData = [rankingApplicationID, minScore, maxScore, flag] (seen: "300000000" "1" "49" "0").
// It returns random courses whose score in that custom ranking is within [min, max]; when the
// range is empty it widens to any course so the challenge can still start.
func ScoreRangeCascadedSearchObject(err error, packet nex.PacketInterface, callID uint32, param datastore_types.DataStoreSearchParam, extraData types.List[types.String]) (*nex.RMCMessage, *nex.Error) {
	if err != nil {
		globals.Logger.Error(err.Error())
		return nil, nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	globals.Logger.Infof("ScoreRangeCascadedSearchObject range=%d+%d extra=%v", param.ResultRange.Offset, param.ResultRange.Length, extraData)

	_, length := clampRange(0, uint32(param.ResultRange.Length))

	var list types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult]
	if len(extraData) >= 3 {
		app, errApp := strconv.ParseUint(string(extraData[0]), 10, 32)
		minScore, errMin := strconv.ParseInt(string(extraData[1]), 10, 64)
		maxScore, errMax := strconv.ParseInt(string(extraData[2]), 10, 64)
		if errApp == nil && errMin == nil && errMax == nil {
			var nexError *nex.Error
			list, nexError = datastore_smm_db.GetRandomCoursesInScoreRange(uint32(app), minScore, maxScore, length)
			if nexError != nil {
				return nil, nexError
			}
		}
	}

	if len(list) == 0 {
		var nexError *nex.Error
		list, nexError = datastore_smm_db.GetRandomCoursesWithLimit(length)
		if nexError != nil {
			return nil, nexError
		}
	}

	return writeCourseList(callID, datastore_super_mario_maker.MethodScoreRangeCascadedSearchObject, list), nil
}

// BestScoreRateCourseSearchObject backs the star-ranked stage list of Course World (v272 client).
// extraData is logged so the difficulty filter can be decoded; for now it is the star ranking.
func BestScoreRateCourseSearchObject(err error, packet nex.PacketInterface, callID uint32, param datastore_types.DataStoreSearchParam, extraData types.List[types.String]) (*nex.RMCMessage, *nex.Error) {
	if err != nil {
		globals.Logger.Error(err.Error())
		return nil, nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	globals.Logger.Infof("BestScoreRateCourseSearchObject range=%d+%d extra=%v", param.ResultRange.Offset, param.ResultRange.Length, extraData)

	offset, length := clampRange(uint32(param.ResultRange.Offset), uint32(param.ResultRange.Length))
	list, nexError := datastore_smm_db.GetCourses(0, false, offset, length)
	if nexError != nil {
		return nil, nexError
	}

	return writeCourseList(callID, datastore_super_mario_maker.MethodBestScoreRateCourseSearchObject, list), nil
}
