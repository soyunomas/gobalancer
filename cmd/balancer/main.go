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
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("🚀 Iniciando Go-NetBalancer Pro [SLA Aware]...")

	// 1. Gestión de argumentos
	configPath := ""
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	// 2. Cargar Configuración
	cfg := config.LoadConfig(configPath)
	
	// 3. Inicializar Router
	router := routing.NewManager(cfg)
	if err := router.Setup(); err != nil {
		log.Fatalf("Setup Kernel: %v", err)
	}
	if err := router.EnableNAT(); err != nil {
		log.Fatalf("NAT: %v", err)
	}

	// 4. Canal de Eventos
	eventsChan := make(chan monitor.StatusEvent, 32)

	// 5. Contexto para monitores
	monitorCtx, monitorCancel := context.WithCancel(context.Background())

	startMonitors := func(c *config.Config, ctx context.Context) {
		mon := monitor.NewMonitor(c, eventsChan)
		mon.Start(ctx)
	}

	startMonitors(cfg, monitorCtx)

	// 6. Señales
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// CAMBIO: Almacenamos estado completo (Punteros para evitar copias)
	interfaceStatus := make(map[string]*monitor.InterfaceState)

	log.Println("✅ Sistema listo. Esperando eventos...")

	for {
		select {
		case event := <-eventsChan:
			// Recuperar o crear estado
			state, exists := interfaceStatus[event.InterfaceName]
			if !exists {
				state = &monitor.InterfaceState{}
				interfaceStatus[event.InterfaceName] = state
			}
			
			// Actualizamos datos
			state.IsUp = event.IsUp
			state.Latency = event.Latency
			state.LastUpdate = time.Now()

			// Logging informativo (Solo si cambia UP/DOWN para no floodear, o debug de SLA)
			stateStr := "DOWN 🔴"
			if event.IsUp { stateStr = "UP 🟢" }
			log.Printf("[EVENT] %s %s (Latencia: %v)", event.InterfaceName, stateStr, event.Latency)
				
			// El Router ahora decide si usarla o no basándose en el SLA
			router.UpdateRoutes(interfaceStatus)

		case sig := <-sigChan:
			switch sig {
			case syscall.SIGHUP:
				log.Println("🔄 Recibida señal SIGHUP: Recargando configuración...")
				monitorCancel()
				
				newCfg := config.LoadConfig(configPath)
				router.Cfg = newCfg
				router.EnableNAT() 

				monitorCtx, monitorCancel = context.WithCancel(context.Background())
				startMonitors(newCfg, monitorCtx)
				
				// Limpiamos estados antiguos para evitar usar datos obsoletos
				interfaceStatus = make(map[string]*monitor.InterfaceState)
				log.Println("   -> Config recargada. Esperando nuevos datos de monitor...")

			case syscall.SIGINT, syscall.SIGTERM:
				log.Printf("🛑 Apagando por señal %v...", sig)
				monitorCancel()
				time.Sleep(100 * time.Millisecond)
				os.Exit(0)
			}
		}
	}
}
