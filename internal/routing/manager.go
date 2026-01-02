package routing

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/rs/zerolog"
	"github.com/tu-usuario/gobalancer/internal/config"
	"github.com/tu-usuario/gobalancer/internal/logger"
	"github.com/tu-usuario/gobalancer/internal/monitor"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	BaseTableID      = 100
	RulePriorityBase = 10000
)

type Manager struct {
	Cfg           *config.Config
	ifaceTableIDs map[string]int
	log           zerolog.Logger
	// Optimización: Cache de la última ruta aplicada para evitar syscalls
	lastRouteKey  string
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		Cfg:           cfg,
		ifaceTableIDs: make(map[string]int),
		log:           logger.Get().With().Str("component", "kernel_routing").Logger(),
	}
}

// ... (Setup, Cleanup, InitDedicatedTables, ApplyUserRules, ConfigureMonitorRoutes, EnableNAT se mantienen IGUAL) ...
// Copia y pega las funciones anteriores aquí, no han cambiado.
// Solo modificamos UpdateRoutes abajo.

func (m *Manager) Setup() error {
	m.log.Info().Msg("Habilitando IPv4 Forwarding")
	err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644)
	if err != nil {
		return fmt.Errorf("no se pudo activar ip_forward: %v", err)
	}

	m.Cleanup()

	if err := m.ConfigureMonitorRoutes(); err != nil {
		m.log.Warn().Err(err).Msg("Error configurando rutas de monitor")
	}

	if err := m.InitDedicatedTables(); err != nil {
		m.log.Error().Err(err).Msg("Error inicializando tablas dedicadas")
	}

	if err := m.ApplyUserRules(); err != nil {
		m.log.Error().Err(err).Msg("Error aplicando reglas PBR")
	}

	return nil
}

func (m *Manager) Cleanup() {
	m.log.Debug().Msg("Limpiando reglas y rutas del Kernel...")

	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err == nil {
		count := 0
		for _, r := range rules {
			if r.Priority >= RulePriorityBase && r.Priority < RulePriorityBase+1000 {
				netlink.RuleDel(&r)
				count++
			}
		}
		if count > 0 {
			m.log.Debug().Int("deleted_rules", count).Msg("Reglas IP eliminadas")
		}
	}

	for i := 0; i < 20; i++ {
		tableID := BaseTableID + i
		filter := &netlink.Route{Table: tableID}
		routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_TABLE)
		if err != nil {
			continue
		}
		for _, route := range routes {
			netlink.RouteDel(&route)
		}
	}
}

func (m *Manager) InitDedicatedTables() error {
	for i, iface := range m.Cfg.Interfaces {
		tableID := BaseTableID + i
		m.ifaceTableIDs[iface.Name] = tableID

		gwIP := net.ParseIP(iface.Gateway)
		if gwIP == nil { continue }

		link, err := netlink.LinkByName(iface.IfaceName)
		if err != nil {
			m.log.Warn().Str("iface", iface.IfaceName).Msg("Interfaz no encontrada en sistema, saltando tabla")
			continue
		}

		dst := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
		route := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       dst,
			Gw:        gwIP,
			Table:     tableID,
		}

		if err := netlink.RouteReplace(route); err != nil {
			m.log.Error().Err(err).Int("table", tableID).Msg("Fallo creando ruta default en tabla")
		}
	}
	return nil
}

func (m *Manager) ApplyUserRules() error {
	if len(m.Cfg.Rules) == 0 {
		return nil
	}
	m.log.Info().Int("count", len(m.Cfg.Rules)).Msg("Aplicando reglas de Policy Routing")

	priority := RulePriorityBase

	for _, rule := range m.Cfg.Rules {
		targetTable, ok := m.ifaceTableIDs[rule.TargetInterface]
		if !ok { 
			m.log.Warn().Str("rule", rule.Name).Msg("Interfaz destino desconocida, saltando regla")
			continue 
		}

		r := netlink.NewRule()
		r.Table = targetTable
		r.Priority = priority
		priority++

		var ipProto int
		switch strings.ToLower(rule.Protocol) {
		case "udp": ipProto = unix.IPPROTO_UDP
		case "icmp": ipProto = unix.IPPROTO_ICMP
		case "tcp": ipProto = unix.IPPROTO_TCP
		default: ipProto = unix.IPPROTO_TCP
		}

		switch strings.ToLower(rule.Type) {
		case "port", "dport":
			port, err := strconv.Atoi(rule.Value)
			if err != nil { continue }
			r.Dport = netlink.NewRulePortRange(uint16(port), uint16(port))
			r.IPProto = ipProto
		case "dst_ip", "ip":
			ip := net.ParseIP(rule.Value)
			if ip != nil {
				r.Dst = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
				if rule.Protocol != "tcp" { r.IPProto = ipProto }
			}
		case "src_ip":
			ip := net.ParseIP(rule.Value)
			if ip != nil {
				r.Src = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
			}
		}

		if err := netlink.RuleAdd(r); err != nil {
			if !strings.Contains(err.Error(), "file exists") {
				m.log.Error().Err(err).Str("rule", rule.Name).Msg("Error kernel añadiendo regla")
			}
		}
	}
	return nil
}

