package globals

import (
	"github.com/PretendoNetwork/nex-go/v2"
	datastorecommon "github.com/PretendoNetwork/nex-protocols-common-go/v2/datastore"
	"github.com/PretendoNetwork/plogger-go"
	"github.com/minio/minio-go/v7"
)

var Logger *plogger.Logger
var KerberosPassword = "password" // * Default password
var AuthenticationServer *nex.PRUDPServer
var AuthenticationEndpoint *nex.PRUDPEndPoint
var SecureServer *nex.PRUDPServer
var SecureEndpoint *nex.PRUDPEndPoint
var DatastoreCommon *datastorecommon.CommonProtocol
var MinIOClient *minio.Client
var Presigner *S3Presigner
var GRPCAPIKey string
var GRPCServerPort int

// AES key that opens login tokens and HMAC secret that derives per-PID NEX passwords;
// both are shared with the service that issues the tokens.
var NEXTokenAESKey, NEXPasswordSecret []byte
