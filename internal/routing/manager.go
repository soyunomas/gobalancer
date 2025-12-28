package routing

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	"github.com/tu-usuario/gobalancer/internal/config"
	"github.com/vishvananda/netlink"
)

// Manager maneja las tablas de rutas y reglas de firewall
type Manager struct {
	Cfg *config.Config
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{Cfg: cfg}
}

// Setup prepara el entorno del Kernel Linux e instala rutas estáticas de monitoreo
func (m *Manager) Setup() error {
	log.Println("[KERNEL] Habilitando IPv4 Forwarding...")

	// OPT(11): I/O Eficiente. Escribimos directamente en /proc.
	// Esto es mucho más rápido que ejecutar "sysctl".
	err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644)
	if err != nil {
		return fmt.Errorf("no se pudo activar ip_forward (¿eres root?): %v", err)
	}

	// NUEVO: Configuramos las rutas de monitoreo automáticamente
	// Esto elimina la necesidad de que el usuario lo haga manual con el Wizard.
	if err := m.ConfigureMonitorRoutes(); err != nil {
		log.Printf("[KERNEL] ⚠️ Advertencia configurando rutas de monitor: %v", err)
		// No retornamos error fatal, permitimos que el sistema intente arrancar
	}

	return nil
}

// ConfigureMonitorRoutes crea rutas estáticas /32 para los objetivos de monitoreo.
// Resuelve el problema del "Huevo y la Gallina" en VPNs.
func (m *Manager) ConfigureMonitorRoutes() error {
	log.Println("[ROUTING] Asegurando rutas estáticas para monitores...")

	for _, iface := range m.Cfg.Interfaces {
		// Validaciones básicas
		if iface.Gateway == "" || iface.MonitorTarget == "" {
			continue
		}

		// 1. Parsear IPs
		gwIP := net.ParseIP(iface.Gateway)
		dstIP := net.ParseIP(iface.MonitorTarget)

		if gwIP == nil || dstIP == nil {
			log.Printf("[ROUTING] ⚠️ IP inválida en config para %s (GW: %s, Target: %s)", iface.Name, iface.Gateway, iface.MonitorTarget)
			continue
		}

		// 2. Obtener la interfaz física real (Link)
		link, err := netlink.LinkByName(iface.IfaceName)
		if err != nil {
			log.Printf("[ROUTING] ⚠️ Interfaz %s no encontrada en el sistema: %v", iface.IfaceName, err)
			continue
		}

		// 3. Crear la ruta de host (/32)
		// Equivale a: ip route add <TARGET>/32 via <GATEWAY> dev <IFACE>
		dstNet := &net.IPNet{
			IP:   dstIP,
			Mask: net.CIDRMask(32, 32), // Máscara /32 significa "solo esta IP exacta"
		}

		route := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       dstNet,
			Gw:        gwIP,
			Protocol:  4, // Protocolo estático definido por admin
		}

		// 4. Aplicar al Kernel (RouteReplace es idempotente: crea o actualiza)
		// OPT(11): Syscall directa, sin fork de proceso.
		if err := netlink.RouteReplace(route); err != nil {
			log.Printf("[ROUTING] ❌ Fallo al fijar ruta monitor para %s -> %s: %v", iface.Name, iface.MonitorTarget, err)
		} else {
			log.Printf("[ROUTING] 📌 Ruta fija: %s via %s (%s) OK", iface.MonitorTarget, iface.Gateway, iface.Name)
		}
	}
	return nil
}

// EnableNAT configura el Masquerading (Source NAT) para permitir salida a internet
func (m *Manager) EnableNAT() error {
	log.Println("[FIREWALL] Verificando reglas de NAT (Masquerade)...")

	// OPT(12): CGO_ENABLED=0 safe.
	ipt, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return fmt.Errorf("no se pudo inicializar iptables: %v", err)
	}

	// OPT(4): Pre-asignamos el mapa.
	processedIfaces := make(map[string]struct{}, len(m.Cfg.Interfaces))

	for _, iface := range m.Cfg.Interfaces {
		if _, exists := processedIfaces[iface.IfaceName]; exists {
			continue
		}

		err := ipt.AppendUnique("nat", "POSTROUTING", "-o", iface.IfaceName, "-j", "MASQUERADE")
		if err != nil {
			return fmt.Errorf("error aplicando NAT en %s: %v", iface.IfaceName, err)
		}

		log.Printf("[FIREWALL] NAT activado en interfaz física: %s", iface.IfaceName)
		processedIfaces[iface.IfaceName] = struct{}{}
	}

	log.Println("[FIREWALL] ✅ Reglas NAT verificadas.")
	return nil
}

// UpdateRoutes reconstruye la tabla de rutas basándose en el estado
func (m *Manager) UpdateRoutes(statusMap map[string]bool) {
	algo := strings.ToLower(m.Cfg.General.Algorithm)

	// OPT(4): Pre-asignar capacidad del slice.
	activeNexthops := make([]*netlink.NexthopInfo, 0, len(m.Cfg.Interfaces))

	if algo == "failover" {
		// --- MODO FAILOVER ---
		for _, iface := range m.Cfg.Interfaces {
			if statusMap[iface.Name] {
				gwIP := net.ParseIP(iface.Gateway)
				if gwIP == nil {
					continue
				}

				activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
					Gw:   gwIP,
					Hops: 0,
				})
				// En failover, tomamos la primera activa y salimos
				log.Printf("[ROUTING] Failover: Ruta activa -> %s", iface.Name)
				break
			}
		}
	} else {
		// --- MODO WEIGHTED ROUND ROBIN ---
		for _, iface := range m.Cfg.Interfaces {
			if statusMap[iface.Name] {
				gwIP := net.ParseIP(iface.Gateway)
				if gwIP == nil {
					continue
				}

				// Netlink usa "Hops" como peso relativo en multipath.
				w := iface.Weight - 1
				if w < 0 {
					w = 0
				}

				activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
					Gw:   gwIP,
					Hops: w,
				})
			}
		}
	}

	// --- Aplicar al Kernel (Netlink) ---

	// OPT(3): Variable en stack. Ruta Default 0.0.0.0/0
	dst := &net.IPNet{
		IP:   net.IPv4zero,
		Mask: net.CIDRMask(0, 32),
	}

	route := &netlink.Route{
		Dst:       dst,
		MultiPath: activeNexthops,
	}

	// Caso especial: Si solo hay 1 gateway, Netlink prefiere Gw simple
	if len(activeNexthops) == 1 {
		route.MultiPath = nil
		route.Gw = activeNexthops[0].Gw
	} else if len(activeNexthops) == 0 {
		log.Println("🚨 ALERTA CRÍTICA: Todas las WANs están caídas. Sin internet.")
		return
	}

	// RouteReplace es atómico.
	err := netlink.RouteReplace(route)
	if err != nil {
		log.Printf("[ERROR] Fallo crítico actualizando rutas kernel: %v", err)
	} else {
		log.Printf("[ROUTING] Tabla de rutas actualizada. Gateways activos: %d", len(activeNexthops))
	}
}
