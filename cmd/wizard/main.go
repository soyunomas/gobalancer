package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
)

// --- Colores y Estilos ---
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorPurple = "\033[35m"
	ColorBold   = "\033[1m"
)

// --- Estructuras de Datos ---

type ConfigInterface struct {
	Name          string
	IfaceName     string
	Gateway       string
	InterfaceIP   string
	Weight        int
	MonitorTarget string
	MonitorPort   int
	// Metadata interna
	IsVPN     bool
	Provider  string // "ZeroTier", "Tailscale", "WireGuard", etc.
	RemoteWan string // Para la guía del servidor
}

type DetectedLink struct {
	Name     string
	IPs      []string
	Gateway  string
	IsVPN    bool
	Provider string
}

// --- MAIN ---

func main() {
	reader := bufio.NewReader(os.Stdin)
	printHeader()

	// 1. Escaneo Inteligente (Sin Sudo para lectura básica, netlink funciona)
	fmt.Println(ColorYellow + "🔍 Analizando interfaces y tabla de enrutamiento..." + ColorReset)
	detectedLinks := scanSystem()

	if len(detectedLinks) == 0 {
		fmt.Println(ColorRed + "❌ No se encontraron interfaces con IP asignada." + ColorReset)
		fmt.Println("   Asegúrate de que tus cables están conectados o tus VPNs iniciadas.")
		return
	}

	fmt.Printf(ColorGreen+"✅ Se han encontrado %d conexiones activas.\n"+ColorReset, len(detectedLinks))
	for _, l := range detectedLinks {
		icon := "🔌"
		desc := "Física"
		if l.IsVPN {
			icon = "🔒"
			desc = l.Provider
		}

		gwStatus := ColorRed + "(Sin Gateway)" + ColorReset
		if l.Gateway != "" {
			gwStatus = ColorBlue + l.Gateway + ColorReset
		}

		// OPT(5): Formato limpio en consola
		fmt.Printf("   %s %-10s [%-10s] IP: %-15s GW: %s\n", icon, l.Name, desc, getFirstIP(l.IPs), gwStatus)
	}
	fmt.Println()

	// 2. Selección de Estrategia
	fmt.Println(ColorBold + "--- ESTRATEGIA DE ENRUTAMIENTO ---" + ColorReset)
	algoChoice := askChoice(reader, "¿Cómo quieres gestionar el tráfico?", []string{
		"Weighted Round Robin (Sumar velocidades - Recomendado)",
		"Failover (Solo respaldo - Ahorro de datos)",
	})
	algorithmVal := "weighted_round_robin"
	if algoChoice == 2 {
		algorithmVal = "failover"
	}

	// 3. Configuración de Interfaces
	var finalConfigs []ConfigInterface

	// Procesamos las detectadas
	for i, link := range detectedLinks {
		fmt.Printf(ColorCyan+"\n--- Configurando #%d: %s (%s) ---"+ColorReset, i+1, link.Name, link.Provider)

		// Si es VPN, sugerimos IP remota
		defaultIP := getFirstIP(link.IPs)

		fmt.Printf("\n   📍 Tu IP Local: %s", defaultIP)
		if link.Gateway != "" {
			fmt.Printf("\n   🌐 Gateway Detectado: %s", link.Gateway)
		} else {
			fmt.Printf("\n   ⚠️  Gateway: No detectado en rutas actuales.")
		}
		fmt.Println("")

		promptUse := fmt.Sprintf("   ¿Usar %s para Internet? (s/n)", link.Name)
		if link.IsVPN {
			promptUse = fmt.Sprintf("   ¿Usar túnel %s (%s) como salida a Internet? (s/n)", link.Provider, link.Name)
		}

		use := askString(reader, promptUse, "s")
		if strings.ToLower(use) != "s" {
			continue
		}

		cfg := configureLink(reader, link, defaultIP, algorithmVal)
		finalConfigs = append(finalConfigs, cfg)
	}

	// Opción manual por si algo escapó al escáner
	fmt.Println(ColorPurple + "\n--- Opciones Avanzadas ---" + ColorReset)
	addMore := askString(reader, "   ¿Deseas añadir una interfaz manualmente? (s/N)", "n")
	if strings.ToLower(addMore) == "s" {
		cName := askString(reader, "   > Nombre de la Interfaz (ej: eth0)", "")
		cIP := askString(reader, "   > Tu IP Local", "")
		cGW := askIP(reader, "   > IP del Gateway", "")

		dummy := DetectedLink{Name: cName, IPs: []string{cIP}, Gateway: cGW}
		finalConfigs = append(finalConfigs, configureLink(reader, dummy, cIP, algorithmVal))
	}

	if len(finalConfigs) == 0 {
		fmt.Println(ColorRed + "❌ No hay interfaces configuradas. Abortando." + ColorReset)
		return
	}

	// 4. Generación de Salida
	generateToml(algorithmVal, finalConfigs)
	generateServerGuide(finalConfigs)
}

