package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xeonliu/TboxWebdav/internal/auth"
	"github.com/xeonliu/TboxWebdav/internal/config"
	"github.com/xeonliu/TboxWebdav/internal/tbox"
	davhandler "github.com/xeonliu/TboxWebdav/internal/webdav"
)

func main() {
	var (
		configFile string
		port       int
		host       string
		cacheSize  int
		authMode   string
		username   string
		password   string
		cookie     string
		token      string
		accessMode string
		logLevel   string
	)

	root := &cobra.Command{
		Use:   "tboxwebdav",
		Short: "WebDAV server wrapping the SJTU Tencent Box (SMH) API",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Initialise structured logger.
			if err := initLogger(logLevel); err != nil {
				return fmt.Errorf("invalid --log-level %q: %w", logLevel, err)
			}

			var cfg *config.Config

			if configFile != "" {
				var err error
				cfg, err = config.LoadFromFile(configFile)
				if err != nil {
					return fmt.Errorf("failed to load config file: %w", err)
				}
				slog.Info("loaded config file", "path", configFile)
			} else {
				am, err := config.ParseAuthMode(authMode)
				if err != nil {
					return err
				}
				ac, err := config.ParseAccessMode(accessMode)
				if err != nil {
					return err
				}

				cfg = &config.Config{
					Host:       host,
					Port:       port,
					CacheSize:  cacheSize,
					AuthMode:   am,
					AccessMode: ac,
					Cookie:     cookie,
					UserToken:  token,
				}

				// If a username is provided on CLI, add it as a custom user.
				if username != "" {
					cfg.Users = append(cfg.Users, config.CustomUser{
						UserName:   username,
						Password:   password,
						Cookie:     cookie,
						UserToken:  token,
						AccessMode: ac,
					})
				}

				// Validate required fields.
				if am == config.AuthModeNone && token == "" && cookie == "" {
					return fmt.Errorf("--auth None requires --cookie or --token")
				}
				if am == config.AuthModeCustom && username == "" {
					return fmt.Errorf("--auth Custom requires --username")
				}
				if am == config.AuthModeCustom && token == "" && cookie == "" {
					return fmt.Errorf("--auth Custom requires --cookie or --token")
				}
			}

			return runServer(cfg)
		},
	}

	root.Flags().StringVarP(&configFile, "config", "c", "", "YAML config file (all other flags are ignored when set)")
	root.Flags().IntVarP(&port, "port", "p", 65472, "Listening port")
	root.Flags().StringVar(&host, "host", "localhost", "Listening host")
	root.Flags().IntVar(&cacheSize, "cachesize", 20*1024*1024, "Cache size in bytes")
	root.Flags().StringVar(&authMode, "auth", "Mixed", "Auth mode: None, JaCookie, UserToken, Custom, Mixed")
	root.Flags().StringVarP(&username, "username", "U", "", "Custom username for WebDAV auth")
	root.Flags().StringVarP(&password, "password", "P", "", "Custom password for WebDAV auth")
	root.Flags().StringVarP(&cookie, "cookie", "C", "", "JAAuthCookie for Tbox auth")
	root.Flags().StringVarP(&token, "token", "T", "", "UserToken for Tbox auth")
	root.Flags().StringVar(&accessMode, "access", "Full", "Access mode: Full, ReadOnly, NoDelete")
	root.Flags().StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, error")

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// initLogger configures the default slog logger with a text handler at the
// requested level writing to stderr.
func initLogger(level string) error {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "info", "":
		l = slog.LevelInfo
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return fmt.Errorf("unknown level %q", level)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
	return nil
}

func runServer(cfg *config.Config) error {
	client := tbox.NewClient()
	davHandler := davhandler.NewHandler(client)

	mux := http.NewServeMux()
	mux.Handle("/", auth.Middleware(cfg, davHandler))

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	slog.Info("TboxWebdav server started", "addr", "http://"+addr, "auth", cfg.AuthMode, "access", cfg.AccessMode)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "error", err)
		return err
	}
	return nil
}
