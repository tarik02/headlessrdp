package servercmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adrg/xdg"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"headlessdesk/internal/authz"
	"headlessdesk/internal/backendpreset"
	"headlessdesk/internal/commandbackend"
	"headlessdesk/internal/control"
	"headlessdesk/internal/desktop"
	"headlessdesk/internal/freerdp"
	"headlessdesk/internal/healthapi"
	"headlessdesk/internal/httpapi"
	"headlessdesk/internal/mcpapi"
	"headlessdesk/internal/nanokvm"
	"headlessdesk/internal/version"
	"headlessdesk/internal/vnc"
)

type config struct {
	Server   serverConfig             `mapstructure:"server" yaml:"server"`
	Input    string                   `mapstructure:"input" yaml:"input"`
	Output   string                   `mapstructure:"output" yaml:"output"`
	Backends map[string]backendConfig `mapstructure:"backends" yaml:"backends"`
}

type serverConfig struct {
	ListenAddr    string     `mapstructure:"listen_addr" yaml:"listen_addr"`
	MCPPath       string     `mapstructure:"mcp_path" yaml:"mcp_path"`
	EnableHTTPAPI bool       `mapstructure:"enable_http_api" yaml:"enable_http_api"`
	EnableMCPAPI  bool       `mapstructure:"enable_mcp_api" yaml:"enable_mcp_api"`
	Auth          authConfig `mapstructure:"auth" yaml:"auth"`
}

type authConfig struct {
	Tokens []authTokenConfig `mapstructure:"tokens" yaml:"tokens"`
}

type authTokenConfig struct {
	Token     string   `mapstructure:"token" yaml:"token"`
	Audience  []string `mapstructure:"audience" yaml:"audience"`
	Audiences []string `mapstructure:"audiences" yaml:"audiences"`
	Scopes    []string `mapstructure:"scopes" yaml:"scopes"`
}

var defaultGinMode string

type backendConfig struct {
	Extends  []string      `mapstructure:"extends" yaml:"extends"`
	Type     string        `mapstructure:"type" yaml:"type"`
	Host     string        `mapstructure:"host" yaml:"host"`
	Port     uint          `mapstructure:"port" yaml:"port"`
	Username string        `mapstructure:"username" yaml:"username"`
	Password string        `mapstructure:"password" yaml:"password"`
	Width    uint          `mapstructure:"width" yaml:"width"`
	Height   uint          `mapstructure:"height" yaml:"height"`
	Insecure bool          `mapstructure:"insecure" yaml:"insecure"`
	RDP      rdpConfig     `mapstructure:"rdp" yaml:"rdp"`
	VNC      vncConfig     `mapstructure:"vnc" yaml:"vnc"`
	Command  commandConfig `mapstructure:"command" yaml:"command"`
}

type rdpConfig struct {
	Domain         string `mapstructure:"domain" yaml:"domain"`
	KeyboardLayout uint   `mapstructure:"keyboard_layout" yaml:"keyboard_layout"`
	GraphicsMode   string `mapstructure:"graphics_mode" yaml:"graphics_mode"`
}

type vncConfig struct {
	Shared   bool `mapstructure:"shared" yaml:"shared"`
	ViewOnly bool `mapstructure:"view_only" yaml:"view_only"`
}

type commandConfig struct {
	Timeout        string            `mapstructure:"timeout" yaml:"timeout"`
	SSH            sshConfig         `mapstructure:"ssh" yaml:"ssh"`
	Screenshot     commandSpecConfig `mapstructure:"screenshot" yaml:"screenshot"`
	ScreenshotCrop commandSpecConfig `mapstructure:"screenshot_crop" yaml:"screenshot_crop"`
	MoveMouse      commandSpecConfig `mapstructure:"move_mouse" yaml:"move_mouse"`
	MouseButton    commandSpecConfig `mapstructure:"mouse_button" yaml:"mouse_button"`
	MouseWheel     commandSpecConfig `mapstructure:"mouse_wheel" yaml:"mouse_wheel"`
	Key            commandSpecConfig `mapstructure:"key" yaml:"key"`
	KeyScancode    commandSpecConfig `mapstructure:"key_scancode" yaml:"key_scancode"`
	TypeText       commandSpecConfig `mapstructure:"type_text" yaml:"type_text"`
}

type commandSpecConfig struct {
	Argv    []string `mapstructure:"argv" yaml:"argv"`
	Script  string   `mapstructure:"script" yaml:"script"`
	Timeout string   `mapstructure:"timeout" yaml:"timeout"`
}

type sshConfig struct {
	Host                  string `mapstructure:"host" yaml:"host"`
	Port                  uint   `mapstructure:"port" yaml:"port"`
	Username              string `mapstructure:"username" yaml:"username"`
	Password              string `mapstructure:"password" yaml:"password"`
	PrivateKeyPath        string `mapstructure:"private_key_path" yaml:"private_key_path"`
	PrivateKeyPassphrase  string `mapstructure:"private_key_passphrase" yaml:"private_key_passphrase"`
	KnownHostsPath        string `mapstructure:"known_hosts_path" yaml:"known_hosts_path"`
	InsecureIgnoreHostKey bool   `mapstructure:"insecure_ignore_host_key" yaml:"insecure_ignore_host_key"`
	Timeout               string `mapstructure:"timeout" yaml:"timeout"`
}

