package monitor

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"syscall"
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
	// Preparación de constantes
	tcpTarget := net.JoinHostPort(iface.MonitorTarget, strconv.Itoa(iface.MonitorPort))
	
	intervalDuration, err := time.ParseDuration(m.Cfg.General.CheckInterval)
	if err != nil {
		intervalDuration = 2 * time.Second
	}
	
	checkTimeout := 1500 * time.Millisecond
	if checkTimeout > intervalDuration {
		checkTimeout = intervalDuration - (100 * time.Millisecond)
	}

	ticker := time.NewTicker(intervalDuration)
	defer ticker.Stop()

	limitDown := iface.FailuresToDown
	if limitDown < 1 { limitDown = 3 }
	limitUp := iface.SuccessesToUp
	if limitUp < 1 { limitUp = 3 }

	var (
		consecutiveFailures  int
		consecutiveSuccesses int
		isCurrentlyUp        bool
	)

	monLog := m.log.With().Str("iface", iface.Name).Logger()

	for {
		select {
		case <-ctx.Done():
			monLog.Debug().Msg("Deteniendo monitor")
			return
		case <-ticker.C:
			// 1. AUTO-DISCOVERY DE IP
			currentIP, ipErr := getInterfaceIP(iface.IfaceName)
			
			// Si el usuario forzó una IP en config, verificamos que la interfaz exista
			if iface.InterfaceIP != "" {
				currentIP = iface.InterfaceIP
				if _, err := net.InterfaceByName(iface.IfaceName); err != nil {
					ipErr = err
				} else {
					// Si la interfaz existe, reseteamos el error aunque getInterfaceIP haya fallado
					// (caso raro de IP estática configurada manual pero no detectada por netlink)
					ipErr = nil
				}
			}

			if ipErr != nil {
				if isCurrentlyUp {
					monLog.Warn().Err(ipErr).Msg("Fallo Físico: Interfaz perdida o sin IP")
				}
				m.handleStateChange(false, 0, &consecutiveSuccesses, &consecutiveFailures, limitUp, limitDown, &isCurrentlyUp, iface.Name, monLog)
				continue
			}

			// --- FASE 2: Ejecución Paralela ---
			var (
				icmpSuccess bool
				rtt         time.Duration
				tcpSuccess  bool
				wg          sync.WaitGroup
			)

			wg.Add(2)

			// Check 1: ICMP (Depende de Tabla de Rutas)
			go func() {
				defer wg.Done()
				icmpSuccess, rtt = m.checkICMP(iface.MonitorTarget, currentIP, checkTimeout)
			}()

			// Check 2: TCP (Bypass de Rutas con SO_BINDTODEVICE)
			go func() {
				defer wg.Done()
				tcpSuccess = m.checkTCP(tcpTarget, iface.IfaceName, currentIP, checkTimeout)
			}()

			wg.Wait()

			// --- FASE 3: Decisión (FIXED) ---
			// CRÍTICO: Si TCP funciona, el enlace físico/L2 está bien.
			// ICMP puede fallar si la ruta aún no existe en el Kernel (Catch-22).
			// Priorizamos TCP para declarar UP y permitir la rehidratación de rutas.
			isSuccess := false
			
			if tcpSuccess {
				isSuccess = true
				// Si ICMP falló pero TCP fue bien, usamos una latencia penalizada temporal
				// para permitir que el router restaure la ruta.
				if !icmpSuccess {
					if !isCurrentlyUp {
						monLog.Debug().Msg("TCP OK, ICMP Fail (Probable falta de ruta). Forzando UP para bootstrapping.")
					}
					// Asignamos latencia default para no bloquear SLA
					if rtt == 0 { rtt = 50 * time.Millisecond }
				}
			} else {
				// Si TCP falla, entonces realmente está caído o bloqueado por Firewall
				// Fallback: Si ICMP funciona (raro si TCP falla), lo aceptamos también
				if icmpSuccess {
					isSuccess = true
				}
			}
			
			// Debug de discrepancia
			if isCurrentlyUp && !isSuccess {
				if !tcpSuccess { monLog.Debug().Str("bind_iface", iface.IfaceName).Msg("Fallo TCP (Handshake)") }
			}

			m.handleStateChange(isSuccess, rtt, &consecutiveSuccesses, &consecutiveFailures, limitUp, limitDown, &isCurrentlyUp, iface.Name, monLog)
		}
	}
}

// handleStateChange centraliza la lógica de histéresis (UP/DOWN thresholds)
func (m *Monitor) handleStateChange(isSuccess bool, rtt time.Duration, succ *int, fails *int, limUp, limDown int, isUp *bool, name string, log zerolog.Logger) {
	stateChanged := false
	shouldEmit := false

	if isSuccess {
		*fails = 0
		*succ++
		if !*isUp && *succ >= limUp {
			*isUp = true
			stateChanged = true
			log.Info().Dur("latency", rtt).Msg("Interfaz RECUPERADA (UP)")
		}
		if *isUp {
			shouldEmit = true
		}
	} else {
		*succ = 0
		*fails++
		if *isUp && *fails >= limDown {
			*isUp = false
			stateChanged = true
			log.Warn().Msg("Interfaz CAÍDA (DOWN)")
		}
	}

	if stateChanged || shouldEmit {
		select {
		case m.Updates <- StatusEvent{
			InterfaceName: name,
			IsUp:          *isUp,
			Latency:       rtt,
		}:
		default:
			// Non-blocking drop para no saturar si el consumidor es lento
		}
		if stateChanged {
			*succ = 0
			*fails = 0
		}
	}
}

// checkICMP usa Pinger privilegiado y FUERZA la IP de origen
func (m *Monitor) checkICMP(target, sourceIP string, timeout time.Duration) (bool, time.Duration) {
	pinger, err := probing.NewPinger(target)
	if err != nil {
		return false, 0
	}
	
	pinger.Source = sourceIP 
	
	pinger.SetPrivileged(true)
	pinger.Count = 1
	pinger.Timeout = timeout

	if err := pinger.Run(); err != nil {
		return false, 0
	}
	stats := pinger.Statistics()
	return (stats.PacketsRecv > 0), stats.AvgRtt
}

// checkTCP usa Dial con BindToDevice Y SourceIP local
func (m *Monitor) checkTCP(address, ifaceName, sourceIP string, timeout time.Duration) bool {
	// Parseamos SourceIP. Si falla, nil deja que el OS elija (pero BindToDevice manda)
	localIP := net.ParseIP(sourceIP)
	var localAddr *net.TCPAddr
	if localIP != nil {
		localAddr = &net.TCPAddr{IP: localIP}
	}

	dialer := &net.Dialer{
		Timeout:   timeout,
		LocalAddr: localAddr, 
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				// Bind físico a la interfaz: ESTO ES LO QUE SALVA LA CONEXIÓN
				syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifaceName)
			})
		},
	}

	conn, err := dialer.Dial("tcp", address)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// getInterfaceIP busca la primera IP IPv4 no-loopback de una interfaz
func getInterfaceIP(ifaceName string) (string, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return "", err
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		// addr es tipo "192.168.1.5/24"
		ipNet, ok := addr.(*net.IPNet)
		if ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String(), nil
		}
	}
	return "", fmt.Errorf("no IPv4 found on interface %s", ifaceName)
}
