package datastore_smm_db

import (
	"database/sql"
	"time"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_super_mario_maker_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker/types"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_db "github.com/PretendoNetwork/super-mario-maker/database/datastore"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/lib/pq"
)

// GetCourses returns completed, visible courses joined with the custom ranking applicationID.
// With newest=true courses without a ranking row are kept (score 0) and the order is creation date;
// otherwise the order is the ranking value, best first. order is 1-based from offset.
func GetCourses(applicationID uint32, newest bool, offset, limit int) (types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult], *nex.Error) {
	return queryCourses(applicationID, newest, false, 0, 0, offset, limit)
}

// GetRandomCoursesInScoreRange returns random courses whose value in the custom ranking applicationID is within [minScore, maxScore].
func GetRandomCoursesInScoreRange(applicationID uint32, minScore, maxScore int64, limit int) (types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult], *nex.Error) {
	return queryCourses(applicationID, false, true, minScore, maxScore, 0, limit)
}

func queryCourses(applicationID uint32, newest, random bool, minScore, maxScore int64, offset, limit int) (types.List[datastore_super_mario_maker_types.DataStoreCustomRankingResult], *nex.Error) {
	courses := types.NewList[datastore_super_mario_maker_types.DataStoreCustomRankingResult]()

	join := `JOIN datastore.object_custom_rankings ranking ON object.data_id = ranking.data_id AND ranking.application_id = $1 AND`
	orderBy := `ranking.value DESC, object.data_id DESC`
	// * Ranking type 4 ranks makers (profile objects, data type 1); the other windowed types rank courses
	kindFilter := ``
	switch {
	case applicationID >= 400000000 && applicationID < 500000000:
		kindFilter = ` AND object.data_type = 1`
	case applicationID >= 100000000:
		kindFilter = ` AND object.data_type <> 1`
	}

	args := []any{applicationID, limit, offset}
	if random {
		join += ` ranking.value BETWEEN $4 AND $5 AND`
		orderBy = `RANDOM()`
		args = append(args, minScore, maxScore)
	}
	if newest {
		join = `LEFT JOIN datastore.object_custom_rankings ranking ON object.data_id = ranking.data_id AND ranking.application_id = $1 WHERE`
		orderBy = `object.creation_date DESC, object.data_id DESC`
	}

	rows, err := database.Postgres.Query(`
		SELECT
			object.data_id,
			object.owner,
			object.size,
			object.name,
			object.data_type,
			object.meta_binary,
			object.permission,
			object.permission_recipients,
			object.delete_permission,
			object.delete_permission_recipients,
			object.period,
			object.refer_data_id,
			object.flag,
			object.tags,
			object.creation_date,
			object.update_date,
			COALESCE(ranking.value, 0)
		FROM datastore.objects object
		`+join+`
			object.upload_completed = TRUE AND
			object.deleted = FALSE AND
			object.under_review = FALSE`+kindFilter+`
		ORDER BY `+orderBy+`
		LIMIT $2 OFFSET $3
	`, args...)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nex.NewError(nex.ResultCodes.DataStore.NotFound, "Object not found")
		}

		globals.Logger.Error(err.Error())
		// TODO - Send more specific errors?
		return nil, nex.NewError(nex.ResultCodes.DataStore.Unknown, err.Error())
	}

	defer rows.Close()

	for rows.Next() {
		course := datastore_super_mario_maker_types.NewDataStoreCustomRankingResult()
		course.MetaInfo = datastore_types.NewDataStoreMetaInfo()
		course.MetaInfo.Permission = datastore_types.NewDataStorePermission()
		course.MetaInfo.DelPermission = datastore_types.NewDataStorePermission()
		course.MetaInfo.ExpireTime = types.NewDateTime(0x9C3F3E0000) // * 9999-12-31T00:00:00.000Z. This is what the real server sends
		course.MetaInfo.Ratings = types.NewList[datastore_types.DataStoreRatingInfoWithSlot]()

		var createdDate time.Time
		var updatedDate time.Time
		var tagArray []string

		err := rows.Scan(
			&course.MetaInfo.DataID,
			&course.MetaInfo.OwnerID,
			&course.MetaInfo.Size,
			&course.MetaInfo.Name,
			&course.MetaInfo.DataType,
			&course.MetaInfo.MetaBinary,
			&course.MetaInfo.Permission.Permission,
			pq.Array(&course.MetaInfo.Permission.RecipientIDs),
			&course.MetaInfo.DelPermission.Permission,
			pq.Array(&course.MetaInfo.DelPermission.RecipientIDs),
			&course.MetaInfo.Period,
			&course.MetaInfo.ReferDataID,
			&course.MetaInfo.Flag,
			pq.Array(&tagArray),
			&createdDate,
			&updatedDate,
			&course.Score,
		)
		if err != nil {
			globals.Logger.Error(err.Error())
			continue
		}

		ratings, nexError := datastore_db.GetObjectRatingsWithSlotByDataID(course.MetaInfo.DataID)
		if nexError != nil {
			return nil, nexError
		}

		course.MetaInfo.Tags = make(types.List[types.String], 0, len(tagArray))
		for i := range tagArray {
			course.MetaInfo.Tags = append(course.MetaInfo.Tags, types.String(tagArray[i]))
		}

		course.MetaInfo.Ratings = ratings

		course.MetaInfo.CreatedTime.FromTimestamp(createdDate)
		course.MetaInfo.UpdatedTime.FromTimestamp(updatedDate)
		course.MetaInfo.ReferredTime.FromTimestamp(createdDate) // * This is what the real server does

		course.Order = types.NewUInt32(uint32(offset + len(courses) + 1))
		courses = append(courses, course)
	}

	return courses, nil
}