type startedBackends struct {
	component *desktop.Composite
	output    desktop.OutputBackend
	input     desktop.InputBackend
}

type backendRoles struct {
	input  bool
	output bool
}

func New() *cobra.Command {
	var configPath string
	v := viper.New()

	cmd := &cobra.Command{
		Use:           "headlessdesk",
		Short:         "Remote desktop screenshot and control server",
		Version:       version.Short(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetVersionTemplate("{{.Name}} {{.Version}}\n")

	setDefaults(v)

	persistentFlags := cmd.PersistentFlags()
	persistentFlags.StringVar(&configPath, "config", "", "path to a YAML, TOML, or JSON config file")
	persistentFlags.String("input-backend", "default", "named backend for keyboard and mouse input")
	persistentFlags.String("output-backend", "default", "named backend for screenshots")
	persistentFlags.String("backend-type", "rdp", "default backend type: "+supportedBackendTypesDescription())
	persistentFlags.String("remote-host", "", "default remote desktop server hostname or IP")
	persistentFlags.Uint("remote-port", 0, "default remote desktop server port; defaults by backend type")
	persistentFlags.String("username", "", "default remote desktop username")
	persistentFlags.String("password", "", "default remote desktop password")
	persistentFlags.Uint("width", 1280, "default requested remote desktop width")
	persistentFlags.Uint("height", 720, "default requested remote desktop height")
	persistentFlags.Bool("insecure", false, "accept unknown or changed server certificates where supported")
	persistentFlags.String("rdp-domain", "", "default RDP domain")
	persistentFlags.Uint("rdp-keyboard-layout", 0x0409, "default RDP keyboard layout id")
	persistentFlags.String("rdp-graphics-mode", "auto", "default RDP graphics transport: auto, gfx, or bitmap")
	persistentFlags.Bool("vnc-shared", true, "request a shared VNC session")
	persistentFlags.Bool("vnc-view-only", false, "connect to VNC without sending input events")

	requireFlag(persistentFlags.Lookup("input-backend"))
	requireFlag(persistentFlags.Lookup("output-backend"))
	requireFlag(persistentFlags.Lookup("backend-type"))
	requireFlag(persistentFlags.Lookup("remote-host"))
	requireFlag(persistentFlags.Lookup("remote-port"))
	requireFlag(persistentFlags.Lookup("username"))
	requireFlag(persistentFlags.Lookup("password"))
	requireFlag(persistentFlags.Lookup("width"))
	requireFlag(persistentFlags.Lookup("height"))
	requireFlag(persistentFlags.Lookup("insecure"))
	requireFlag(persistentFlags.Lookup("rdp-domain"))
	requireFlag(persistentFlags.Lookup("rdp-keyboard-layout"))
	requireFlag(persistentFlags.Lookup("rdp-graphics-mode"))
	requireFlag(persistentFlags.Lookup("vnc-shared"))
	requireFlag(persistentFlags.Lookup("vnc-view-only"))

	cmd.AddCommand(newServeCommand(v, &configPath))
	cmd.AddCommand(newStdioMCPCommand(v, &configPath))
	cmd.AddCommand(newMountCommand(v, &configPath))
	cmd.AddCommand(newVersionCommand())
	return cmd
}

func Execute() error {
	return New().Execute()
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println(version.Details())
		},
	}
}

func newServeCommand(v *viper.Viper, configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP server with health, REST, and optional MCP endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(v, *configPath)
			if err != nil {
				return err
			}
			applyChangedFlags(cmd, &cfg)
			if err := resolveBackendExtends(&cfg); err != nil {
				return err
			}
			if err := validateConfig(cfg); err != nil {
				return err
			}
			if err := validateServeConfig(cfg); err != nil {
				return err
			}
			return runServe(cfg)
		},
	}

	flags := cmd.Flags()
	flags.String("listen-addr", "127.0.0.1:4243", "HTTP listen address")
	flags.String("mcp-path", "/mcp", "HTTP path for the MCP streamable endpoint")
	flags.Bool("enable-http-api", true, "enable the REST screenshot and input API")
	flags.Bool("enable-mcp-api", true, "enable the MCP streamable HTTP API")

	requireFlag(flags.Lookup("listen-addr"))
	requireFlag(flags.Lookup("mcp-path"))
	requireFlag(flags.Lookup("enable-http-api"))
	requireFlag(flags.Lookup("enable-mcp-api"))

	return cmd
}

