package main

import (
	"fmt"

	"github.com/dburman/nightsky/internal/web"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web UI server",
		Long:  "Serve the web UI for browsing captures and viewing configuration. Reads from the output directory; does not require a camera.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			srv := web.New(cfg, addr, logger)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address (e.g. :8080 or 0.0.0.0:8080)")
	return cmd
}
