package routing

import (
	"context"
	"log"
	"net"
	"os/exec"
	"time"
)

// FlushConntrack elimina las conexiones "zombis" usando la herramienta nativa del kernel.
// Requiere tener instalado 'conntrack' (apt install conntrack).
func (m *Manager) FlushConntrack(ifaceIP string) {
	if ifaceIP == "" {
		return
	}
	// Validación de seguridad para evitar inyección de comandos
	if net.ParseIP(ifaceIP) == nil {
		log.Printf("[CONNTRACK] ❌ IP inválida para flush: %s", ifaceIP)
		return
	}

	log.Printf("[CONNTRACK] 🧹 Purgando conexiones para IP muerta: %s", ifaceIP)

	// Ejecutamos con timeout por seguridad (no queremos bloquear goroutines)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Borrar flujos donde la IP es ORIGEN (Salida)
	// conntrack -D -s <IP>
	errSrc := exec.CommandContext(ctx, "conntrack", "-D", "-s", ifaceIP).Run()

	// 2. Borrar flujos donde la IP es DESTINO (Entrada/Retorno)
	// conntrack -D -d <IP>
	errDst := exec.CommandContext(ctx, "conntrack", "-D", "-d", ifaceIP).Run()

	// Análisis de resultados
	if errSrc == nil && errDst == nil {
		log.Printf("[CONNTRACK] ✅ Flujos purgados exitosamente.")
	} else {
		// Es normal que falle si no había flujos que borrar (exit status 1)
		// Solo avisamos si parece que falta el comando
		if isCommandNotFound(errSrc) || isCommandNotFound(errDst) {
			log.Println("[CONNTRACK] ⚠️  Comando 'conntrack' no encontrado.")
			log.Println("   👉 Instálalo para failover instantáneo: sudo apt install conntrack")
		}
	}
}

func isCommandNotFound(err error) bool {
	if err == nil {
		return false
	}
	// Go devuelve error si el binario no existe en el PATH
	return err.Error() == "file does not exist" || err.Error() == "executable file not found in $PATH"
}
