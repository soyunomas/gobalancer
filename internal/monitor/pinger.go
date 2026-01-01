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

// StatusEvent es el evento puntual que emite el monitor
type StatusEvent struct {
	InterfaceName string
	IsUp          bool
	Latency       time.Duration
}

// InterfaceState representa el estado acumulado usado por el Router
type InterfaceState struct {
	IsUp       bool
	Latency    time.Duration
	LastUpdate time.Time
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
	for _, iface := range m.Cfg.Interfaces {
		go m.watchInterface(ctx, iface)
	}
}

func (m *Monitor) watchInterface(ctx context.Context, iface config.InterfaceConfig) {
	// Pre-cálculos para evitar allocs en loop
	tcpTarget := net.JoinHostPort(iface.MonitorTarget, strconv.Itoa(iface.MonitorPort))
	
	dialer := &net.Dialer{
		Timeout:   1500 * time.Millisecond,
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(iface.InterfaceIP)}, // Si es vacía, el Kernel decide (auto-detect)
		KeepAlive: -1, 
	}

	intervalDuration, err := time.ParseDuration(m.Cfg.General.CheckInterval)
	if err != nil {
		intervalDuration = 2 * time.Second
	}
	ticker := time.NewTicker(intervalDuration)
	defer ticker.Stop()

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
			tcpSuccess = m.checkTCP(dialer, tcpTarget)

			isSuccess := icmpSuccess && tcpSuccess
			stateChanged := false
			shouldEmit := false

			if isSuccess {
				consecutiveFailures = 0
				consecutiveSuccesses++
				
				if !isCurrentlyUp && consecutiveSuccesses >= limitUp {
					isCurrentlyUp = true
					stateChanged = true
					log.Printf("[MONITOR] %s RECUPERADO. Latencia: %v", iface.Name, rtt)
				}
				if isCurrentlyUp {
					shouldEmit = true
				}

			} else {
				consecutiveSuccesses = 0
				consecutiveFailures++
				
				if isCurrentlyUp && consecutiveFailures >= limitDown {
					isCurrentlyUp = false
					stateChanged = true
					log.Printf("[MONITOR] %s CAÍDO (Ping:%v, TCP:%v)", iface.Name, icmpSuccess, tcpSuccess)
				}
			}

			if stateChanged || shouldEmit {
				m.Updates <- StatusEvent{
					InterfaceName: iface.Name,
					IsUp:          isCurrentlyUp,
					Latency:       rtt,
				}
				
				if stateChanged {
					consecutiveFailures = 0
					consecutiveSuccesses = 0
				}
			}
		}
	}
}

func (m *Monitor) checkICMP(iface config.InterfaceConfig) (bool, time.Duration) {
	pinger, err := probing.NewPinger(iface.MonitorTarget)
	if err != nil { return false, 0 }
	
	// Si InterfaceIP está vacío, NO forzamos Source. 
	// Dejamos que el routing del kernel (que ya configuramos en ConfigureMonitorRoutes) haga el trabajo.
	if iface.InterfaceIP != "" {
		pinger.Source = iface.InterfaceIP
	}
	
	pinger.SetPrivileged(true)
	pinger.Count = 1
	pinger.Timeout = 1500 * time.Millisecond 

	if err := pinger.Run(); err != nil { return false, 0 }
	stats := pinger.Statistics()
	return (stats.PacketsRecv > 0), stats.AvgRtt
}

func (m *Monitor) checkTCP(d *net.Dialer, address string) bool {
	conn, err := d.Dial("tcp", address)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