func newStdioMCPCommand(v *viper.Viper, configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stdio-mcp",
		Short: "Run the MCP server over stdio",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(v, *configPath)
			if err != nil {
				return err
			}
			applyChangedFlags(cmd, &cfg)
			if err := resolveBackendExtends(&cfg); err != nil {
				return err
			}
			if err := validateConfig(cfg); err != nil {
				return err
			}
			return runStdioMCP(cfg)
		},
	}
}

func resolveMountpoint(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}

	for _, base := range []string{
		os.Getenv("XDG_RUNTIME_DIR"),
		xdg.RuntimeDir,
		xdg.CacheHome,
		os.TempDir(),
	} {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		info, err := os.Stat(base)
		if err == nil && info.IsDir() {
			return filepath.Join(base, "headlessdesk"), nil
		}
	}
	return "", errors.New("resolve default mountpoint: no runtime, cache, or temp directory available")
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.listen_addr", "127.0.0.1:4243")
	v.SetDefault("server.mcp_path", "/mcp")
	v.SetDefault("server.enable_http_api", true)
	v.SetDefault("server.enable_mcp_api", true)
	v.SetDefault("input", "default")
	v.SetDefault("output", "default")
	v.SetDefault("backends.default.type", "rdp")
	v.SetDefault("backends.default.width", 1280)
	v.SetDefault("backends.default.height", 720)
	v.SetDefault("backends.default.rdp.keyboard_layout", 0x0409)
	v.SetDefault("backends.default.rdp.graphics_mode", "auto")
	v.SetDefault("backends.default.vnc.shared", true)
}

func loadConfig(v *viper.Viper, configPath string) (config, error) {
	v.SetEnvPrefix("HEADLESSDESK")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return config{}, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg config
	if err := v.Unmarshal(&cfg); err != nil {
		return config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

func applyChangedFlags(cmd *cobra.Command, cfg *config) {
	setStringFlag(cmd, "input-backend", &cfg.Input)
	setStringFlag(cmd, "output-backend", &cfg.Output)
	setStringFlag(cmd, "listen-addr", &cfg.Server.ListenAddr)
	setStringFlag(cmd, "mcp-path", &cfg.Server.MCPPath)
	setBoolFlag(cmd, "enable-http-api", &cfg.Server.EnableHTTPAPI)
	setBoolFlag(cmd, "enable-mcp-api", &cfg.Server.EnableMCPAPI)

	if shouldConfigureDefaultBackend(cmd, *cfg) {
		defaultBackend := ensureDefaultBackend(cfg)
		setStringFlag(cmd, "backend-type", &defaultBackend.Type)
		setStringFlag(cmd, "remote-host", &defaultBackend.Host)
		setUintFlag(cmd, "remote-port", &defaultBackend.Port)
		setStringFlag(cmd, "username", &defaultBackend.Username)
		setStringFlag(cmd, "password", &defaultBackend.Password)
		setUintFlag(cmd, "width", &defaultBackend.Width)
		setUintFlag(cmd, "height", &defaultBackend.Height)
		setBoolFlag(cmd, "insecure", &defaultBackend.Insecure)
		setStringFlag(cmd, "rdp-domain", &defaultBackend.RDP.Domain)
		setUintFlag(cmd, "rdp-keyboard-layout", &defaultBackend.RDP.KeyboardLayout)
		setStringFlag(cmd, "rdp-graphics-mode", &defaultBackend.RDP.GraphicsMode)
		setBoolFlag(cmd, "vnc-shared", &defaultBackend.VNC.Shared)
		setBoolFlag(cmd, "vnc-view-only", &defaultBackend.VNC.ViewOnly)
		cfg.Backends["default"] = defaultBackend
	}
}

func ensureDefaultBackend(cfg *config) backendConfig {
	if cfg.Backends == nil {
		cfg.Backends = map[string]backendConfig{}
	}
	return cfg.Backends["default"]
}

func shouldConfigureDefaultBackend(cmd *cobra.Command, cfg config) bool {
	if strings.TrimSpace(cfg.Input) == "default" || strings.TrimSpace(cfg.Output) == "default" {
		return true
	}
	if _, ok := cfg.Backends["default"]; ok {
		return true
	}
	for _, name := range []string{
		"backend-type",
		"remote-host",
		"remote-port",
		"username",
		"password",
		"width",
		"height",
		"insecure",
		"rdp-domain",
		"rdp-keyboard-layout",
		"rdp-graphics-mode",
		"vnc-shared",
		"vnc-view-only",
	} {
		if changedFlag(cmd, name) != nil {
			return true
		}
	}
	return false
}

func resolveBackendExtends(cfg *config) error {
	for name, backend := range cfg.Backends {
		resolved := backendConfig{}
		for _, extend := range backend.Extends {
			presetName, err := parsePresetExtend(extend)
			if err != nil {
				return fmt.Errorf("backends.%s.extends: %w", name, err)
			}
			preset, ok, err := loadBackendPreset(presetName)
			if err != nil {
				return fmt.Errorf("backends.%s.extends preset %q: %w", name, presetName, err)
			}
			if !ok {
				return fmt.Errorf("backends.%s.extends references unknown preset %q", name, presetName)
			}
			resolved = mergeBackendConfig(resolved, preset)
		}
		backend.Extends = nil
		cfg.Backends[name] = mergeBackendConfig(resolved, backend)
	}
	return nil
}

func parsePresetExtend(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty extend entry")
	}
	if strings.HasPrefix(value, "preset:") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "preset:"))
	}
	if value == "" {
		return "", errors.New("empty preset name")
	}
	return value, nil
}

