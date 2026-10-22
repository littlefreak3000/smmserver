package globals

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
)

// PasswordFromPID derives the NEX password as HMAC-SHA256(secret, pid as 8 bytes LE),
// so the token service and this server agree without storing credentials.
func PasswordFromPID(pid types.PID) (string, uint32) {
	if len(NEXPasswordSecret) < 32 {
		return "", nex.ResultCodes.RendezVous.InvalidUsername
	}

	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(pid))
	mac := hmac.New(sha256.New, NEXPasswordSecret)
	_, _ = mac.Write(b)

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), 0
}
