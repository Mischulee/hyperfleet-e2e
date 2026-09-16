package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/onsi/ginkgo/v2"
	hfl "github.com/openshift-hyperfleet/hyperfleet-logger"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/config"
)

const (
	logComponent = "hyperfleet-e2e"
	logVersion   = "dev"
)

// GinkgoLogHandler adds the active Ginkgo spec name to each log record.
//
// It wraps the shared HyperFleet handler so E2E logs retain the common format
// and metadata while preserving test-case context.
type GinkgoLogHandler struct {
	slog.Handler
}

// Handle adds the active Ginkgo spec name when logging from a spec.
func (h *GinkgoLogHandler) Handle(ctx context.Context, r slog.Record) error {
	report := ginkgo.CurrentSpecReport()
	if report.LeafNodeText != "" {
		r.AddAttrs(slog.String("test_case", report.LeafNodeText))
	}

	return h.Handler.Handle(ctx, r)
}

// WithAttrs preserves Ginkgo context injection for a logger with attributes.
func (h *GinkgoLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &GinkgoLogHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup preserves Ginkgo context injection for a logger with a group.
func (h *GinkgoLogHandler) WithGroup(name string) slog.Handler {
	return &GinkgoLogHandler{Handler: h.Handler.WithGroup(name)}
}

// initLogging configures the process-wide slog default with the shared
// HyperFleet handler and the E2E Ginkgo-context wrapper.
func initLogging(cfg *config.LogConfig) error {
	level, err := hfl.ParseLevel(cfg.Level)
	if err != nil {
		return fmt.Errorf("parse log level: %w", err)
	}

	format, err := hfl.ParseFormat(cfg.Format)
	if err != nil {
		return fmt.Errorf("parse log format: %w", err)
	}

	output, err := hfl.ParseOutput(cfg.Output)
	if err != nil {
		return fmt.Errorf("parse log output: %w", err)
	}

	slog.SetDefault(slog.New(newLogHandler(level, format, output)))

	return nil
}

func newLogHandler(level slog.Level, format hfl.Format, output io.Writer) slog.Handler {
	handler := hfl.NewHandler(logComponent, logVersion,
		hfl.WithLevel(level),
		hfl.WithFormat(format),
		hfl.WithOutput(output),
		hfl.WithSanitize(),
	)

	return &GinkgoLogHandler{Handler: handler}
}