func loadBackendPreset(name string) (backendConfig, bool, error) {
	data, ok, err := backendpreset.Load(name)
	if err != nil || !ok {
		return backendConfig{}, ok, err
	}

	var cfg backendConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return backendConfig{}, true, err
	}
	return cfg, true, nil
}

func mergeBackendConfig(base backendConfig, override backendConfig) backendConfig {
	out := base
	if override.Type != "" {
		out.Type = override.Type
	}
	if override.Host != "" {
		out.Host = override.Host
	}
	if override.Port != 0 {
		out.Port = override.Port
	}
	if override.Username != "" {
		out.Username = override.Username
	}
	if override.Password != "" {
		out.Password = override.Password
	}
	if override.Width != 0 {
		out.Width = override.Width
	}
	if override.Height != 0 {
		out.Height = override.Height
	}
	if override.Insecure {
		out.Insecure = override.Insecure
	}
	out.RDP = mergeRDPConfig(out.RDP, override.RDP)
	out.VNC = mergeVNCConfig(out.VNC, override.VNC)
	out.Command = mergeCommandConfig(out.Command, override.Command)
	return out
}

func mergeRDPConfig(base rdpConfig, override rdpConfig) rdpConfig {
	out := base
	if override.Domain != "" {
		out.Domain = override.Domain
	}
	if override.KeyboardLayout != 0 {
		out.KeyboardLayout = override.KeyboardLayout
	}
	if override.GraphicsMode != "" {
		out.GraphicsMode = override.GraphicsMode
	}
	return out
}

func mergeVNCConfig(base vncConfig, override vncConfig) vncConfig {
	out := base
	if override.Shared {
		out.Shared = override.Shared
	}
	if override.ViewOnly {
		out.ViewOnly = override.ViewOnly
	}
	return out
}

func mergeCommandConfig(base commandConfig, override commandConfig) commandConfig {
	out := base
	if override.Timeout != "" {
		out.Timeout = override.Timeout
	}
	out.SSH = mergeSSHConfig(out.SSH, override.SSH)
	out.Screenshot = mergeCommandSpecConfig(out.Screenshot, override.Screenshot)
	out.ScreenshotCrop = mergeCommandSpecConfig(out.ScreenshotCrop, override.ScreenshotCrop)
	out.MoveMouse = mergeCommandSpecConfig(out.MoveMouse, override.MoveMouse)
	out.MouseButton = mergeCommandSpecConfig(out.MouseButton, override.MouseButton)
	out.MouseWheel = mergeCommandSpecConfig(out.MouseWheel, override.MouseWheel)
	out.Key = mergeCommandSpecConfig(out.Key, override.Key)
	out.KeyScancode = mergeCommandSpecConfig(out.KeyScancode, override.KeyScancode)
	out.TypeText = mergeCommandSpecConfig(out.TypeText, override.TypeText)
	return out
}

func mergeCommandSpecConfig(base commandSpecConfig, override commandSpecConfig) commandSpecConfig {
	out := base
	if commandConfigured(override) {
		out.Argv = append([]string(nil), override.Argv...)
		out.Script = override.Script
	}
	if override.Timeout != "" {
		out.Timeout = override.Timeout
	}
	return out
}

func mergeSSHConfig(base sshConfig, override sshConfig) sshConfig {
	out := base
	if override.Host != "" {
		out.Host = override.Host
	}
	if override.Port != 0 {
		out.Port = override.Port
	}
	if override.Username != "" {
		out.Username = override.Username
	}
	if override.Password != "" {
		out.Password = override.Password
	}
	if override.PrivateKeyPath != "" {
		out.PrivateKeyPath = override.PrivateKeyPath
	}
	if override.PrivateKeyPassphrase != "" {
		out.PrivateKeyPassphrase = override.PrivateKeyPassphrase
	}
	if override.KnownHostsPath != "" {
		out.KnownHostsPath = override.KnownHostsPath
	}
	if override.InsecureIgnoreHostKey {
		out.InsecureIgnoreHostKey = override.InsecureIgnoreHostKey
	}
	if override.Timeout != "" {
		out.Timeout = override.Timeout
	}
	return out
}

func changedFlag(cmd *cobra.Command, name string) *pflag.Flag {
	if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
		return flag
	}
	if flag := cmd.InheritedFlags().Lookup(name); flag != nil && flag.Changed {
		return flag
	}
	return nil
}

func setStringFlag(cmd *cobra.Command, name string, value *string) {
	if flag := changedFlag(cmd, name); flag != nil {
		*value = flag.Value.String()
	}
}

