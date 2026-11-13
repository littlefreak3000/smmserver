package main

import (
	"encoding/hex"
	"crypto/rand"
	"os"
	"strconv"
	"strings"

	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/joho/godotenv"

	"github.com/PretendoNetwork/nex-go/v2"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func init() {
	globals.Logger = plogger.NewLogger()

	var err error

	err = godotenv.Load()
	if err != nil {
		globals.Logger.Warning("Error loading .env file")
	}

	s3Endpoint := os.Getenv("PN_SMM_CONFIG_S3_ENDPOINT")
	s3AccessKey := os.Getenv("PN_SMM_CONFIG_S3_ACCESS_KEY")
	s3AccessSecret := os.Getenv("PN_SMM_CONFIG_S3_ACCESS_SECRET")
	s3SecureEnv := os.Getenv("PN_SMM_CONFIG_S3_SECURE")

	postgresURI := os.Getenv("PN_SMM_POSTGRES_URI")
	authenticationServerPort := os.Getenv("PN_SMM_AUTHENTICATION_SERVER_PORT")
	secureServerHost := os.Getenv("PN_SMM_SECURE_SERVER_HOST")
	secureServerPort := os.Getenv("PN_SMM_SECURE_SERVER_PORT")
	gRPCServerPort := os.Getenv("PN_SMM_GRPC_SERVER_PORT")
	gRPCAPIKey := os.Getenv("PN_SMM_GRPC_API_KEY")

	if strings.TrimSpace(postgresURI) == "" {
		globals.Logger.Error("PN_SMM_POSTGRES_URI environment variable not set")
		os.Exit(0)
	}

	kerberosPassword := make([]byte, 0x10)
	_, err = rand.Read(kerberosPassword)
	if err != nil {
		globals.Logger.Error("Error generating Kerberos password")
		os.Exit(0)
	}

	globals.KerberosPassword = string(kerberosPassword)

	globals.AuthenticationServerAccount = nex.NewAccount(1, "Quazal Authentication", globals.KerberosPassword)
	globals.SecureServerAccount = nex.NewAccount(2, "Quazal Rendez-Vous", globals.KerberosPassword)

	if strings.TrimSpace(authenticationServerPort) == "" {
		globals.Logger.Error("PN_SMM_AUTHENTICATION_SERVER_PORT environment variable not set")
		os.Exit(0)
	}

	if port, err := strconv.Atoi(authenticationServerPort); err != nil {
		globals.Logger.Errorf("PN_SMM_AUTHENTICATION_SERVER_PORT is not a valid port. Expected 0-65535, got %s", authenticationServerPort)
		os.Exit(0)
	} else if port < 0 || port > 65535 {
		globals.Logger.Errorf("PN_SMM_AUTHENTICATION_SERVER_PORT is not a valid port. Expected 0-65535, got %s", authenticationServerPort)
		os.Exit(0)
	}

	if strings.TrimSpace(secureServerHost) == "" {
		globals.Logger.Error("PN_SMM_SECURE_SERVER_HOST environment variable not set")
		os.Exit(0)
	}

	if strings.TrimSpace(secureServerPort) == "" {
		globals.Logger.Error("PN_SMM_SECURE_SERVER_PORT environment variable not set")
		os.Exit(0)
	}

	if port, err := strconv.Atoi(secureServerPort); err != nil {
		globals.Logger.Errorf("PN_SMM_SECURE_SERVER_PORT is not a valid port. Expected 0-65535, got %s", secureServerPort)
		os.Exit(0)
	} else if port < 0 || port > 65535 {
		globals.Logger.Errorf("PN_SMM_SECURE_SERVER_PORT is not a valid port. Expected 0-65535, got %s", secureServerPort)
		os.Exit(0)
	}

	// The course-deletion gRPC server is optional; leave PN_SMM_GRPC_SERVER_PORT empty to disable it.
	if strings.TrimSpace(gRPCServerPort) != "" {
		gRPCPort, err := strconv.Atoi(gRPCServerPort)
		if err != nil || gRPCPort < 1 || gRPCPort > 65535 {
			globals.Logger.Errorf("PN_SMM_GRPC_SERVER_PORT is not a valid port, got %s", gRPCServerPort)
			os.Exit(0)
		}
		globals.GRPCServerPort = gRPCPort
	}

	if strings.TrimSpace(gRPCAPIKey) == "" {
		globals.Logger.Warning("Insecure gRPC server detected. PN_SMM_GRPC_API_KEY environment variable not set")
	}
	globals.GRPCAPIKey = gRPCAPIKey

	globals.NEXTokenAESKey, err = hex.DecodeString(os.Getenv("PN_NEX_TOKEN_AES_KEY"))
	if err != nil || len(globals.NEXTokenAESKey) != 32 {
		globals.Logger.Error("PN_NEX_TOKEN_AES_KEY must be a 64-character hex AES-256 key")
		os.Exit(0)
	}

	globals.NEXPasswordSecret, err = hex.DecodeString(os.Getenv("PN_NEX_PASSWORD_SECRET"))
	if err != nil || len(globals.NEXPasswordSecret) < 32 {
		globals.Logger.Error("PN_NEX_PASSWORD_SECRET must be at least 32 bytes encoded as hex")
		os.Exit(0)
	}

	staticCredentials := credentials.NewStaticV4(s3AccessKey, s3AccessSecret, "")

	s3Secure, err := strconv.ParseBool(s3SecureEnv)
	if err != nil {
		globals.Logger.Warningf("PN_SMM_CONFIG_S3_SECURE environment variable not set. Using default value: %t", true)
		s3Secure = true
	}

	minIOClient, err := minio.New(s3Endpoint, &minio.Options{
		Creds:  staticCredentials,
		Secure: s3Secure,
	})
	if err != nil {
		panic(err)
	}

	globals.MinIOClient = minIOClient
	globals.Presigner = globals.NewS3Presigner(globals.MinIOClient)

	// * Connect to and setup databases
	database.ConnectPostgres()
}
