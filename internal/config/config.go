package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type LanConfig struct {
	IfaceName  string `mapstructure:"iface_name"`
	EnableDHCP bool   `mapstructure:"enable_dhcp"`
}

type RoutingRule struct {
	Name            string
	Type            string
	Value           string
	Protocol        string
	TargetInterface string `mapstructure:"target_interface"`
}

type InterfaceConfig struct {
	Name           string
	IfaceName      string `mapstructure:"iface_name"`
	Gateway        string
	InterfaceIP    string `mapstructure:"interface_ip"`
	Weight         int
	MonitorTarget  string `mapstructure:"monitor_target"`
	MonitorPort    int    `mapstructure:"monitor_port"`
	FailuresToDown int    `mapstructure:"failures_to_down"`
	SuccessesToUp  int    `mapstructure:"successes_to_up"`
	MaxLatency     string `mapstructure:"max_latency"`
}

type Config struct {
	General struct {
		CheckInterval string `mapstructure:"check_interval"`
		// AÑADIDO: Tag mapstructure para asegurar que Viper lo encuentra
		Algorithm     string `mapstructure:"algorithm"`
	}
	Lan        LanConfig
	Interfaces []InterfaceConfig
	Rules      []RoutingRule
}

func LoadConfig(customPath string) (*Config, error) {
	v := viper.New()

	if customPath != "" {
		if _, err := os.Stat(customPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("config no existe: %s", customPath)
		}
		v.SetConfigFile(customPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("toml")
		v.AddConfigPath("/etc/gobalancer/")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("error leyendo archivo config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("error decodificando estructura (sintaxis toml): %w", err)
	}

	// --- FIX: Valores por Defecto (Hardcoded) ---
	// Si el config.toml tiene la línea comentada, esto debe saltar.
	if cfg.General.Algorithm == "" {
		cfg.General.Algorithm = "weighted_round_robin"
	}
	if cfg.General.CheckInterval == "" {
		cfg.General.CheckInterval = "2s"
	}

	// Validaciones Lógicas
	if len(cfg.Interfaces) == 0 {
		return nil, fmt.Errorf("config inválida: se requiere al menos 1 interfaz")
	}

	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].MonitorPort == 0 {
			cfg.Interfaces[i].MonitorPort = 53
		}
		if cfg.Interfaces[i].Weight < 1 {
			cfg.Interfaces[i].Weight = 1
		}
	}

	for i := range cfg.Rules {
		if cfg.Rules[i].Protocol == "" {
			cfg.Rules[i].Protocol = "tcp"
		}
		cfg.Rules[i].Protocol = strings.ToLower(cfg.Rules[i].Protocol)
	}

	return &cfg, nil
}
