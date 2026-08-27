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
			"The UI has no authentication and reports the camera's coordinates, so it binds to localhost by default. " +
			"To reach it from another machine, put it behind an authenticating reverse proxy, or pass --addr 0.0.0.0:8080 " +
			"if the network is trusted.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			srv := web.New(cfg, addr, logger)
			return srv.ListenAndServe(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "listen address (use 0.0.0.0:8080 to expose on the network — the UI is unauthenticated)")
	return cmd
}
