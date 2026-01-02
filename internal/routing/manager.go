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
	runtimeGateways map[string]net.IP
	log             zerolog.Logger
	lastRouteKey    string
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		Cfg:             cfg,
		ifaceTableIDs:   make(map[string]int),
		runtimeGateways: make(map[string]net.IP),
		log:             logger.Get().With().Str("component", "kernel_routing").Logger(),
	}
}

func (m *Manager) GetResolvedGateways() map[string]net.IP {
	copyMap := make(map[string]net.IP)
	for k, v := range m.runtimeGateways {
		copyMap[k] = v
	}
	return copyMap
}

// ResetInterface restaura TODAS las rutas críticas de una interfaz tras un evento UP.
// El Kernel borra rutas al caer la interfaz, por lo que debemos recrearlas.
func (m *Manager) ResetInterface(ifaceName string) {
	m.log.Info().Str("iface", ifaceName).Msg("♻️ Rehidratando configuración de interfaz tras recuperación")
	
	// Limpiamos cache para forzar resolución fresca
	delete(m.runtimeGateways, ifaceName)

	var targetIface config.InterfaceConfig
	found := false
	idx := 0
	for i, iface := range m.Cfg.Interfaces {
		if iface.Name == ifaceName {
			targetIface = iface
			idx = i
			found = true
			break
		}
	}
	if !found { return }

	// Intentamos resolver Gateway
	gwIP, err := m.resolveGatewayIP(targetIface)
	if err != nil {
		// Si falla (DHCP lento), el loop principal reintentará en UpdateRoutes, pero
		// no podemos configurar rutas estáticas aún.
		m.log.Warn().Str("iface", ifaceName).Err(err).Msg("No se pudo redescubrir el Gateway (posible latencia DHCP). Se reintentará en el loop.")
		return 
	}
	m.runtimeGateways[ifaceName] = gwIP

	// Obtenemos el Link index físico
	link, err := netlink.LinkByName(targetIface.IfaceName)
	if err != nil {
		m.log.Error().Err(err).Msg("Interfaz física no encontrada al resetear")
		return
	}
	linkIndex := link.Attrs().Index

	// 1. Restaurar Tabla Dedicada (Policy Routing)
	tableID := BaseTableID + idx
	dst := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	route := &netlink.Route{
		LinkIndex: linkIndex,
		Dst:       dst,
		Gw:        gwIP,
		Table:     tableID,
	}

	if err := netlink.RouteReplace(route); err != nil {
		m.log.Error().Err(err).Int("table", tableID).Msg("Fallo restaurando ruta en tabla dedicada")
	} else {
		m.log.Debug().Int("table", tableID).Msg("Tabla dedicada restaurada correctamente")
	}

	// 2. [FIX] Restaurar Ruta de Monitoreo (Host Route)
	// Vital para que el Pinger mida la latencia de ESTA interfaz y no salga por la default.
	if err := m.setMonitorRoute(targetIface, gwIP, linkIndex); err != nil {
		m.log.Error().Err(err).Msg("Fallo restaurando ruta de monitor")
	} else {
		m.log.Debug().Str("target", targetIface.MonitorTarget).Msg("Ruta de monitor restaurada")
	}
	
	// CRÍTICO: Forzamos regeneración de multipath en la siguiente llamada a UpdateRoutes
	m.lastRouteKey = "" 
}

func (m *Manager) Setup() error {
	m.log.Info().Msg("Habilitando IPv4 Forwarding")
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644)

	if err := m.ApplyTuning(); err != nil {
		m.log.Warn().Err(err).Msg("Hubo advertencias aplicando Kernel Tuning")
	}

	m.Cleanup()
	m.refreshGateways()

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
	m.log.Debug().Msg("🧹 Iniciando limpieza profunda del Kernel...")

	// 1. Monitor Routes
	for _, iface := range m.Cfg.Interfaces {
		if iface.MonitorTarget == "" { continue }
		dstIP := net.ParseIP(iface.MonitorTarget)
		if dstIP == nil { continue }

		filter := &netlink.Route{
			Dst:   &net.IPNet{IP: dstIP, Mask: net.CIDRMask(32, 32)},
			Table: unix.RT_TABLE_MAIN,
		}
		routes, _ := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_DST|netlink.RT_FILTER_TABLE)
		for _, r := range routes {
			if r.Protocol == 4 {
				netlink.RouteDel(&r)
			}
		}
	}

	// 2. Multipath Default Route
	filter := &netlink.Route{
		Table: unix.RT_TABLE_MAIN,
		Dst:   nil, 
	}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_TABLE)
	if err == nil {
		for _, r := range routes {
			if len(r.MultiPath) > 0 {
				netlink.RouteDel(&r)
				m.log.Info().Msg("Ruta Multipath eliminada")
			}
		}
	}

	// 3. IP Rules
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err == nil {
		for _, r := range rules {
			if r.Priority >= RulePriorityBase && r.Priority < RulePriorityBase+1000 {
				netlink.RuleDel(&r)
			}
		}
	}

	// 4. Dedicated Tables
	for i := 0; i < 20; i++ {
		tableID := BaseTableID + i
		filter := &netlink.Route{Table: tableID}
		dtRoutes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_TABLE)
		if err == nil {
			for _, route := range dtRoutes {
				netlink.RouteDel(&route)
			}
		}
	}
}

