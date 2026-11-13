package globals

import "os"

var trace = os.Getenv("SMM_TRACE") == "1"

// Tracef logs a diagnostic line, but only when the server runs with SMM_TRACE=1. It is for finding out what the game asks
// the server (every request, every profile lookup), which fills the log in a normal session.
func Tracef(format string, a ...any) {
	if trace && Logger != nil {
		Logger.Infof(format, a...)
	}
}