// --- Lógica de Configuración ---

func configureLink(r *bufio.Reader, link DetectedLink, defaultIP, algo string) ConfigInterface {
	// Nombre
	defName := fmt.Sprintf("WAN_%s", link.Name)
	if link.IsVPN {
		defName = fmt.Sprintf("%s_Backup", link.Provider)
	}
	name := askString(r, "   > Nombre corto", defName)

	// Gateway
	gw := link.Gateway
	if gw == "" {
		// Si no se detectó gateway, lo pedimos obligatoriamente.
		prompt := "   > Escribe la IP del Gateway (Router)"
		if link.IsVPN {
			prompt = "   > IP del Servidor VPN Remoto (Gateway)"
		}
		gw = askIP(r, prompt, "")
	} else {
		// Permitimos cambiarlo
		gw = askString(r, fmt.Sprintf("   > Gateway [%s]", gw), gw)
	}

	// Peso
	weight := 1
	if algo == "weighted_round_robin" {
		wStr := askString(r, "   > Peso (1-10) [Mayor = Más tráfico]", "1")
		weight, _ = strconv.Atoi(wStr)
	}

	// Monitorización
	target := "8.8.8.8"
	port := 53

	if link.IsVPN {
		// Recomendamos usar targets distintos si es posible, aunque el sistema ahora soporta duplicados.
		target = "1.1.1.1"
		fmt.Println(ColorYellow + "   ℹ️  Detectada VPN: Configura NAT en tu servidor remoto (ver guía al finalizar)." + ColorReset)
	}

	target = askString(r, "   > IP a monitorear (Ping)", target)

	// Puerto
	pStr := askString(r, "   > Puerto TCP Monitor (Enter para 53 DNS)", "53")
	port, _ = strconv.Atoi(pStr)

	// Extra info para VPN (Server Guide)
	remoteWan := ""
	if link.IsVPN {
		remoteWan = askString(r, "   > (Para la Guía) ¿Nombre de la interfaz WAN en el servidor remoto? (ej: eth0)", "eth0")
	}

	return ConfigInterface{
		Name:          name,
		IfaceName:     link.Name,
		Gateway:       gw,
		InterfaceIP:   defaultIP,
		Weight:        weight,
		MonitorTarget: target,
		MonitorPort:   port,
		IsVPN:         link.IsVPN,
		Provider:      link.Provider,
		RemoteWan:     remoteWan,
	}
}

// --- Escáner de Sistema (Netlink) ---

func scanSystem() []DetectedLink {
	// 1. Obtener Enlaces
	links, err := netlink.LinkList()
	if err != nil {
		fmt.Println(ColorRed + "Error leyendo interfaces: " + err.Error() + ColorReset)
		return nil
	}

	// 2. Obtener Rutas (Para buscar gateways)
	routes, _ := netlink.RouteList(nil, netlink.FAMILY_V4)

	var results []DetectedLink

	for _, l := range links {
		attrs := l.Attrs()

		// Filtros: Ignorar Loopback y Down
		if attrs.Flags&net.FlagLoopback != 0 || attrs.Flags&net.FlagUp == 0 {
			continue
		}

		// Obtener IPs
		addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
		if err != nil || len(addrs) == 0 {
			continue
		}
		var ipList []string
		for _, a := range addrs {
			ipList = append(ipList, a.IP.String())
		}

		// Detectar Gateway asociado
		gateway := ""
		for _, r := range routes {
			if r.LinkIndex == attrs.Index {
				// Buscamos ruta por defecto o con gateway explícito
				if r.Gw != nil && !r.Gw.IsUnspecified() {
					if r.Dst == nil {
						gateway = r.Gw.String()
						break
					}
					// Check mask 0.0.0.0/0
					ones, _ := r.Dst.Mask.Size()
					if ones == 0 {
						gateway = r.Gw.String()
						break
					}
				}
			}
		}

		// Identificar Proveedor
		vpn, provider := identifyProvider(attrs.Name)

		results = append(results, DetectedLink{
			Name:     attrs.Name,
			IPs:      ipList,
			Gateway:  gateway,
			IsVPN:    vpn,
			Provider: provider,
		})
	}
	return results
}

func identifyProvider(name string) (bool, string) {
	n := strings.ToLower(name)
	if strings.Contains(n, "zt") {
		return true, "ZeroTier"
	}
	if strings.Contains(n, "tailscale") {
		return true, "Tailscale"
	}
	if strings.Contains(n, "wg") {
		return true, "WireGuard"
	}
	if strings.Contains(n, "tun") || strings.Contains(n, "tap") {
		return true, "OpenVPN/Tunnel"
	}
	if strings.Contains(n, "ppp") {
		return true, "PPPoE"
	}
	return false, "Ethernet/WiFi"
}

