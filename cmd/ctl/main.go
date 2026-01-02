package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/tu-usuario/gobalancer/internal/status"
)

const (
	DefaultStatusPath = "/var/run/gobalancer/status.json"
	
	// ANSI Colors
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorBold   = "\033[1m"
)

func main() {
	watchMode := flag.Bool("w", false, "Modo Watch (actualizar cada 1s)")
	flag.Parse()

	if *watchMode {
		for {
			clearScreen()
			printStatus()
			time.Sleep(1 * time.Second)
		}
	} else {
		printStatus()
	}
}

func printStatus() {
	data, err := os.ReadFile(DefaultStatusPath)
	if err != nil {
		fmt.Printf("%s❌ No se puede leer el estado del balanceador (%s).%s\n", ColorRed, DefaultStatusPath, ColorReset)
		fmt.Println("   ¿Está corriendo el servicio? (sudo systemctl status gobalancer)")
		os.Exit(1)
	}

	var s status.SystemStatus
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Printf("❌ JSON corrupto: %v\n", err)
		os.Exit(1)
	}

	// Header Info
	fmt.Printf("%s🚀 GoBalancer Status%s  Last Update: %s\n", ColorBlue, ColorReset, s.Timestamp.Format("15:04:05"))
	fmt.Printf("   Algorithm: %s%s%s\n\n", ColorBold, s.Algorithm, ColorReset)

	// Definición de formato de tabla
	// Se ha añadido la columna PHYSICAL (ancho 10)
	// INTERFACE(18) | PHYSICAL(10) | TYPE(10) | STATUS(10) | LATENCY(15) | WEIGHT(8) | GATEWAY(22) | TARGET(15)
	headerFmt := "%-18s %-10s %-10s %-10s %-15s %-8s %-22s %-15s\n"
	rowFmt    := "%-18s %-10s %-10s %s %s %-8d %-22s %-15s\n" // Status y Latency son %s manuales para meter color

	fmt.Printf(headerFmt, "INTERFACE", "PHYSICAL", "TYPE", "STATUS", "LATENCY", "WEIGHT", "GATEWAY", "TARGET")
	fmt.Printf(headerFmt, "---------", "--------", "----", "------", "-------", "------", "-------", "------")

	// Sorting
	type row struct {
		id string
		d  status.InterfaceDetail
	}
	rows := make([]row, 0, len(s.Interfaces))
	for k, v := range s.Interfaces {
		rows = append(rows, row{k, v})
	}
	
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].d.IsUp != rows[j].d.IsUp {
			return rows[i].d.IsUp // Up primero
		}
		return rows[i].d.Name < rows[j].d.Name
	})

	for _, r := range rows {
		i := r.d
		
		// Status Colorizado
		statusTxt := "DOWN"
		statusColor := ColorRed
		if i.IsUp {
			statusTxt = "UP"
			statusColor = ColorGreen
		}
		// Padding manual del status para que el color no rompa el ancho
		paddedStatus := fmt.Sprintf("%-10s", statusTxt)
		finalStatus := statusColor + paddedStatus + ColorReset

		// Latency Colorizada
		latTxt := i.Latency
		latColor := ColorReset
		if i.IsUp {
			if i.LatencyNs < 50_000_000 { // < 50ms
				latColor = ColorGreen
			} else if i.LatencyNs > 150_000_000 { // > 150ms
				latColor = ColorRed
			} else {
				latColor = ColorYellow
			}
		} else {
			latTxt = "-"
		}
		paddedLat := fmt.Sprintf("%-15s", latTxt)
		finalLat := latColor + paddedLat + ColorReset

		fmt.Printf(rowFmt, 
			truncate(i.Name, 17), 
			truncate(i.PhysicalIface, 9), // <--- DATO FÍSICO (eth0, etc)
			truncate(i.Type, 9), 
			finalStatus, 
			finalLat, 
			i.Weight, 
			truncate(i.Gateway, 21),
			truncate(i.Target, 15),
		)
	}
	fmt.Println()
}

func clearScreen() {
	cmd := exec.Command("clear")
	cmd.Stdout = os.Stdout
	cmd.Run()
}

// Helper para cortar strings largos que rompen la tabla
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
