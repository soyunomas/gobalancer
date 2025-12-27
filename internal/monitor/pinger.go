package monitor

import (
	"context"
	"log"
	"net"
	"strconv"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/tu-usuario/gobalancer/internal/config"
)

type StatusEvent struct {
	InterfaceName string
	IsUp          bool
	Latency       time.Duration
}

type Monitor struct {
	Cfg     *config.Config
	Updates chan StatusEvent
}

func NewMonitor(cfg *config.Config, ch chan StatusEvent) *Monitor {
	return &Monitor{
		Cfg:     cfg,
		Updates: ch,
	}
}

func (m *Monitor) Start(ctx context.Context) {
	// OPT(7): Una goroutine por interfaz es aceptable y escalable para <100 interfaces
	for _, iface := range m.Cfg.Interfaces {
		go m.watchInterface(ctx, iface)
	}
}

func (m *Monitor) watchInterface(ctx context.Context, iface config.InterfaceConfig) {
	// OPT(3, 5): Pre-calculamos el target TCP (String inmutable) FUERA del bucle.
	// Esto evita llamar a net.JoinHostPort() y crear basura en el Heap cada segundo.
	tcpTarget := net.JoinHostPort(iface.MonitorTarget, strconv.Itoa(iface.MonitorPort))
	
	log.Printf("[MONITOR] Iniciando vigilancia en %s -> ICMP:%s, TCP:%s", iface.Name, iface.MonitorTarget, tcpTarget)

	// OPT(4): Pre-configuramos el Dialer una sola vez.
	dialer := &net.Dialer{
		Timeout:   1500 * time.Millisecond,
		// Importante: Forzamos la salida por la IP de la interfaz específica
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(iface.InterfaceIP)},
		KeepAlive: -1, // No necesitamos KeepAlive para un simple handshake check
	}

	// Configurar intervalo
	intervalDuration, err := time.ParseDuration(m.Cfg.General.CheckInterval)
	if err != nil {
		intervalDuration = 2 * time.Second
	}
	ticker := time.NewTicker(intervalDuration)
	defer ticker.Stop()

	// OPT(3): Variables en Stack para evitar escape analysis
	var (
		consecutiveFailures int
		consecutiveSuccesses int
		isCurrentlyUp       bool
		rtt                 time.Duration
		icmpSuccess         bool
		tcpSuccess          bool
	)
	
	limitDown := iface.FailuresToDown
	if limitDown < 1 { limitDown = 3 }
	limitUp := iface.SuccessesToUp
	if limitUp < 1 { limitUp = 3 }

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1. Ping ICMP
			icmpSuccess, rtt = m.checkICMP(iface)

			// 2. TCP Handshake 
			// Usamos el string tcpTarget pre-calculado
			tcpSuccess = m.checkTCP(dialer, tcpTarget)

			// Lógica: Para estar UP, ambos tests deben pasar (ajustable según necesidad)
			isSuccess := icmpSuccess && tcpSuccess
			stateChanged := false

			if isSuccess {
				consecutiveFailures = 0
				consecutiveSuccesses++
				
				// Lógica de histéresis (Up)
				if !isCurrentlyUp && consecutiveSuccesses >= limitUp {
					isCurrentlyUp = true
					stateChanged = true
					log.Printf("[MONITOR] %s RECUPERADO. Latencia: %v", iface.Name, rtt)
				} else if isCurrentlyUp {
					// Si ya está arriba, mantenemos actualizada la latencia para métricas futuras
					// pero no emitimos evento de cambio de estado.
				}
			} else {
				consecutiveSuccesses = 0
				consecutiveFailures++
				
				// Lógica de histéresis (Down)
				if isCurrentlyUp && consecutiveFailures >= limitDown {
					isCurrentlyUp = false
					stateChanged = true
					log.Printf("[MONITOR] %s CAÍDO (Ping: %v, TCP: %v)", iface.Name, icmpSuccess, tcpSuccess)
				}
			}

			if stateChanged {
				m.Updates <- StatusEvent{
					InterfaceName: iface.Name,
					IsUp:          isCurrentlyUp,
					Latency:       rtt,
				}
				// Resetear contadores tras cambio de estado para evitar flapping
				consecutiveFailures = 0
				consecutiveSuccesses = 0
			}
		}
	}
}

func (m *Monitor) checkICMP(iface config.InterfaceConfig) (bool, time.Duration) {
	// OPT(2): probing.NewPinger genera allocs, es inevitable con esta librería.
	// Si quisiéramos optimizar más, usaríamos un socket RAW compartido, pero aumenta complejidad drásticamente.
	pinger, err := probing.NewPinger(iface.MonitorTarget)
	if err != nil { return false, 0 }
	
	if iface.InterfaceIP != "" {
		pinger.Source = iface.InterfaceIP
	}
	
	// OPT(11): SetPrivileged evita syscalls UDP no privilegiadas que a veces fallan en contenedores
	pinger.SetPrivileged(true)
	pinger.Count = 1
	pinger.Timeout = 1500 * time.Millisecond 

	if err := pinger.Run(); err != nil { return false, 0 }
	stats := pinger.Statistics()
	return (stats.PacketsRecv > 0), stats.AvgRtt
}

// OPT(9): Pasamos 'address' string por valor. Al ser inmutable y pequeña, es muy eficiente.
func (m *Monitor) checkTCP(d *net.Dialer, address string) bool {
	conn, err := d.Dial("tcp", address)
	if err != nil {
		return false
	}
	// OPT(10): Defer tiene un coste de nanosegundos. En un monitor de red (ms) es despreciable,
	// pero cerrar explícitamente es técnicamente más rápido.
	conn.Close()
	return true
}
