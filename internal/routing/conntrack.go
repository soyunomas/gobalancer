package routing

import (
	"context"
	"net"
	"os/exec"
	"time"

	"github.com/tu-usuario/gobalancer/internal/logger"
)

// FlushConntrack elimina las conexiones "zombis" usando la herramienta nativa del kernel.
func (m *Manager) FlushConntrack(ifaceIP string) {
	log := logger.Get().With().Str("component", "conntrack").Logger()

	if ifaceIP == "" {
		return
	}
	if net.ParseIP(ifaceIP) == nil {
		log.Error().Str("ip", ifaceIP).Msg("IP inválida para flush")
		return
	}

	log.Info().Str("ip", ifaceIP).Msg("Purgando conexiones muertas (Conntrack Flush)")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Borrar flujos Origen
	errSrc := exec.CommandContext(ctx, "conntrack", "-D", "-s", ifaceIP).Run()

	// 2. Borrar flujos Destino
	errDst := exec.CommandContext(ctx, "conntrack", "-D", "-d", ifaceIP).Run()

	if errSrc == nil && errDst == nil {
		log.Debug().Msg("Flujos purgados exitosamente")
	} else {
		// Logueamos solo como debug/warn porque si no hay flujos devuelve error 1
		log.Debug().Err(errSrc).Msg("Resultado purga SRC (puede ser error si tabla vacía)")
	}
}
