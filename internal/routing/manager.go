package routing

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/tu-usuario/gobalancer/internal/config"
	"github.com/tu-usuario/gobalancer/internal/monitor"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	BaseTableID = 100
	RulePriorityBase = 10000
)

type Manager struct {
	Cfg           *config.Config
	ifaceTableIDs map[string]int
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		Cfg:           cfg,
		ifaceTableIDs: make(map[string]int),
	}
}

func (m *Manager) Setup() error {
	log.Println("[KERNEL] Habilitando IPv4 Forwarding...")
	err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644)
	if err != nil {
		return fmt.Errorf("no se pudo activar ip_forward: %v", err)
	}

	m.Cleanup()

	if err := m.ConfigureMonitorRoutes(); err != nil {
		log.Printf("[KERNEL] ⚠️ Advertencia rutas monitor: %v", err)
	}

	if err := m.InitDedicatedTables(); err != nil {
		log.Printf("[KERNEL] ⚠️ Error inicializando tablas dedicadas: %v", err)
	}

	if err := m.ApplyUserRules(); err != nil {
		log.Printf("[KERNEL] ⚠️ Error aplicando reglas PBR: %v", err)
	}

	return nil
}

func (m *Manager) Cleanup() {
	log.Println("[CLEANUP] 🧹 Iniciando limpieza de reglas y rutas del Kernel...")

	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err == nil {
		for _, r := range rules {
			if r.Priority >= RulePriorityBase && r.Priority < RulePriorityBase+1000 {
				netlink.RuleDel(&r)
			}
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
	log.Println("[CLEANUP] ✅ Limpieza completada.")
}

func (m *Manager) InitDedicatedTables() error {
	log.Println("[PBR] Inicializando tablas de ruta dedicadas por interfaz...")

	for i, iface := range m.Cfg.Interfaces {
		tableID := BaseTableID + i
		m.ifaceTableIDs[iface.Name] = tableID

		gwIP := net.ParseIP(iface.Gateway)
		if gwIP == nil { continue }

		link, err := netlink.LinkByName(iface.IfaceName)
		if err != nil {
			log.Printf("⚠️ Interfaz %s no encontrada, saltando tabla dedicada.", iface.IfaceName)
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
			log.Printf("❌ Error creando tabla %d para %s: %v", tableID, iface.Name, err)
		}
	}
	return nil
}

func (m *Manager) ApplyUserRules() error {
	if len(m.Cfg.Rules) == 0 {
		return nil
	}
	log.Printf("[PBR] Aplicando %d reglas de tráfico...", len(m.Cfg.Rules))

	priority := RulePriorityBase

	for _, rule := range m.Cfg.Rules {
		targetTable, ok := m.ifaceTableIDs[rule.TargetInterface]
		if !ok { continue }

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
				log.Printf("❌ Error añadiendo regla '%s': %v", rule.Name, err)
			}
		}
	}
	return nil
}

func (m *Manager) ConfigureMonitorRoutes() error {
	log.Println("[ROUTING] Asegurando rutas estáticas para monitores...")

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
	log.Println("[FIREWALL] Verificando reglas de NAT (Masquerade)...")
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
			if err := ipt.Append("nat", "POSTROUTING", "-o", iface.IfaceName, "-j", "MASQUERADE"); err != nil {
				return fmt.Errorf("NAT error %s: %v", iface.IfaceName, err)
			}
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
	}
	candidates := make([]candidate, 0, len(m.Cfg.Interfaces))

	for _, iface := range m.Cfg.Interfaces {
		state, ok := statusMap[iface.Name]
		if !ok || !state.IsUp { continue }

		if iface.MaxLatency != "" {
			maxLat, err := time.ParseDuration(iface.MaxLatency)
			if err == nil && maxLat > 0 && state.Latency > maxLat { continue }
		}

		gwIP := net.ParseIP(iface.Gateway)
		if gwIP == nil { continue }

		w := iface.Weight - 1
		if w < 0 { w = 0 }

		candidates = append(candidates, candidate{gwIP: gwIP, hopWeight: w, name: iface.Name})
	}

	if algo == "failover" {
		if len(candidates) > 0 {
			activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
				Gw: candidates[0].gwIP, Hops: 0,
			})
		}
	} else {
		for _, c := range candidates {
			activeNexthops = append(activeNexthops, &netlink.NexthopInfo{
				Gw: c.gwIP, Hops: c.hopWeight,
			})
		}
	}

	dst := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	route := &netlink.Route{Dst: dst, MultiPath: activeNexthops}

	if len(activeNexthops) == 1 {
		route.MultiPath = nil
		route.Gw = activeNexthops[0].Gw
	} else if len(activeNexthops) == 0 {
		return // No routes
	}

	if err := netlink.RouteReplace(route); err != nil {
		log.Printf("[ERROR] UpdateRoutes: %v", err)
	}
}
