package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tu-usuario/gobalancer/internal/config"
	"github.com/tu-usuario/gobalancer/internal/logger"
)

// InterfaceDetail representa el estado combinado (Config + Dinámico) para exportar
type InterfaceDetail struct {
	Name          string        `json:"name"`
	PhysicalIface string        `json:"iface_name"`    // <--- NUEVO CAMPO
	Type          string        `json:"type"`          // "ethernet", "vpn", etc
	Gateway       string        `json:"gateway"`
	IsUp          bool          `json:"is_up"`
	Latency       string        `json:"latency"`       // Human readable "15ms"
	LatencyNs     int64         `json:"latency_ns"`    // Para gráficas
	LastChange    time.Time     `json:"last_change"`
	LastChangeStr string        `json:"last_change_human"`
	Weight        int           `json:"weight"`
	Target        string        `json:"monitor_target"`
}

// SystemStatus es el objeto raíz del JSON
type SystemStatus struct {
	Timestamp  time.Time                  `json:"timestamp"`
	Algorithm  string                     `json:"algorithm"`
	Interfaces map[string]InterfaceDetail `json:"interfaces"`
}

// Exporter gestiona el estado de forma concurrente y segura
type Exporter struct {
	mu           sync.RWMutex
	cfg          *config.Config
	state        map[string]InterfaceDetail
	dumpPath     string
	stopChan     chan struct{}
}

func NewExporter(cfg *config.Config, path string) *Exporter {
	// Precargamos el mapa con datos estáticos de la config
	initialState := make(map[string]InterfaceDetail)
	
	for _, iface := range cfg.Interfaces {
		tipo := "ethernet"
		// Detección simple de tipo para el dashboard
		if iface.FailuresToDown > 5 { tipo = "backup" }
		
		initialState[iface.Name] = InterfaceDetail{
			Name:          iface.Name,
			PhysicalIface: iface.IfaceName, // <--- ASIGNACIÓN NUEVO CAMPO
			Type:          tipo,
			Gateway:       iface.Gateway,
			Weight:        iface.Weight,
			Target:        iface.MonitorTarget,
			IsUp:          false, // Asumimos down hasta primer ping
			Latency:       "N/A",
			LastChange:    time.Now(),
		}
	}

	return &Exporter{
		cfg:      cfg,
		state:    initialState,
		dumpPath: path,
		stopChan: make(chan struct{}),
	}
}

// Update actualiza el estado dinámico de una interfaz (Thread-Safe)
func (e *Exporter) Update(name string, isUp bool, latency time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if val, ok := e.state[name]; ok {
		// Detectar cambio de estado para actualizar timestamp
		if val.IsUp != isUp {
			val.LastChange = time.Now()
			val.LastChangeStr = val.LastChange.Format(time.RFC3339)
		}
		
		val.IsUp = isUp
		val.Latency = latency.String()
		val.LatencyNs = latency.Nanoseconds()
		
		e.state[name] = val
	}
}

// UpdateGateway permite actualizar la IP del Gateway (ej: resuelta vía DHCP)
func (e *Exporter) UpdateGateway(name string, gateway string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if val, ok := e.state[name]; ok {
		val.Gateway = gateway
		e.state[name] = val
	}
}

// Start comienza el ciclo de escritura en disco
func (e *Exporter) Start(interval time.Duration) {
	// Asegurar directorio
	dir := filepath.Dir(e.dumpPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.Get().Error().Str("dir", dir).Err(err).Msg("Error creando directorio status")
		return
	}

	go e.loop(interval)
}

func (e *Exporter) Stop() {
	close(e.stopChan)
}

func (e *Exporter) loop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Primera escritura inmediata
	e.dumpToDisk()

	for {
		select {
		case <-e.stopChan:
			return
		case <-ticker.C:
			e.dumpToDisk()
		}
	}
}

func (e *Exporter) dumpToDisk() {
	// 1. Snapshot (Lectura rápida bajo bloqueo)
	e.mu.RLock()
	snapshot := SystemStatus{
		Timestamp:  time.Now(),
		Algorithm:  e.cfg.General.Algorithm,
		Interfaces: make(map[string]InterfaceDetail, len(e.state)),
	}
	for k, v := range e.state {
		snapshot.Interfaces[k] = v
	}
	e.mu.RUnlock()

	// 2. Serialización y Escritura (Lento, sin bloqueo)
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		logger.Get().Error().Err(err).Msg("Error marshaling status json")
		return
	}

	// Escritura atómica (temp file + rename) para evitar lecturas parciales
	tmpPath := e.dumpPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.Get().Error().Err(err).Msg("Error escribiendo status tmp")
		return
	}
	if err := os.Rename(tmpPath, e.dumpPath); err != nil {
		logger.Get().Error().Err(err).Msg("Error rotando status file")
	}
}