func setUintFlag(cmd *cobra.Command, name string, value *uint) {
	if flag := changedFlag(cmd, name); flag != nil {
		parsed, err := strconv.ParseUint(flag.Value.String(), 10, 0)
		if err == nil {
			*value = uint(parsed)
		}
	}
}

func setBoolFlag(cmd *cobra.Command, name string, value *bool) {
	if flag := changedFlag(cmd, name); flag != nil {
		parsed, err := strconv.ParseBool(flag.Value.String())
		if err == nil {
			*value = parsed
		}
	}
}

func validateConfig(cfg config) error {
	inputName := strings.TrimSpace(cfg.Input)
	outputName := strings.TrimSpace(cfg.Output)
	if inputName == "" {
		return errors.New("input is required")
	}
	if outputName == "" {
		return errors.New("output is required")
	}
	if len(cfg.Backends) == 0 {
		return errors.New("backends is required")
	}
	if _, ok := cfg.Backends[inputName]; !ok {
		return fmt.Errorf("input backend %q is not configured", inputName)
	}
	if _, ok := cfg.Backends[outputName]; !ok {
		return fmt.Errorf("output backend %q is not configured", outputName)
	}

	roles := map[string]backendRoles{
		inputName:  {input: true},
		outputName: {output: true},
	}
	if inputName == outputName {
		roles[inputName] = backendRoles{input: true, output: true}
	}
	for name, role := range roles {
		if err := validateBackendConfig(name, cfg.Backends[name], role); err != nil {
			return err
		}
	}
	return nil
}

func validateBackendConfig(name string, cfg backendConfig, roles backendRoles) error {
	backendType := normalizeBackendType(cfg)
	if backendType == "" {
		return fmt.Errorf("backends.%s.type is required", name)
	}
	switch backendType {
	case "rdp", "vnc", "command", "kwin", "eis", "windows", "macos", "nanokvm":
	default:
		return fmt.Errorf("invalid backends.%s.type: %q", name, cfg.Type)
	}
	if err := validateBackendPlatform(name, backendType); err != nil {
		return err
	}
	if cfg.Port > math.MaxUint16 {
		return fmt.Errorf("invalid backends.%s.port: %d", name, cfg.Port)
	}

	switch backendType {
	case "rdp":
		if strings.TrimSpace(cfg.Host) == "" {
			return fmt.Errorf("backends.%s.host is required", name)
		}
		if strings.TrimSpace(cfg.Username) == "" {
			return fmt.Errorf("backends.%s.username is required for rdp", name)
		}
		if strings.TrimSpace(cfg.Password) == "" {
			return fmt.Errorf("backends.%s.password is required for rdp", name)
		}
		if err := validateDimensions(name, cfg); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(cfg.RDP.GraphicsMode)) {
		case "", "auto", "avc", "h264", "gfx", "graphics", "bitmap", "legacy":
		default:
			return fmt.Errorf("invalid backends.%s.rdp.graphics_mode: %q", name, cfg.RDP.GraphicsMode)
		}
	case "vnc":
		if strings.TrimSpace(cfg.Host) == "" {
			return fmt.Errorf("backends.%s.host is required", name)
		}
		if err := validateDimensions(name, cfg); err != nil {
			return err
		}
		if roles.input && cfg.VNC.ViewOnly {
			return fmt.Errorf("backends.%s.vnc.view_only cannot be true for input backend", name)
		}
	case "nanokvm":
		if strings.TrimSpace(cfg.Host) == "" {
			return fmt.Errorf("backends.%s.host is required", name)
		}
		if strings.TrimSpace(cfg.Username) == "" {
			return fmt.Errorf("backends.%s.username is required for nanokvm", name)
		}
		if strings.TrimSpace(cfg.Password) == "" {
			return fmt.Errorf("backends.%s.password is required for nanokvm", name)
		}
	case "command":
		if roles.output && !commandConfigured(cfg.Command.Screenshot) {
			return fmt.Errorf("backends.%s.command.screenshot argv or script is required for output backend", name)
		}
		if roles.input {
			if !commandConfigured(cfg.Command.MoveMouse) {
				return fmt.Errorf("backends.%s.command.move_mouse argv or script is required for input backend", name)
			}
			if !commandConfigured(cfg.Command.MouseButton) {
				return fmt.Errorf("backends.%s.command.mouse_button argv or script is required for input backend", name)
			}
			if !commandConfigured(cfg.Command.MouseWheel) {
				return fmt.Errorf("backends.%s.command.mouse_wheel argv or script is required for input backend", name)
			}
			if !commandConfigured(cfg.Command.Key) {
				return fmt.Errorf("backends.%s.command.key argv or script is required for input backend", name)
			}
			if !commandConfigured(cfg.Command.TypeText) {
				return fmt.Errorf("backends.%s.command.type_text argv or script is required for input backend", name)
			}
		}
		if err := validateCommandDurations(name, cfg.Command); err != nil {
			return err
		}
	case "kwin":
		if roles.input {
			return fmt.Errorf("backends.%s.type kwin cannot be used for input backend", name)
		}
	case "eis":
		if roles.output {
			return fmt.Errorf("backends.%s.type eis cannot be used for output backend", name)
		}
	case "windows":
	case "macos":
	}
	return nil
}

