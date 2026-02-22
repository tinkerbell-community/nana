// Package config provides configuration management for the JetKVM management API,
// supporting CLI flags, environment variables, and YAML config files via Viper.
//
// The configuration defines server settings and a list of managed devices,
// each with a MAC address, optional name, and one or more BMC providers.
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Config holds the server and device configuration.
type Config struct {
	// Server settings.
	Port    int    `mapstructure:"port"    yaml:"port"`
	Address string `mapstructure:"address" yaml:"address"`

	// Logging.
	LogLevel string `mapstructure:"log_level" yaml:"log_level"`

	// WebRTC settings (used by JetKVM driver).
	WebRTCTimeout int `mapstructure:"webrtc_timeout" yaml:"webrtc_timeout"`

	// Go runtime tuning.
	MaxprocsEnable bool    `mapstructure:"maxprocs_enable" yaml:"maxprocs_enable"`
	MemlimitEnable bool    `mapstructure:"memlimit_enable" yaml:"memlimit_enable"`
	MemlimitRatio  float64 `mapstructure:"memlimit_ratio"  yaml:"memlimit_ratio"`

	// Managed devices.
	Devices []DeviceConfig `mapstructure:"devices" yaml:"devices"`
}

// DeviceConfig holds configuration for a managed BMC device.
type DeviceConfig struct {
	// Name is an optional human-readable identifier for the device.
	// Used as the primary Redfish system ID when present.
	Name string `mapstructure:"name" yaml:"name"`

	// MAC is the device's MAC address (required). Used as a fallback system ID
	// and as the canonical device identifier.
	MAC string `mapstructure:"mac" yaml:"mac"`

	// Providers lists the BMC providers that offer capabilities for this device.
	Providers []ProviderConfig `mapstructure:"providers" yaml:"providers"`
}

// BootMacroStepConfig defines a single keyboard input step within a boot macro.
type BootMacroStepConfig struct {
	Keys      []string `mapstructure:"keys"      yaml:"keys"`
	Modifiers []string `mapstructure:"modifiers" yaml:"modifiers"`
	Delay     string   `mapstructure:"delay"     yaml:"delay"`
}

// BootDeviceConfig defines the keyboard macro sequence for a boot device option.
type BootDeviceConfig struct {
	Device string                `mapstructure:"device" yaml:"device"`
	Delay  string                `mapstructure:"delay"  yaml:"delay"`
	Steps  []BootMacroStepConfig `mapstructure:"steps"  yaml:"steps"`
}

// ProviderConfig holds configuration for a single provider instance.
type ProviderConfig struct {
	// Type is the provider type name (e.g., "jetkvm", "unifi").
	Type string `mapstructure:"type" yaml:"type"`

	// Host is the provider's target hostname or IP address.
	Host string `mapstructure:"host" yaml:"host"`

	// Password is the optional authentication credential.
	Password string `mapstructure:"password" yaml:"password"`

	// APIKey is the UniFi API key. Used by the UniFi provider for both API access
	// and deriving the SSH key used to connect to managed switches.
	APIKey string `mapstructure:"api_key" yaml:"api_key"`

	// Site is the UniFi site name (default: "default"). Used by UniFi providers.
	Site string `mapstructure:"site" yaml:"site"`

	// Boot defines keyboard macro sequences for boot device selection.
	Boot []BootDeviceConfig `mapstructure:"boot" yaml:"boot"`
}

var (
	cfgFile string
	cfg     *Config
)

// InitConfig reads in config file and ENV variables if set.
func InitConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.AddConfigPath("$HOME")
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName("jetkvm-api")
	}

	viper.SetEnvPrefix("JETKVM_API")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	viper.AutomaticEnv()

	// Defaults.
	viper.SetDefault("port", 5000)
	viper.SetDefault("address", "0.0.0.0")
	viper.SetDefault("log_level", "info")
	viper.SetDefault("webrtc_timeout", 30)
	viper.SetDefault("maxprocs_enable", true)
	viper.SetDefault("memlimit_enable", true)
	viper.SetDefault("memlimit_ratio", 0.9)

	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}

// InitFlags binds CLI flags to Viper configuration keys.
func InitFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().
		StringVar(&cfgFile, "config", "", "Config file path (default: ./jetkvm-api.yaml)")
	cmd.Flags().Int("port", 5000, "HTTP server port")
	cmd.Flags().String("address", "0.0.0.0", "HTTP server bind address")
	cmd.Flags().String("log-level", "info", "Log level (debug, info, warn, error)")
	cmd.Flags().Int("webrtc-timeout", 30, "WebRTC connection timeout in seconds")

	_ = viper.BindPFlag("port", cmd.Flags().Lookup("port"))
	_ = viper.BindPFlag("address", cmd.Flags().Lookup("address"))
	_ = viper.BindPFlag("log_level", cmd.Flags().Lookup("log-level"))
	_ = viper.BindPFlag("webrtc_timeout", cmd.Flags().Lookup("webrtc-timeout"))
}

// LoadConfig unmarshals the Viper config into a Config struct and validates it.
func LoadConfig() (*Config, error) {
	var c Config
	if err := viper.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unable to decode config: %w", err)
	}
	if err := validateConfig(&c); err != nil {
		return nil, err
	}
	cfg = &c
	return &c, nil
}

// GetConfig returns the loaded configuration.
func GetConfig() *Config {
	return cfg
}

func validateConfig(config *Config) error {
	if config.Port < 1 || config.Port > 65535 {
		return fmt.Errorf("invalid server port: %d (must be 1-65535)", config.Port)
	}
	if config.WebRTCTimeout < 1 {
		return fmt.Errorf("webrtc_timeout must be a positive integer")
	}
	for i, dev := range config.Devices {
		if dev.MAC == "" {
			return fmt.Errorf("device[%d]: mac address is required", i)
		}
		if len(dev.Providers) == 0 {
			return fmt.Errorf("device[%d] (%s): at least one provider is required", i, dev.MAC)
		}
		for j, prv := range dev.Providers {
			if prv.Type == "" {
				return fmt.Errorf("device[%d].providers[%d]: provider type is required", i, j)
			}
		}
	}
	return nil
}
