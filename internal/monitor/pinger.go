package monitor

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/rs/zerolog"
	"github.com/tu-usuario/gobalancer/internal/config"
	"github.com/tu-usuario/gobalancer/internal/logger"
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
	log     zerolog.Logger
}

func NewMonitor(cfg *config.Config, ch chan StatusEvent) *Monitor {
	return &Monitor{
		Cfg:     cfg,
		Updates: ch,
		log:     logger.Get().With().Str("component", "monitor").Logger(),
	}
}

func (m *Monitor) Start(ctx context.Context) {
	for _, iface := range m.Cfg.Interfaces {
		m.log.Info().Str("iface", iface.Name).Msg("Iniciando monitor de interfaz")
		go m.watchInterface(ctx, iface)
	}
}

func (m *Monitor) watchInterface(ctx context.Context, iface config.InterfaceConfig) {
	// --- FASE 1: Pre-cálculo (Zero Allocation en Loop) ---
	// Calculamos strings y durations una sola vez fuera del loop caliente
	tcpTarget := net.JoinHostPort(iface.MonitorTarget, strconv.Itoa(iface.MonitorPort))
	
	intervalDuration, err := time.ParseDuration(m.Cfg.General.CheckInterval)
	if err != nil {
		intervalDuration = 2 * time.Second
	}
	
	// Timeout ajustado: No debe superar el intervalo para evitar solapamiento
	checkTimeout := 1500 * time.Millisecond
	if checkTimeout > intervalDuration {
		checkTimeout = intervalDuration - (100 * time.Millisecond)
	}

	dialer := &net.Dialer{
		Timeout:   checkTimeout,
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(iface.InterfaceIP)},
		KeepAlive: -1,
	}

	ticker := time.NewTicker(intervalDuration)
	defer ticker.Stop()

	// Contadores de estado
	limitDown := iface.FailuresToDown
	if limitDown < 1 { limitDown = 3 }
	limitUp := iface.SuccessesToUp
	if limitUp < 1 { limitUp = 3 }

	var (
		consecutiveFailures  int
		consecutiveSuccesses int
		isCurrentlyUp        bool
	)

	// Logger contextualizado para esta goroutine (evita allocs de key/value repetidos)
	monLog := m.log.With().Str("iface", iface.Name).Logger()

	for {
		select {
		case <-ctx.Done():
			monLog.Debug().Msg("Deteniendo monitor")
			return
		case <-ticker.C:
			// --- FASE 2: Ejecución Paralela (Latency Reduction) ---
			var (
				icmpSuccess bool
				rtt         time.Duration
				tcpSuccess  bool
				wg          sync.WaitGroup
			)

			wg.Add(2)

			// Check 1: ICMP (Goroutine dedicada)
			go func() {
				defer wg.Done()
				icmpSuccess, rtt = m.checkICMP(iface, checkTimeout)
			}()

			// Check 2: TCP (Goroutine dedicada)
			go func() {
				defer wg.Done()
				tcpSuccess = m.checkTCP(dialer, tcpTarget)
			}()

			wg.Wait() // Esperamos el máx(ICMP, TCP) en lugar de la suma

			// --- FASE 3: Lógica de Decisión (State Machine) ---
			isSuccess := icmpSuccess && tcpSuccess
			stateChanged := false
			shouldEmit := false

			if isSuccess {
				consecutiveFailures = 0
				consecutiveSuccesses++
				
				if !isCurrentlyUp && consecutiveSuccesses >= limitUp {
					isCurrentlyUp = true
					stateChanged = true
					monLog.Info().Dur("latency", rtt).Msg("Interfaz RECUPERADA (UP)")
				}
				if isCurrentlyUp {
					shouldEmit = true
				}
			} else {
				consecutiveSuccesses = 0
				consecutiveFailures++
				
				// Solo logueamos debug de fallos si no estamos caídos todavía, para diagnosis
				if isCurrentlyUp {
					monLog.Debug().
						Bool("icmp", icmpSuccess).
						Bool("tcp", tcpSuccess).
						Int("fails", consecutiveFailures).
						Msg("Fallo detectado en ciclo")
				}
				
				if isCurrentlyUp && consecutiveFailures >= limitDown {
					isCurrentlyUp = false
					stateChanged = true
					monLog.Warn().
						Bool("icmp", icmpSuccess).
						Bool("tcp", tcpSuccess).
						Msg("Interfaz CAÍDA (DOWN)")
				}
			}

			if stateChanged || shouldEmit {
				select {
				case m.Updates <- StatusEvent{
					InterfaceName: iface.Name,
					IsUp:          isCurrentlyUp,
					Latency:       rtt,
				}:
				default:
					monLog.Warn().Msg("Canal de eventos lleno, descartando actualización")
				}

				if stateChanged {
					consecutiveFailures = 0
					consecutiveSuccesses = 0
				}
			}
		}
	}
}

func (m *Monitor) checkICMP(iface config.InterfaceConfig, timeout time.Duration) (bool, time.Duration) {
	pinger, err := probing.NewPinger(iface.MonitorTarget)
	if err != nil {
		return false, 0
	}
	
	if iface.InterfaceIP != "" {
		pinger.Source = iface.InterfaceIP
	}
	
	pinger.SetPrivileged(true)
	pinger.Count = 1
	pinger.Timeout = timeout

	if err := pinger.Run(); err != nil {
		return false, 0
	}
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
