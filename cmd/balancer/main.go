package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/tu-usuario/gobalancer/internal/config"
	"github.com/tu-usuario/gobalancer/internal/logger"
	"github.com/tu-usuario/gobalancer/internal/monitor"
	"github.com/tu-usuario/gobalancer/internal/routing"
	"github.com/tu-usuario/gobalancer/internal/status"
)

const StatusFilePath = "/var/run/gobalancer/status.json"

func main() {
	// Logger Setup
	debug := false
	for _, arg := range os.Args {
		if arg == "--debug" || arg == "-d" {
			debug = true
		}
	}
	logger.Setup(debug)
	log := logger.Get()

	log.Info().Msg("🚀 Iniciando Go-NetBalancer Pro [Hot-Reload Enabled]")

	configPath := ""
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		configPath = os.Args[1]
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Error Crítico config")
	}

	// Router Setup
	router := routing.NewManager(cfg)
	if err := router.Setup(); err != nil {
		log.Fatal().Err(err).Msg("Fallo Setup Kernel")
	}
	router.EnableNAT()
	defer router.Cleanup()

	exporter := status.NewExporter(cfg, StatusFilePath)
	syncGateways(cfg, router, exporter)
	exporter.Start(3 * time.Second)
	defer exporter.Stop()

	// Monitor Setup
	eventsChan := make(chan monitor.StatusEvent, 32)
	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	mon := monitor.NewMonitor(cfg, eventsChan)
	mon.Start(monitorCtx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	routingState := make(map[string]*monitor.InterfaceState)
	ifaceIPs := make(map[string]string)
	for _, iface := range cfg.Interfaces {
		ifaceIPs[iface.Name] = iface.InterfaceIP
	}

	log.Info().Str("status_file", StatusFilePath).Msg("Sistema listo")

	for {
		select {
		case event := <-eventsChan:
			state, exists := routingState[event.InterfaceName]
			if !exists {
				state = &monitor.InterfaceState{}
				routingState[event.InterfaceName] = state
			}

			prevState := state.IsUp
			state.IsUp = event.IsUp
			state.Latency = event.Latency
			state.LastUpdate = time.Now()

			exporter.Update(event.InterfaceName, event.IsUp, event.Latency)

			if prevState != event.IsUp {
				logEvent := log.Info().Str("iface", event.InterfaceName)

				if event.IsUp {
					logEvent.Msg("Estado cambiado a UP 🟢 - Rehidratando")

					// 🔥 CORE FIX: REHIDRATACIÓN DE RUTAS
					// Si la interfaz vuelve, forzamos la regeneración de sus tablas
					// porque el kernel las borró al caerse la interfaz.
					router.ResetInterface(event.InterfaceName)

					// También actualizamos el Gateway en el exporter por si cambió (DHCP)
					syncGateways(cfg, router, exporter)

				} else {
					logEvent.Msg("Estado cambiado a DOWN 🔴 - Failover")
					// Flush Conntrack inmediato
					if ip, ok := ifaceIPs[event.InterfaceName]; ok && ip != "" {
						go router.FlushConntrack(ip)
					}
				}

				if cfg.General.OnEventScript != "" {
					statusStr := "DOWN"
					if event.IsUp {
						statusStr = "UP"
					}
					// Ejecutar hook en goroutine para no bloquear el loop principal
					go execHook(cfg.General.OnEventScript, event.InterfaceName, statusStr, "CHANGE", event.Latency)
				}
			}

			router.UpdateRoutes(routingState)

		case sig := <-sigChan:
			switch sig {
			case syscall.SIGHUP:
				log.Info().Msg("🔄 SIGHUP recibido: Iniciando Hot-Reload...")

				// 1. Carga Segura (Dry-Run)
				newCfg, err := config.LoadConfig(configPath)
				if err != nil {
					log.Error().Err(err).Msg("❌ Configuración nueva inválida. Se mantiene la actual.")
					continue
				}

				// 2. Parar Monitor actual
				log.Debug().Msg("Deteniendo monitor y limpiando rutas...")
				monitorCancel()

				// 3. Swap Atómico de Configuración
				*cfg = *newCfg

				// Actualizar IPs para Conntrack
				for _, iface := range cfg.Interfaces {
					ifaceIPs[iface.Name] = iface.InterfaceIP
				}

				// 4. Re-aplicar configuración al Kernel
				router.Cleanup()
				if err := router.Setup(); err != nil {
					log.Error().Err(err).Msg("❌ Error aplicando nueva configuración de red")
				} else {
					router.EnableNAT()
				}
				
				syncGateways(cfg, router, exporter)

				// 5. Reiniciar Monitor
				monitorCtx, monitorCancel = context.WithCancel(context.Background())
				mon.Start(monitorCtx)

				log.Info().Msg("✅ Hot-Reload completado exitosamente.")

			case syscall.SIGINT, syscall.SIGTERM:
				log.Info().Msg("🛑 Deteniendo servicio...")
				monitorCancel()
				exporter.Stop()
				router.Cleanup()
				os.Exit(0)
			}
		}
	}
}

func syncGateways(cfg *config.Config, router *routing.Manager, exporter *status.Exporter) {
	resolved := router.GetResolvedGateways()
	for _, iface := range cfg.Interfaces {
		actualIPNet, ok := resolved[iface.Name]
		if !ok {
			continue
		}
		actualIP := actualIPNet.String()
		if iface.Gateway == "auto" {
			exporter.UpdateGateway(iface.Name, fmt.Sprintf("%s (aut)", actualIP))
		} else {
			exporter.UpdateGateway(iface.Name, actualIP)
		}
	}
}

// execHook ejecuta un script externo inyectando el estado como Variables de Entorno.
// Sigue la especificación del README.
func execHook(scriptPath, iface, status, eventType string, latency time.Duration) {
	// Contexto con timeout para evitar procesos zombies si el script se cuelga
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath)
	
	// Heredar entorno actual y añadir las variables específicas
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("GOBALANCER_IFACE=%s", iface))
	cmd.Env = append(cmd.Env, fmt.Sprintf("GOBALANCER_STATUS=%s", status))
	cmd.Env = append(cmd.Env, fmt.Sprintf("GOBALANCER_EVENT_TYPE=%s", eventType))
	cmd.Env = append(cmd.Env, fmt.Sprintf("GOBALANCER_LATENCY=%s", latency.String()))

	// Capturamos salida estándar y error para loguear si falla
	output, err := cmd.CombinedOutput()
	
	logger := logger.Get().With().Str("hook", scriptPath).Logger()

	if err != nil {
		// Si es timeout, el error será context deadline exceeded
		if ctx.Err() == context.DeadlineExceeded {
			logger.Error().Msg("El script de notificación excedió el tiempo límite (10s)")
		} else {
			logger.Error().Err(err).Str("output", string(output)).Msg("Error ejecutando script de notificación")
		}
		return
	}

	logger.Debug().Msg("Notificación ejecutada correctamente")
}
