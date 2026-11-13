package datastore_smm_db

import (
	"github.com/PretendoNetwork/super-mario-maker/database"
)

// PreviousRankingPeriod returns the newest custom ranking application ID below applicationID that
// shares its leading digit (windowed IDs look like <kind><8 digit period>) and has rows.
func PreviousRankingPeriod(applicationID uint32) (uint32, bool) {
	kind := uint64(applicationID) / 100000000 * 100000000

	var prev *int64
	err := database.DB.QueryRow(`
		SELECT MAX(application_id) FROM datastore.object_custom_rankings
		WHERE application_id < $1 AND application_id >= $2
	`, int64(applicationID), int64(kind)).Scan(&prev)
	if err != nil || prev == nil {
		return 0, false
	}

	return uint32(*prev), true
}