func (m *Manager) refreshGateways() {
	for _, iface := range m.Cfg.Interfaces {
		gwIP, err := m.resolveGatewayIP(iface)
		if err != nil {
			m.log.Debug().Str("iface", iface.Name).Err(err).Msg("Gateway pendiente de resolución")
			continue
		}
		m.runtimeGateways[iface.Name] = gwIP
	}
}

func (m *Manager) resolveGatewayIP(iface config.InterfaceConfig) (net.IP, error) {
	// 1. IP Estática en Config
	if iface.Gateway != "" && iface.Gateway != "auto" {
		ip := net.ParseIP(iface.Gateway)
		if ip != nil { return ip, nil }
	}

	// 2. Auto-Detección (Netlink)
	link, err := netlink.LinkByName(iface.IfaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s no encontrada", iface.IfaceName)
	}

	filter := &netlink.Route{
		Table: unix.RT_TABLE_MAIN,
	}

	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, err
	}

	for _, r := range routes {
		if r.LinkIndex != link.Attrs().Index {
			continue
		}
		// Check Default Gateway (Dst == nil or /0)
		isDefault := r.Dst == nil
		if !isDefault {
			ones, _ := r.Dst.Mask.Size()
			isDefault = (ones == 0)
		}

		if isDefault && r.Gw != nil && !r.Gw.IsUnspecified() {
			return r.Gw, nil
		}
	}

	return nil, fmt.Errorf("gateway 'auto' no encontrado en tabla MAIN para %s", iface.IfaceName)
}

func (m *Manager) InitDedicatedTables() error {
	for i, iface := range m.Cfg.Interfaces {
		tableID := BaseTableID + i
		m.ifaceTableIDs[iface.Name] = tableID

		gwIP, ok := m.runtimeGateways[iface.Name]
		if !ok {
			var err error
			gwIP, err = m.resolveGatewayIP(iface)
			if err != nil { continue }
			m.runtimeGateways[iface.Name] = gwIP
		}

		link, err := netlink.LinkByName(iface.IfaceName)
		if err != nil { continue }

		dst := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
		route := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       dst,
			Gw:        gwIP,
			Table:     tableID,
		}
		netlink.RouteReplace(route)
	}
	return nil
}

func (m *Manager) ApplyUserRules() error {
	if len(m.Cfg.Rules) == 0 { return nil }
	
	priority := RulePriorityBase
	for _, rule := range m.Cfg.Rules {
		targetTable, ok := m.ifaceTableIDs[rule.TargetInterface]
		if !ok { 
			m.log.Warn().Str("target", rule.TargetInterface).Msg("Regla PBR ignorada: Interfaz destino no válida")
			continue 
		}

		r := netlink.NewRule()
		r.Table = targetTable
		r.Priority = priority
		priority++

		// 1. Determinar Protocolo (Por defecto TCP si no se especifica)
		var ipProto int
		switch strings.ToLower(rule.Protocol) {
		case "udp": 
			ipProto = unix.IPPROTO_UDP
		case "icmp": 
			ipProto = unix.IPPROTO_ICMP
		case "tcp":
			ipProto = unix.IPPROTO_TCP
		default: 
			// Si el usuario pone algo raro o vacío, asumimos TCP o IP general?
			// Tu config loader pone "tcp" por defecto, así que esto es seguro.
			ipProto = unix.IPPROTO_TCP
		}

		// 2. Construir la Regla según Tipo
		switch strings.ToLower(rule.Type) {
		case "port", "dport":
			port, _ := strconv.Atoi(rule.Value)
			r.Dport = netlink.NewRulePortRange(uint16(port), uint16(port))
			r.IPProto = ipProto // dport REQUIERE protocolo (TCP/UDP)

		case "dst_ip", "ip":
			ip := net.ParseIP(rule.Value)
			if ip != nil {
				r.Dst = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
				// FIX: Asignar siempre el protocolo. 
				// Si quieres que afecte a todo (TCP+UDP+ICMP), el usuario debería 
				// no especificar protocolo, pero tu struct obliga a uno.
				// Asumiendo que si config dice "tcp", queremos SOLO tcp:
				r.IPProto = ipProto 
			}

		case "src_ip":
			ip := net.ParseIP(rule.Value)
			if ip != nil {
				r.Src = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
				// Src IP puede filtrar por protocolo opcionalmente
				r.IPProto = ipProto
			}
		}
		
		if err := netlink.RuleAdd(r); err != nil {
			// Si la regla ya existe (ej: restart rápido), no es error fatal
			if !strings.Contains(err.Error(), "file exists") {
				m.log.Error().Err(err).Str("rule", rule.Name).Msg("Error aplicando regla PBR")
			}
		} else {
			m.log.Debug().Str("rule", rule.Name).Int("table", targetTable).Msg("Regla PBR aplicada")
		}
	}
	return nil
}

