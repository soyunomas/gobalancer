package routing

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netlink"
	"github.com/tu-usuario/gobalancer/internal/config"
)

// Manager maneja las tablas de rutas y reglas de firewall
type Manager struct {
	Cfg *config.Config
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{Cfg: cfg}
}

// Setup prepara el entorno del Kernel Linux
func (m *Manager) Setup() error {
	log.Println("[KERNEL] Habilitando IPv4 Forwarding...")
	
	// OPT(11): I/O Eficiente. Escribimos directamente en /proc/sys/net/ipv4/ip_forward.
	// Esto es mucho más rápido y seguro que ejecutar un comando externo "sysctl -w ...".
	err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644)
	if err != nil {
		return fmt.Errorf("no se pudo activar ip_forward (¿eres root?): %v", err)
	}

	return nil
}

// EnableNAT configura el Masquerading (Source NAT) para permitir salida a internet
func (m *Manager) EnableNAT() error {
	log.Println("[FIREWALL] Verificando reglas de NAT (Masquerade)...")

	// OPT(12): CGO_ENABLED=0 safe. Usamos librería nativa.
	ipt, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return fmt.Errorf("no se pudo inicializar iptables: %v", err)
	}

	// OPT(4): Pre-asignamos el mapa con capacidad exacta.
	// Usamos map[string]struct{} en lugar de bool porque struct{} ocupa 0 bytes de memoria.
	processedIfaces := make(map[string]struct{}, len(m.Cfg.Interfaces))

	for _, iface := range m.Cfg.Interfaces {
		// Evitar duplicados si hay múltiples WANs en la misma interfaz física
		if _, exists := processedIfaces[iface.IfaceName]; exists {
			continue
		}

		// iptables -t nat -A POSTROUTING -o <interfaz> -j MASQUERADE
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
	
	// OPT(4): Pre-asignar capacidad del slice para evitar re-allocations
	// Esto es crítico en la ruta de "failover" para minimizar latencia.
	activeNexthops := make([]*netlink.NexthopInfo, 0, len(m.Cfg.Interfaces))

	// Lógica de Selección
	if algo == "failover" {
		// --- MODO FAILOVER ---
		for _, iface := range m.Cfg.Interfaces {
			if statusMap[iface.Name] {
				gwIP := net.ParseIP(iface.Gateway)
				// OPT(14): Validación simple
				if gwIP == nil { continue } 

				activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
					Gw:   gwIP,
					Hops: 0,
				})
				// En failover, tomamos la primera activa (según orden config) y salimos
				log.Printf("[ROUTING] Failover: Ruta activa -> %s", iface.Name)
				break 
			}
		}
	} else {
		// --- MODO WEIGHTED ROUND ROBIN ---
		for _, iface := range m.Cfg.Interfaces {
			if statusMap[iface.Name] {
				gwIP := net.ParseIP(iface.Gateway)
				if gwIP == nil { continue }

				// Netlink usa "Hops" como peso relativo.
				// weight 1 en config -> hops 0 en netlink (base)
				w := iface.Weight - 1
				if w < 0 { w = 0 }

				activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
					Gw:   gwIP,
					Hops: w,
				})
			}
		}
	}

	// --- Aplicar al Kernel (Netlink) ---
	
	// OPT(3): Variable en stack. Define 0.0.0.0/0
	dst := &net.IPNet{
		IP:   net.IPv4zero,
		Mask: net.CIDRMask(0, 32),
	}

	// Construimos la ruta
	route := &netlink.Route{
		Dst:       dst,
		MultiPath: activeNexthops,
	}

	// Caso especial: Si solo hay 1 gateway, Netlink prefiere Gw simple en vez de MultiPath
	if len(activeNexthops) == 1 {
		route.MultiPath = nil
		route.Gw = activeNexthops[0].Gw
	} else if len(activeNexthops) == 0 {
		log.Println("🚨 ALERTA CRÍTICA: Todas las WANs están caídas. Sin internet.")
		// No borramos la ruta explícitamente para evitar "Network Unreachable" inmediato en aplicaciones,
		// a veces es mejor dejar que los paquetes mueran por timeout esperando recovery.
		return
	}

	// RouteReplace es atómico: borra la vieja y pone la nueva en una syscall.
	err := netlink.RouteReplace(route)
	if err != nil {
		log.Printf("[ERROR] Fallo crítico actualizando rutas kernel: %v", err)
	} else {
		log.Printf("[ROUTING] Tabla de rutas actualizada. Gateways activos: %d", len(activeNexthops))
	}
}
