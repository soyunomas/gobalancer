package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tu-usuario/gobalancer/internal/config"
	"github.com/tu-usuario/gobalancer/internal/monitor"
	"github.com/tu-usuario/gobalancer/internal/routing"
	"github.com/tu-usuario/gobalancer/internal/status"
)

const StatusFilePath = "/var/run/gobalancer/status.json"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("🚀 Iniciando Go-NetBalancer Pro [SLA Aware]...")

	configPath := ""
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("❌ Error Crítico al inicio: %v", err)
	}

	router := routing.NewManager(cfg)
	if err := router.Setup(); err != nil {
		log.Fatalf("Setup Kernel: %v", err)
	}
	if err := router.EnableNAT(); err != nil {
		log.Fatalf("NAT: %v", err)
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
	// Mapa auxiliar para buscar IP rápidamente por nombre de interfaz (para el Flush)
	ifaceIPs := make(map[string]string)
	for _, iface := range cfg.Interfaces {
		ifaceIPs[iface.Name] = iface.InterfaceIP
	}

	log.Printf("✅ Sistema listo. Estado disponible en: %s", StatusFilePath)

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
				stateStr := "DOWN 🔴"
				if event.IsUp {
					stateStr = "UP 🟢"
				} else {
					// CRÍTICO: La interfaz ha caído.
					// Si tenía IP asignada, limpiamos sus conexiones zombis.
					// Intentamos obtener la IP del mapa auxiliar o de la config.
					if ip, ok := ifaceIPs[event.InterfaceName]; ok && ip != "" {
						// Ejecutamos Flush de forma asíncrona para no bloquear el loop de eventos
						go router.FlushConntrack(ip)
					}
				}
				log.Printf("[EVENT] %s ha cambiado a %s (Latencia: %v)", event.InterfaceName, stateStr, event.Latency)
			}

			router.UpdateRoutes(routingState)

		case sig := <-sigChan:
			switch sig {
			case syscall.SIGHUP:
				log.Println("🔄 Recargando configuración...")
				
				newCfg, err := config.LoadConfig(configPath)
				if err != nil {
					log.Printf("⚠️  Config inválida: %v. Ignorando.", err)
					continue
				}

				monitorCancel()
				exporter.Stop() 

				router.Cfg = newCfg
				router.Cleanup()
				router.Setup()
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
				
				log.Println("✨ Config recargada.")

			case syscall.SIGINT, syscall.SIGTERM:
				log.Printf("🛑 Apagando...")
				monitorCancel()
				exporter.Stop()
				router.Cleanup()
				time.Sleep(100 * time.Millisecond)
				os.Exit(0)
			}
		}
	}
}
