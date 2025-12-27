package config

import (
	"log"
	"os"

	"github.com/spf13/viper"
)

type LanConfig struct {
	IfaceName  string `mapstructure:"iface_name"`
	EnableDHCP bool   `mapstructure:"enable_dhcp"`
}

type InterfaceConfig struct {
	Name           string
	IfaceName      string `mapstructure:"iface_name"`
	Gateway        string
	InterfaceIP    string `mapstructure:"interface_ip"`
	Weight         int
	MonitorTarget  string `mapstructure:"monitor_target"`
	MonitorPort    int    `mapstructure:"monitor_port"` // Agregado para soportar configuración del Wizard
	FailuresToDown int    `mapstructure:"failures_to_down"`
	SuccessesToUp  int    `mapstructure:"successes_to_up"`
}

type Config struct {
	General struct {
		CheckInterval string `mapstructure:"check_interval"`
		Algorithm     string
	}
	Lan        LanConfig
	Interfaces []InterfaceConfig
}

func LoadConfig(customPath string) *Config {
	if customPath != "" {
		if _, err := os.Stat(customPath); os.IsNotExist(err) {
			log.Fatalf("❌ Error: Config no existe: %s", customPath)
		}
		viper.SetConfigFile(customPath)
	} else {
		viper.SetConfigName("config") 
		viper.SetConfigType("toml")
		viper.AddConfigPath("/etc/gobalancer/")
		viper.AddConfigPath(".")
	}

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("❌ Error leyendo config: %s", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		log.Fatalf("Error decodificando config: %s", err)
	}
	
	// Validar valores por defecto si el TOML es antiguo
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].MonitorPort == 0 {
			cfg.Interfaces[i].MonitorPort = 53
		}
		if cfg.Interfaces[i].Weight < 1 {
			cfg.Interfaces[i].Weight = 1
		}
	}

	return &cfg
}
