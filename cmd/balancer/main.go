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
	log.Println("🚀 Iniciando Go-NetBalancer Pro [CLI Enabled]...")

	// 1. Gestión de argumentos (Archivo de config personalizado)
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

	// Helper para iniciar monitores
	startMonitors := func(c *config.Config, ctx context.Context) {
		mon := monitor.NewMonitor(c, eventsChan)
		mon.Start(ctx)
	}

	startMonitors(cfg, monitorCtx)

	// 6. Señales
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	interfaceStatus := make(map[string]bool)

	log.Println("✅ Sistema listo. Esperando eventos...")

	for {
		select {
		case event := <-eventsChan:
			currentStatus, known := interfaceStatus[event.InterfaceName]
			
			if !known || currentStatus != event.IsUp {
				stateStr := "DOWN 🔴"
				if event.IsUp { stateStr = "UP 🟢" }
				
				log.Printf("[%s] %s (Latencia: %v)", event.InterfaceName, stateStr, event.Latency)
				interfaceStatus[event.InterfaceName] = event.IsUp
				
				router.UpdateRoutes(interfaceStatus)
			}

		case sig := <-sigChan:
			switch sig {
			case syscall.SIGHUP:
				log.Println("🔄 Recibida señal SIGHUP: Recargando configuración...")
				monitorCancel()
				
				// Recargamos usando la MISMA ruta con la que iniciamos
				newCfg := config.LoadConfig(configPath)
				router.Cfg = newCfg
				router.EnableNAT() 

				monitorCtx, monitorCancel = context.WithCancel(context.Background())
				startMonitors(newCfg, monitorCtx)
				
				log.Println("   -> Recalculando rutas...")
				router.UpdateRoutes(interfaceStatus)

			case syscall.SIGINT, syscall.SIGTERM:
				log.Printf("🛑 Apagando por señal %v...", sig)
				monitorCancel()
				time.Sleep(100 * time.Millisecond)
				os.Exit(0)
			}
		}
	}
}
