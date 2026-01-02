package main

import (
	"context"
	"os"
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
	// 1. Setup Logger Optimizado (Zerolog)
	// Comprobamos si hay flag debug (simple check de args por ahora)
	debug := false
	for _, arg := range os.Args {
		if arg == "--debug" || arg == "-d" {
			debug = true
		}
	}
	logger.Setup(debug)
	log := logger.Get()

	log.Info().Msg("🚀 Iniciando Go-NetBalancer Pro [SLA Aware]")

	configPath := ""
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		configPath = os.Args[1]
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Error Crítico al cargar configuración")
	}

	router := routing.NewManager(cfg)
	if err := router.Setup(); err != nil {
		log.Fatal().Err(err).Msg("Fallo en Setup Kernel")
	}
	if err := router.EnableNAT(); err != nil {
		log.Fatal().Err(err).Msg("Fallo configurando NAT")
	}
	defer router.Cleanup()

	exporter := status.NewExporter(cfg, StatusFilePath)
	exporter.Start(3 * time.Second)
	defer exporter.Stop()

	eventsChan := make(chan monitor.StatusEvent, 32)
	monitorCtx, monitorCancel := context.WithCancel(context.Background())

	startMonitors := func(c *config.Config, ctx context.Context) {
		mon := monitor.NewMonitor(c, eventsChan)
		mon.Start(ctx)
	}
	startMonitors(cfg, monitorCtx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	routingState := make(map[string]*monitor.InterfaceState)
	ifaceIPs := make(map[string]string)
	for _, iface := range cfg.Interfaces {
		ifaceIPs[iface.Name] = iface.InterfaceIP
	}

	log.Info().Str("status_file", StatusFilePath).Msg("Sistema listo y monitoreando")

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
				logEvent := log.Info().Str("interface", event.InterfaceName).Dur("latency", event.Latency)
				
				if event.IsUp {
					logEvent.Msg("Estado cambiado a UP 🟢")
				} else {
					logEvent.Msg("Estado cambiado a DOWN 🔴 - Iniciando Failover")
					
					// CRÍTICO: Flush Conntrack
					if ip, ok := ifaceIPs[event.InterfaceName]; ok && ip != "" {
						go router.FlushConntrack(ip)
					}
				}
			}

			// Actualizar rutas solo si hay cambios relevantes (throttling opcional podría ir aquí)
			router.UpdateRoutes(routingState)

		case sig := <-sigChan:
			switch sig {
			case syscall.SIGHUP:
				log.Info().Msg("🔄 SIGHUP recibido. Recargando configuración...")
				
				newCfg, err := config.LoadConfig(configPath)
				if err != nil {
					log.Error().Err(err).Msg("Configuración inválida en recarga. Ignorando.")
					continue
				}

				monitorCancel()
				exporter.Stop() 

				router.Cfg = newCfg
				router.Cleanup()
				if err := router.Setup(); err != nil {
					log.Error().Err(err).Msg("Error setup router tras reload")
				}
				router.EnableNAT()

				exporter = status.NewExporter(newCfg, StatusFilePath)
				exporter.Start(3 * time.Second)

				routingState = make(map[string]*monitor.InterfaceState)
				ifaceIPs = make(map[string]string)
				for _, iface := range newCfg.Interfaces {
					ifaceIPs[iface.Name] = iface.InterfaceIP
				}
				
				monitorCtx, monitorCancel = context.WithCancel(context.Background())
				startMonitors(newCfg, monitorCtx)
				
				log.Info().Msg("✨ Configuración recargada exitosamente.")

			case syscall.SIGINT, syscall.SIGTERM:
				log.Info().Msg("🛑 Señal de apagado recibida.")
				monitorCancel()
				exporter.Stop()
				router.Cleanup()
				time.Sleep(100 * time.Millisecond)
				os.Exit(0)
			}
		}
	}
}