func commandConfigured(cfg commandSpecConfig) bool {
	return len(cfg.Argv) > 0 || strings.TrimSpace(cfg.Script) != ""
}

func validateDimensions(name string, cfg backendConfig) error {
	if cfg.Width == 0 {
		return fmt.Errorf("backends.%s.width must be greater than zero", name)
	}
	if cfg.Height == 0 {
		return fmt.Errorf("backends.%s.height must be greater than zero", name)
	}
	if cfg.Width > math.MaxUint32 || cfg.Height > math.MaxUint32 {
		return fmt.Errorf("invalid backends.%s dimensions: %dx%d", name, cfg.Width, cfg.Height)
	}
	return nil
}

func validateCommandDurations(name string, cfg commandConfig) error {
	if _, err := parseDuration(cfg.Timeout); err != nil {
		return fmt.Errorf("invalid backends.%s.command.timeout: %w", name, err)
	}
	if _, err := parseDuration(cfg.SSH.Timeout); err != nil {
		return fmt.Errorf("invalid backends.%s.command.ssh.timeout: %w", name, err)
	}
	for key, spec := range map[string]commandSpecConfig{
		"screenshot":      cfg.Screenshot,
		"screenshot_crop": cfg.ScreenshotCrop,
		"move_mouse":      cfg.MoveMouse,
		"mouse_button":    cfg.MouseButton,
		"mouse_wheel":     cfg.MouseWheel,
		"key":             cfg.Key,
		"key_scancode":    cfg.KeyScancode,
		"type_text":       cfg.TypeText,
	} {
		if len(spec.Argv) > 0 && strings.TrimSpace(spec.Script) != "" {
			return fmt.Errorf("backends.%s.command.%s cannot define both argv and script", name, key)
		}
		if _, err := parseDuration(spec.Timeout); err != nil {
			return fmt.Errorf("invalid backends.%s.command.%s.timeout: %w", name, key, err)
		}
	}
	if err := validateSSHConfig(name, cfg.SSH); err != nil {
		return err
	}
	return nil
}

func validateSSHConfig(name string, cfg sshConfig) error {
	if !hasSSHConfig(cfg) {
		return nil
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return fmt.Errorf("backends.%s.command.ssh.host is required", name)
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return fmt.Errorf("backends.%s.command.ssh.username is required", name)
	}
	if cfg.Port > math.MaxUint16 {
		return fmt.Errorf("invalid backends.%s.command.ssh.port: %d", name, cfg.Port)
	}
	if cfg.Password == "" && cfg.PrivateKeyPath == "" {
		return fmt.Errorf("backends.%s.command.ssh.password or private_key_path is required", name)
	}
	if !cfg.InsecureIgnoreHostKey && cfg.KnownHostsPath == "" {
		return fmt.Errorf("backends.%s.command.ssh.known_hosts_path is required unless insecure_ignore_host_key is true", name)
	}
	return nil
}

func validateServeConfig(cfg config) error {
	if strings.TrimSpace(cfg.Server.ListenAddr) == "" {
		return errors.New("server.listen_addr is required")
	}
	if cfg.Server.EnableMCPAPI && !strings.HasPrefix(cfg.Server.MCPPath, "/") {
		return errors.New("server.mcp_path must start with '/'")
	}
	if _, err := newAuthorizer(cfg.Server.Auth); err != nil {
		return fmt.Errorf("server.auth: %w", err)
	}
	return nil
}

func runServe(cfg config) error {
	hideStandaloneConsole()
	configureGinMode()

	backends, err := startBackends(cfg)
	if err != nil {
		return err
	}
	defer closeComponent(backends.component)

	service := control.NewService(backends.component, backends.output, backends.input)
	authorizer, err := newAuthorizer(cfg.Server.Auth)
	if err != nil {
		return fmt.Errorf("server.auth: %w", err)
	}
	router := gin.Default()

	healthapi.RegisterRoutes(router, service)
	if cfg.Server.EnableHTTPAPI {
		httpapi.RegisterRoutes(router, service, authorizer)
	}
	if cfg.Server.EnableMCPAPI {
		router.Any(cfg.Server.MCPPath, gin.WrapH(mcpapi.NewHTTPHandler(service, authorizer)))
	}

	server := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: router,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("http server listening on %s", cfg.Server.ListenAddr)
	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- fmt.Errorf("http server failed: %w", err)
			return
		}
		serverErrCh <- nil
	}()

	select {
	case err := <-serverErrCh:
		return err
	case <-ctx.Done():
		return shutdownHTTPServer(server)
	case <-backends.component.Done():
		if err := backends.component.Err(); err != nil {
			log.Printf("backend graph ended: %v", err)
			if shutdownErr := shutdownHTTPServer(server); shutdownErr != nil {
				return errors.Join(err, shutdownErr)
			}
			return err
		}
		return shutdownHTTPServer(server)
	}
}

