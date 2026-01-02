package logger

import (
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Setup configura el logger global.
func Setup(debug bool) {
	if debug {
		// MODO DEBUG (Desarrollo)
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		
		// 1. Cambiamos el formato interno a String legible (RFC3339) para que 'i' sea texto
		zerolog.TimeFieldFormat = time.RFC3339

		output := zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: time.RFC3339,
		}

		// 2. Sobrescribimos el formateador de fecha.
		// Al hacer esto, eliminamos el color gris oscuro por defecto de zerolog.
		// Simplemente devolvemos el string tal cual (usará el color de tu terminal).
		output.FormatTimestamp = func(i interface{}) string {
			return fmt.Sprintf("%s", i)
		}

		log.Logger = log.Output(output)

	} else {
		// MODO PRODUCCIÓN (Systemd/Docker)
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
		
		// Usamos UnixMs (números) porque es mucho más rápido de generar para la CPU
		// y los sistemas de logs (Splunk, ELK, CloudWatch) lo parsean mejor.
		zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
		
		// Log.logger por defecto escribe JSON a Stderr
	}
}

// Get devuelve el puntero al logger global
func Get() *zerolog.Logger {
	return &log.Logger
}