func getFirstIP(ips []string) string {
	if len(ips) > 0 {
		parts := strings.Split(ips[0], "/")
		return parts[0]
	}
	return ""
}

// --- Generadores ---

func generateToml(algo string, configs []ConfigInterface) {
	f, _ := os.Create("config.toml")
	defer f.Close()

	// OPT(5): Usamos Fprintf para claridad
	fmt.Fprintf(f, `[general]
check_interval = "2s"
algorithm = "%s"

`, algo)

	for _, c := range configs {
		fmt.Fprintf(f, `[[interfaces]]
name = "%s"
iface_name = "%s"
gateway = "%s"
interface_ip = "%s"
weight = %d
monitor_target = "%s"
monitor_port = %d
failures_to_down = 3
successes_to_up = 3

`, c.Name, c.IfaceName, c.Gateway, c.InterfaceIP, c.Weight, c.MonitorTarget, c.MonitorPort)
	}
	fmt.Println(ColorGreen + "\n✅ Archivo 'config.toml' creado exitosamente." + ColorReset)
}

func generateServerGuide(configs []ConfigInterface) {
	hasVPN := false
	for _, c := range configs {
		if c.IsVPN {
			hasVPN = true
			break
		}
	}
	if !hasVPN {
		return
	}

	f, _ := os.Create("SERVER_SETUP_GUIDE.md")
	defer f.Close()

	f.WriteString("# 📘 Configuración del Servidor Remoto (Exit Node)\n\n")
	f.WriteString("Has configurado interfaces VPN. Para que funcionen como Internet Gateway, **debes ejecutar esto en los servidores remotos (VPS)**:\n")

	for _, c := range configs {
		if !c.IsVPN {
			continue
		}
		f.WriteString(fmt.Sprintf("\n## 🌍 VPN: %s (%s)\n", c.Name, c.Provider))
		f.WriteString("Accede por SSH a tu servidor remoto y ejecuta:\n```bash\n")
		f.WriteString("echo 1 | sudo tee /proc/sys/net/ipv4/ip_forward\n")
		f.WriteString(fmt.Sprintf("WAN=\"%s\"   # Interfaz de internet del servidor (ej: eth0)\n", c.RemoteWan))
		f.WriteString(fmt.Sprintf("VPN=\"%s\"   # Interfaz VPN en el servidor (ej: zt... o wg0)\n", c.IfaceName))
		f.WriteString("\n# Activar NAT para que el tráfico salga a internet\n")
		f.WriteString("sudo iptables -t nat -I POSTROUTING 1 -o $WAN -j MASQUERADE\n")
		f.WriteString("sudo iptables -I FORWARD 1 -i $VPN -o $WAN -j ACCEPT\n")
		f.WriteString("sudo iptables -I FORWARD 1 -i $WAN -o $VPN -m state --state RELATED,ESTABLISHED -j ACCEPT\n")
		f.WriteString("```\n")
		f.WriteString("\n*Nota: Go-NetBalancer ya ha configurado las rutas locales automáticamente en tu cliente.*\n")
		f.WriteString("\n---\n")
	}
	fmt.Println(ColorPurple + "📘 Se ha generado 'SERVER_SETUP_GUIDE.md' con instrucciones para tu Servidor VPN." + ColorReset)
}

// --- Helpers Input ---

func askString(r *bufio.Reader, q, def string) string {
	msg := fmt.Sprintf("%s [%s]: ", q, def)
	if def == "" {
		msg = q + ": "
	}
	fmt.Print(msg)
	in, _ := r.ReadString('\n')
	in = strings.TrimSpace(in)
	if in == "" {
		return def
	}
	return in
}

func askIP(r *bufio.Reader, q, def string) string {
	for {
		val := askString(r, q, def)
		if net.ParseIP(val) != nil {
			return val
		}
		fmt.Println(ColorRed + "❌ IP inválida." + ColorReset)
	}
}

func askChoice(r *bufio.Reader, q string, opts []string) int {
	fmt.Println(q)
	for i, o := range opts {
		fmt.Printf("   %d) %s\n", i+1, o)
	}
	for {
		fmt.Print("Opción: ")
		in, _ := r.ReadString('\n')
		val, err := strconv.Atoi(strings.TrimSpace(in))
		if err == nil && val >= 1 && val <= len(opts) {
			return val
		}
	}
}

func printHeader() {
	fmt.Print("\033[H\033[2J")
	fmt.Println(ColorBlue + `
  ______       _           _                              
 / _____)     | |         | |                             
| /  ___  ___ | | _   ____| | ____ ____   ____ ____  ____ 
| | (___)/ _ \| || \ / _  | |/ _  |  _ \ / ___) _  )/ ___)
| \____/| |_| | |_) | ( | | ( ( | | | | ( (__( (/ /| |    
 \_____/ \___/|____/ \_||_|_|\_||_|_| |_|\____)____)_|                                                                                  
 
            WIZARD DE CONFIGURACIÓN
` + ColorReset)
}
