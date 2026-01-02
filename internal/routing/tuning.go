package routing

import (
	"os"
	"strings"
)

// KernelParams define los valores sysctl óptimos para un Router Multi-WAN
var optimalSysctls = map[string]string{
	// --- ENRUTAMIENTO MULTIPATH (CRÍTICO) ---
	// 1 = Layer 4 Hashing (IP+Port).
	// Garantiza que un flujo TCP/UDP se mantenga en la misma interfaz.
	// Evita que HTTPS (Bancos, Gmail) cierren sesión por cambio de IP.
	"net.ipv4.fib_multipath_hash_policy": "1",

	// --- OPTIMIZACIÓN TCP (BBR) ---
	// BBR (Bottleneck Bandwidth and RTT) es superior en redes mixtas (4G/Fibra)
	// Requiere Kernel 4.9+. Si falla, no pasa nada (se queda en cubic).
	"net.core.default_qdisc":          "fq",
	"net.ipv4.tcp_congestion_control": "bbr",

	// --- GESTIÓN DE CONEXIONES ---
	// Permitir reusar sockets en TIME_WAIT para conexiones salientes nuevas.
	// Reduce el agotamiento de puertos en NAT intensivo.
	"net.ipv4.tcp_tw_reuse": "1",

	// Protección contra ataques SYN Flood básicos
	"net.ipv4.tcp_syncookies": "1",

	// Rango de puertos efímeros ampliado para NAT
	"net.ipv4.ip_local_port_range": "1024 65535",

	// Aumentar cola de backlog para ráfagas de conexiones
	"net.core.somaxconn": "4096",
}

// ApplyTuning aplica configuraciones de sysctl para convertir el host en un router robusto.
func (m *Manager) ApplyTuning() error {
	m.log.Info().Msg("🔧 Aplicando Kernel Tuning (Sysctl Hardening)")

	for key, value := range optimalSysctls {
		path := "/proc/sys/" + strings.ReplaceAll(key, ".", "/")

		// Verificamos si el archivo existe (ej: BBR puede no estar disponible en kernels viejos)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			m.log.Debug().Str("param", key).Msg("Parámetro no soportado por este Kernel (ignorado)")
			continue
		}

		// Escribimos el valor
		if err := os.WriteFile(path, []byte(value+"\n"), 0644); err != nil {
			// Logueamos como warn, no error fatal, para no detener el arranque
			m.log.Warn().Err(err).Str("param", key).Msg("No se pudo aplicar tuning")
		} else {
			m.log.Debug().Str("param", key).Str("val", value).Msg("Kernel param aplicado")
		}
	}

	return nil
}
