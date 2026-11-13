package nex_datastore_super_mario_maker

import (
	"strconv"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_super_mario_maker_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker/types"
	datastore_smm_db "github.com/PretendoNetwork/super-mario-maker/database/datastore/super-mario-maker"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/selection"
)

// Selector decides which levels the course lists contain (whole-archive index, challenge seeds). nil keeps the
// original behavior: plain SQL queries over the catalog.
var Selector *selection.Selector

// NNIDForPID maps a player's PID to their NNID, so a per-player challenge seed can be applied. Optional.
var NNIDForPID func(pid uint32) string

// EnsureFetched makes sure a level's file is in the catalog, downloading it if needed (index mode). It must not
// block for long. Optional.
var EnsureFetched func(dataID uint64) bool

// Settle gives the levels of a list a moment to finish downloading before the list is sent (index mode, optional;
// see SMM_LIST_WAIT). Levels that are not done in time are listed as they are and finish in the background.
var Settle func(ids []int64)

// EnsureRecord makes sure the archived world record / first clear of a level is in the catalog before the game asks
// for it (nil = nothing to look up). Set by the home server when fetching is on.
var EnsureRecord func(dataID uint64)

// Played is told when the game opens a level (a course, not a profile), so random lists offer other levels first
// (nil = nothing to tell). Set by the home server.
var Played func(dataID uint64)

// requester returns the NNID of the player who made the call ("" when unknown).
func requester(packet nex.PacketInterface) string {
	if NNIDForPID == nil || packet == nil || packet.Sender() == nil {
		return ""
	}

	return NNIDForPID(uint32(packet.Sender().PID()))
}

// selectedList returns the list the Selector picked for q, rendered with scores from ranking scoreApp.
// ok is false when the caller should run its normal query.
func selectedList(q selection.Query, scoreApp uint32, firstOrder int) (types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult], bool) {
	if Selector == nil {
		return nil, false
	}

	ids, handled, err := Selector.IDs(q)
	if err != nil {
		globals.Logger.Errorf("Selecting levels failed, using the catalog instead: %v", err)
		return nil, false
	}

	if !handled || len(ids) == 0 {
		return nil, false
	}

	if Settle != nil {
		Settle(ids)
	}

	list := datastore_smm_db.GetCoursesByIDs(scoreApp, ids, firstOrder)
	if len(list) == 0 {
		return nil, false
	}

	return list, true
}

// recommendedRange reads the difficulty filter of RecommendedCourseSearchObject as a clear-rate range in percent.
// The client sends ["1", min, max, "0", "0"] with min and max as a FAILURE rate (Easy 0-34 ... Super Expert 96-100),
// or empty strings for "All".
func recommendedRange(extra types.List[types.String]) (int64, int64) {
	if len(extra) >= 3 && string(extra[0]) == "1" {
		minFail, errMin := strconv.ParseInt(string(extra[1]), 10, 64)
		maxFail, errMax := strconv.ParseInt(string(extra[2]), 10, 64)

		if errMin == nil && errMax == nil {
			return selection.FailureToClear(minFail, maxFail)
		}
	}

	return 0, 100
}