func (m *Manager) ConfigureMonitorRoutes() error {
	for _, iface := range m.Cfg.Interfaces {
		if iface.MonitorTarget == "" { continue }
		gwIP, ok := m.runtimeGateways[iface.Name]
		if !ok { continue }

		link, err := netlink.LinkByName(iface.IfaceName)
		if err != nil { continue }

		if err := m.setMonitorRoute(iface, gwIP, link.Attrs().Index); err != nil {
			m.log.Warn().Err(err).Str("iface", iface.Name).Msg("No se pudo configurar ruta monitor inicial")
		}
	}
	return nil
}

// setMonitorRoute helper para configurar la ruta estática /32 hacia el target de monitoreo
func (m *Manager) setMonitorRoute(iface config.InterfaceConfig, gwIP net.IP, linkIndex int) error {
	if iface.MonitorTarget == "" { return nil }
	
	dstIP := net.ParseIP(iface.MonitorTarget)
	if dstIP == nil { return fmt.Errorf("IP objetivo inválida: %s", iface.MonitorTarget) }

	dstNet := &net.IPNet{IP: dstIP, Mask: net.CIDRMask(32, 32)}
	
	route := &netlink.Route{
		LinkIndex: linkIndex,
		Dst:       dstNet,
		Gw:        gwIP,
		Protocol:  4, // Protocolo definido por el usuario (static) en Netlink
	}
	
	return netlink.RouteReplace(route)
}

func (m *Manager) EnableNAT() error {
	ipt, err := iptables.NewWithProtocol(iptables.ProtocolIPv4)
	if err != nil { return err }

	processed := make(map[string]bool)
	for _, iface := range m.Cfg.Interfaces {
		if processed[iface.IfaceName] { continue }
		exists, _ := ipt.Exists("nat", "POSTROUTING", "-o", iface.IfaceName, "-j", "MASQUERADE")
		if !exists {
			ipt.Append("nat", "POSTROUTING", "-o", iface.IfaceName, "-j", "MASQUERADE")
		}
		processed[iface.IfaceName] = true
	}
	return nil
}

func (m *Manager) UpdateRoutes(statusMap map[string]*monitor.InterfaceState) {
	algo := strings.ToLower(m.Cfg.General.Algorithm)
	activeNexthops := make([]*netlink.NexthopInfo, 0, len(m.Cfg.Interfaces))

	type candidate struct {
		gwIP      net.IP
		hopWeight int
		name      string
		gwStr     string
	}
	candidates := make([]candidate, 0, len(m.Cfg.Interfaces))

	for _, iface := range m.Cfg.Interfaces {
		state, ok := statusMap[iface.Name]
		// Debe estar UP según el monitor (TCP Success)
		if !ok || !state.IsUp { continue }

		if iface.MaxLatency != "" {
			maxLat, err := time.ParseDuration(iface.MaxLatency)
			if err == nil && maxLat > 0 && state.Latency > maxLat { continue }
		}

		gwIP, ok := m.runtimeGateways[iface.Name]
		if !ok {
			var err error
			gwIP, err = m.resolveGatewayIP(iface)
			if err != nil { 
				// Silenciamos log en producción si es frecuente, pero útil para debug
				// m.log.Debug().Str("iface", iface.Name).Err(err).Msg("Skipping: Gateway not resolved yet")
				continue 
			}
			m.runtimeGateways[iface.Name] = gwIP
		}

		w := iface.Weight
		if w < 1 { w = 1 }

		candidates = append(candidates, candidate{
			gwIP:      gwIP,
			hopWeight: w,
			name:      iface.Name,
			gwStr:     gwIP.String(),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].name < candidates[j].name
	})

	keyBuilder := strings.Builder{}
	keyBuilder.WriteString(algo)
	keyBuilder.WriteString(":")

	if algo == "failover" {
		if len(candidates) > 0 {
			c := candidates[0] 
			activeNexthops = append(activeNexthops, &netlink.NexthopInfo{Gw: c.gwIP})
			keyBuilder.WriteString(c.gwStr)
		}
	} else {
		for _, c := range candidates {
			activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
				Gw:   c.gwIP,
				Hops: c.hopWeight - 1,
			})
			keyBuilder.WriteString(fmt.Sprintf("%s(%d)|", c.gwStr, c.hopWeight))
		}
	}

	newKey := keyBuilder.String()
	// Verificación de idempotencia
	if newKey == m.lastRouteKey { return }

	dst := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	route := &netlink.Route{Dst: dst, MultiPath: activeNexthops}

	if len(activeNexthops) == 1 {
		route.MultiPath = nil
		route.Gw = activeNexthops[0].Gw
	} else if len(activeNexthops) == 0 {
		m.log.Warn().Msg("⚠️  BLACKOUT: No hay rutas disponibles")
		m.lastRouteKey = "blackout"
		return
	}

	m.log.Info().Str("key", newKey).Int("nexthops", len(activeNexthops)).Msg("🔥 Rutas actualizadas en Kernel")

	if err := netlink.RouteReplace(route); err != nil {
		m.log.Error().Err(err).Msg("Fallo crítico escribiendo rutas")
		m.lastRouteKey = ""
	} else {
		m.lastRouteKey = newKey
	}
}