func (m *Manager) ConfigureMonitorRoutes() error {
	for _, iface := range m.Cfg.Interfaces {
		if iface.Gateway == "" || iface.MonitorTarget == "" { continue }
		gwIP := net.ParseIP(iface.Gateway)
		dstIP := net.ParseIP(iface.MonitorTarget)
		if gwIP == nil || dstIP == nil { continue }

		link, err := netlink.LinkByName(iface.IfaceName)
		if err != nil { continue }

		dstNet := &net.IPNet{IP: dstIP, Mask: net.CIDRMask(32, 32)}
		route := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       dstNet,
			Gw:        gwIP,
			Protocol:  4, 
		}

		netlink.RouteReplace(route)
	}
	return nil
}

func (m *Manager) EnableNAT() error {
	ipt, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil {
		return fmt.Errorf("iptables init: %v", err)
	}

	processed := make(map[string]bool)
	for _, iface := range m.Cfg.Interfaces {
		if processed[iface.IfaceName] { continue }

		exists, err := ipt.Exists("nat", "POSTROUTING", "-o", iface.IfaceName, "-j", "MASQUERADE")
		if err != nil { return err }
		if !exists {
			m.log.Info().Str("iface", iface.IfaceName).Msg("Añadiendo regla MASQUERADE (NAT)")
			if err := ipt.Append("nat", "POSTROUTING", "-o", iface.IfaceName, "-j", "MASQUERADE"); err != nil {
				return fmt.Errorf("NAT error %s: %v", iface.IfaceName, err)
			}
		}
		processed[iface.IfaceName] = true
	}
	return nil
}

// UpdateRoutes ahora es inteligente e idempotente
func (m *Manager) UpdateRoutes(statusMap map[string]*monitor.InterfaceState) {
	algo := strings.ToLower(m.Cfg.General.Algorithm)
	activeNexthops := make([]*netlink.NexthopInfo, 0, len(m.Cfg.Interfaces))

	type candidate struct {
		gwIP      net.IP
		hopWeight int
		name      string
		gwStr     string // Para key generation
	}
	candidates := make([]candidate, 0, len(m.Cfg.Interfaces))

	// 1. Filtrado de candidatos
	for _, iface := range m.Cfg.Interfaces {
		state, ok := statusMap[iface.Name]
		if !ok || !state.IsUp { continue }

		if iface.MaxLatency != "" {
			maxLat, err := time.ParseDuration(iface.MaxLatency)
			if err == nil && maxLat > 0 && state.Latency > maxLat {
				m.log.Trace().Str("iface", iface.Name).Dur("lat", state.Latency).Msg("SLA violación")
				continue
			}
		}

		gwIP := net.ParseIP(iface.Gateway)
		if gwIP == nil { continue }

		w := iface.Weight - 1
		if w < 0 { w = 0 }

		candidates = append(candidates, candidate{
			gwIP:      gwIP,
			hopWeight: w,
			name:      iface.Name,
			gwStr:     iface.Gateway,
		})
	}

	// 2. Selección según algoritmo
	currentKeyBuilder := strings.Builder{}
	currentKeyBuilder.WriteString(algo)
	currentKeyBuilder.WriteString(":")

	if algo == "failover" {
		if len(candidates) > 0 {
			// En failover, solo usamos el primero (asumiendo orden de config)
			c := candidates[0]
			activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
				Gw: c.gwIP, Hops: 0,
			})
			currentKeyBuilder.WriteString(c.gwStr)
		}
	} else {
		// En WRR usamos todos. Ordenamos para que la key sea determinista
		// (aunque el orden de nexthops en kernel no importa tanto, el hash sí)
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].name < candidates[j].name
		})

		for _, c := range candidates {
			activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
				Gw: c.gwIP, Hops: c.hopWeight,
			})
			// Generamos firma única: "192.168.1.1(10)|10.0.0.1(5)"
			currentKeyBuilder.WriteString(fmt.Sprintf("%s(%d)|", c.gwStr, c.hopWeight))
		}
	}

	newKey := currentKeyBuilder.String()

	// 3. IDEMPOTENCIA: Si la key es igual a la anterior, NO HACEMOS NADA
	if newKey == m.lastRouteKey {
		// No logging, "silencio absoluto" si nada cambió
		return
	}

	// 4. Aplicación al Kernel (Solo si cambió)
	dst := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	route := &netlink.Route{Dst: dst, MultiPath: activeNexthops}

	if len(activeNexthops) == 1 {
		route.MultiPath = nil
		route.Gw = activeNexthops[0].Gw
	} else if len(activeNexthops) == 0 {
		m.log.Warn().Msg("⚠️  NO HAY RUTAS ACTIVAS (Blackout)")
		m.lastRouteKey = "blackout" // Forzamos actualización cuando vuelva internet
		return
	}

	m.log.Info().Str("key", newKey).Int("nexthops", len(activeNexthops)).Msg("🔥 CAMBIO DETECTADO: Actualizando Tabla de Rutas Kernel")

	if err := netlink.RouteReplace(route); err != nil {
		m.log.Error().Err(err).Msg("Fallo crítico actualizando rutas")
		// No actualizamos lastRouteKey para reintentar en el siguiente ciclo
	} else {
		m.lastRouteKey = newKey
	}
}
