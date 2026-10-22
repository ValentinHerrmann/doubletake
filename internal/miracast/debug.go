package miracast

import (
	"log"

	"doubletake/internal/airplay"
)

// dbg logs only in -debug mode, which airplay.SetDebugMode controls for every
// protocol.
func dbg(format string, args ...interface{}) {
	if airplay.DebugMode() {
		log.Printf(format, args...)
	}
}
