package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Setup configura el logger global.
// Si debug es true, el nivel baja a DEBUG y la salida es "ConsoleWriter" (lento pero legible).
// Si debug es false, nivel INFO y salida JSON (rápido para producción/systemd).
func Setup(debug bool) {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
		// Por defecto Zerolog escribe JSON a Stderr, perfecto para Journald/Docker
	}
}

// Get devuelve el puntero al logger global para evitar copias y permitir métodos pointer receiver.
func Get() *zerolog.Logger {
	return &log.Logger
}


