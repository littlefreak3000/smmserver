package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/difficulty"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/pool"
	"github.com/PretendoNetwork/super-mario-maker/webui"
)

// webURL is the address of the web page ("" when it is off or could not start).
var webURL string

// startWebUI serves the level viewer and the settings editor (package webui). SMM_WEB_PORT=0 turns it off. It listens on this
// PC only unless SMM_WEB_HOST says otherwise: it has no password and can change the settings file.
func startWebUI() {
	port := getenv("SMM_WEB_PORT", "8081")
	if port == "0" {
		return
	}

	host := getenv("SMM_WEB_HOST", "")

	ln, err := webui.Listen(host, port)
	if err != nil {
		globals.Logger.Warningf("Cannot start the web page on port %s: %s. Change the port with SMM_WEB_PORT (0 turns the page off) in %s",
			port, err.Error(), filepath.Join(dataDirPath, "settings.env"))

		return
	}

	handler := webui.New(webui.Config{
		DB:              database.DB,
		CoursesDir:      coursesDirPath,
		SettingsPath:    filepath.Join(dataDirPath, "settings.env"),
		Template:        settingsTemplate,
		ValidateSetting: validateSetting,
		Info: func() map[string]any {
			return map[string]any{"address": publicHost}
		},
	})

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			globals.Logger.Warningf("The web page stopped: %s", err.Error())
		}
	}()

	shown := "localhost"
	if host != "" && host != "127.0.0.1" && host != "::1" {
		shown = host
		if host == "0.0.0.0" || host == "::" {
			shown = publicHost
		}
	}

	webURL = "http://" + shown + ":" + port

	if host == "" || host == "127.0.0.1" || host == "::1" {
		globals.Logger.Successf("Web page (levels and settings) at %s, on this PC only (SMM_WEB_HOST=0.0.0.0 opens it to your network: it has no password)", webURL)
	} else {
		globals.Logger.Warningf("Web page (levels and settings) at %s, open to your network and WITHOUT a password: anyone who can reach it can change the settings", webURL)
	}
}

// validateSetting judges the settings only the server itself understands, for the web page's settings editor.
func validateSetting(key, value string) error {
	switch key {
	case "SMM_POOL_BANDS":
		_, err := pool.ParseBands(value)
		return err
	case "SMM_DIFFICULTY_CEILING":
		if n, err := strconv.Atoi(value); err != nil || n < 1 || n > 100 {
			return fmt.Errorf("must be a whole number from 1 to 100")
		}
	case "SMM_DIFFICULTY_PRIOR":
		_, err := difficulty.Parse(value)
		return err
	case "SMM_HISTORY_LEVELS":
		if v := strings.ToLower(value); v != "off" {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return fmt.Errorf("must be a number or off")
			}
		}
	case "SMM_PROXY_PORT", "SMM_ACCOUNT_PORT", "SMM_S3_PORT", "SMM_WEB_PORT", "PN_SMM_AUTHENTICATION_SERVER_PORT", "PN_SMM_SECURE_SERVER_PORT":
		if n, err := strconv.Atoi(value); err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("must be a port number (0 to 65535)")
		}
	case "SMM_WEB_HOST":
		if strings.ContainsAny(value, " /") {
			return fmt.Errorf("must be an address such as 0.0.0.0")
		}
	}

	return nil
}