func runStdioMCP(cfg config) error {
	backends, err := startBackends(cfg)
	if err != nil {
		return err
	}
	defer closeComponent(backends.component)
	logComponentEnd("backend graph", backends.component)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return mcpapi.RunStdio(ctx, control.NewService(backends.component, backends.output, backends.input))
}

func newAuthorizer(cfg authConfig) (*authz.Authorizer, error) {
	tokens := make([]authz.Token, 0, len(cfg.Tokens))
	for _, token := range cfg.Tokens {
		tokens = append(tokens, authz.Token{
			Value:     token.Token,
			Audience:  token.Audience,
			Audiences: token.Audiences,
			Scopes:    token.Scopes,
		})
	}
	return authz.New(tokens)
}

func startBackends(cfg config) (*startedBackends, error) {
	inputName := strings.TrimSpace(cfg.Input)
	outputName := strings.TrimSpace(cfg.Output)

	var input desktop.InputBackend
	var output desktop.OutputBackend
	if inputName == outputName {
		session, err := startSessionBackend(outputName, cfg.Backends[outputName])
		if err != nil {
			return nil, err
		}
		input = session
		output = session
	} else {
		var err error
		output, err = startOutputBackend(outputName, cfg.Backends[outputName])
		if err != nil {
			return nil, err
		}
		input, err = startInputBackend(inputName, cfg.Backends[inputName])
		if err != nil {
			_ = output.Close()
			return nil, err
		}
	}

	component := desktop.NewComposite(inputName, input, outputName, output)
	return &startedBackends{component: component, output: output, input: input}, nil
}

func startSessionBackend(name string, cfg backendConfig) (desktop.Session, error) {
	switch normalizeBackendType(cfg) {
	case "rdp":
		return startRDPBackend(name, cfg)
	case "vnc":
		return startVNCBackend(name, cfg)
	case "nanokvm":
		return startNanoKVMBackend(name, cfg)
	case "command":
		return startCommandBackend(name, cfg)
	case "windows":
		return startWindowsBackend(name)
	case "macos":
		return startMacOSBackend(name)
	case "kwin":
		return nil, fmt.Errorf("backend %q type kwin does not support input", name)
	case "eis":
		return nil, fmt.Errorf("backend %q type eis does not support output", name)
	default:
		return nil, fmt.Errorf("unsupported backend %q type: %s", name, cfg.Type)
	}
}

func startOutputBackend(name string, cfg backendConfig) (desktop.OutputBackend, error) {
	switch normalizeBackendType(cfg) {
	case "rdp":
		return startRDPBackend(name, cfg)
	case "vnc":
		return startVNCBackend(name, cfg)
	case "nanokvm":
		return startNanoKVMBackend(name, cfg)
	case "command":
		return startCommandBackend(name, cfg)
	case "windows":
		return startWindowsBackend(name)
	case "macos":
		return startMacOSBackend(name)
	case "kwin":
		return startKWinBackend(name)
	case "eis":
		return nil, fmt.Errorf("backend %q type eis does not support output", name)
	default:
		return nil, fmt.Errorf("unsupported output backend %q type: %s", name, cfg.Type)
	}
}

func startInputBackend(name string, cfg backendConfig) (desktop.InputBackend, error) {
	switch normalizeBackendType(cfg) {
	case "rdp":
		return startRDPBackend(name, cfg)
	case "vnc":
		return startVNCBackend(name, cfg)
	case "nanokvm":
		return startNanoKVMBackend(name, cfg)
	case "command":
		return startCommandBackend(name, cfg)
	case "windows":
		return startWindowsBackend(name)
	case "macos":
		return startMacOSBackend(name)
	case "kwin":
		return nil, fmt.Errorf("backend %q type kwin does not support input", name)
	case "eis":
		return startKWinEISBackend(name)
	default:
		return nil, fmt.Errorf("unsupported input backend %q type: %s", name, cfg.Type)
	}
}

func startRDPBackend(name string, cfg backendConfig) (desktop.Session, error) {
	session, err := freerdp.StartSession(freerdp.Config{
		Host:               cfg.Host,
		Port:               backendPort(cfg, 3389),
		Username:           cfg.Username,
		Password:           cfg.Password,
		Domain:             cfg.RDP.Domain,
		DesktopWidth:       uint32(cfg.Width),
		DesktopHeight:      uint32(cfg.Height),
		KeyboardLayout:     uint32(cfg.RDP.KeyboardLayout),
		InsecureSkipVerify: cfg.Insecure,
		GraphicsMode:       cfg.RDP.GraphicsMode,
	})
	if err != nil {
		return nil, fmt.Errorf("start RDP backend %q: %w", name, err)
	}
	return session, nil
}

