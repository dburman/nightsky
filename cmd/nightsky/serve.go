package main

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/dburman/nightsky/internal/web"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web UI server",
		Long: "Serve the web UI for browsing captures and viewing configuration. Reads from the output directory; does not require a camera.\n\n" +
			"The UI has no authentication and reports the camera's coordinates, so it listens on 127.0.0.1:8080 unless told otherwise. " +
			"Set web.addr in the config file (or NIGHTSKY_WEB_ADDR) to bind elsewhere — \"0.0.0.0:8080\" for every interface, " +
			"or a specific interface address. --addr overrides the config when passed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}

			// The flag wins only when actually passed, so a config file (or
			// NIGHTSKY_WEB_ADDR) is not silently overridden by the flag's
			// default.
			if !cmd.Flags().Changed("addr") {
				addr = cfg.Web.Addr
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			srv := web.New(cfg, addr, logger)
			return srv.ListenAndServe(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address, e.g. 0.0.0.0:8080 — the UI is unauthenticated (default: from config web.addr, 127.0.0.1:8080)")
	return cmd
}