func startVNCBackend(name string, cfg backendConfig) (desktop.Session, error) {
	session, err := vnc.StartSession(vnc.Config{
		Host:          cfg.Host,
		Port:          backendPort(cfg, 5900),
		Username:      cfg.Username,
		Password:      cfg.Password,
		DesktopWidth:  uint32(cfg.Width),
		DesktopHeight: uint32(cfg.Height),
		Shared:        cfg.VNC.Shared,
		ViewOnly:      cfg.VNC.ViewOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("start VNC backend %q: %w", name, err)
	}
	return session, nil
}

func startNanoKVMBackend(name string, cfg backendConfig) (desktop.Session, error) {
	session, err := nanokvm.StartSession(nanokvm.Config{
		Host:               cfg.Host,
		Port:               uint16(cfg.Port),
		Username:           cfg.Username,
		Password:           cfg.Password,
		InsecureSkipVerify: cfg.Insecure,
	})
	if err != nil {
		return nil, fmt.Errorf("start NanoKVM backend %q: %w", name, err)
	}
	return session, nil
}

func startCommandBackend(name string, cfg backendConfig) (desktop.Session, error) {
	backend, err := newCommandBackend(cfg)
	if err != nil {
		return nil, fmt.Errorf("start command backend %q: %w", name, err)
	}
	return backend, nil
}

func newCommandBackend(cfg backendConfig) (*commandbackend.Backend, error) {
	timeout, err := parseDuration(cfg.Command.Timeout)
	if err != nil {
		return nil, err
	}
	sshCfg, err := commandSSHConfig(cfg.Command.SSH)
	if err != nil {
		return nil, err
	}
	return commandbackend.New(commandbackend.Config{
		Timeout:        timeout,
		SSH:            sshCfg,
		Screenshot:     commandSpec(cfg.Command.Screenshot),
		ScreenshotCrop: commandSpec(cfg.Command.ScreenshotCrop),
		MoveMouse:      commandSpec(cfg.Command.MoveMouse),
		MouseButton:    commandSpec(cfg.Command.MouseButton),
		MouseWheel:     commandSpec(cfg.Command.MouseWheel),
		Key:            commandSpec(cfg.Command.Key),
		KeyScancode:    commandSpec(cfg.Command.KeyScancode),
		TypeText:       commandSpec(cfg.Command.TypeText),
	})
}

func commandSSHConfig(cfg sshConfig) (*commandbackend.SSHConfig, error) {
	if !hasSSHConfig(cfg) {
		return nil, nil
	}
	timeout, err := parseDuration(cfg.Timeout)
	if err != nil {
		return nil, err
	}
	return &commandbackend.SSHConfig{
		Host:                  cfg.Host,
		Port:                  uint16(cfg.Port),
		Username:              cfg.Username,
		Password:              cfg.Password,
		PrivateKeyPath:        cfg.PrivateKeyPath,
		PrivateKeyPassphrase:  cfg.PrivateKeyPassphrase,
		KnownHostsPath:        cfg.KnownHostsPath,
		InsecureIgnoreHostKey: cfg.InsecureIgnoreHostKey,
		Timeout:               timeout,
	}, nil
}

func hasSSHConfig(cfg sshConfig) bool {
	return strings.TrimSpace(cfg.Host) != "" ||
		strings.TrimSpace(cfg.Username) != "" ||
		cfg.Password != "" ||
		cfg.PrivateKeyPath != "" ||
		cfg.PrivateKeyPassphrase != "" ||
		cfg.KnownHostsPath != "" ||
		cfg.InsecureIgnoreHostKey ||
		cfg.Port != 0 ||
		cfg.Timeout != ""
}

func commandSpec(cfg commandSpecConfig) commandbackend.Command {
	timeout, _ := parseDuration(cfg.Timeout)
	return commandbackend.Command{Argv: cfg.Argv, Script: cfg.Script, Timeout: timeout}
}

func parseDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	return time.ParseDuration(value)
}

func normalizeBackendType(cfg backendConfig) string {
	return strings.ToLower(strings.TrimSpace(cfg.Type))
}

func backendPort(cfg backendConfig, fallback uint16) uint16 {
	if cfg.Port == 0 {
		return fallback
	}
	return uint16(cfg.Port)
}

func closeComponent(component desktop.Component) {
	select {
	case <-component.Done():
		_ = component.Close()
		return
	default:
	}
	if err := component.Close(); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("backend close error: %v", err)
	}
}

func shutdownHTTPServer(server *http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http shutdown error: %w", err)
	}
	return nil
}

func configureGinMode() {
	if defaultGinMode == "" {
		return
	}
	gin.SetMode(defaultGinMode)
}

func logComponentEnd(name string, component desktop.Component) {
	go func() {
		<-component.Done()
		if err := component.Err(); err != nil {
			log.Printf("%s ended: %v", name, err)
			return
		}
		log.Printf("%s ended", name)
	}()
}

func requireFlag(flag *pflag.Flag) {
	if flag == nil {
		panic("flag must not be nil")
	}
}
