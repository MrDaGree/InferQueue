package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"inferqueue/internal/config"
	dbpkg "inferqueue/internal/db"
	"inferqueue/internal/handlers"
)

func main() {
	cfg := config.Load()

	db, err := dbpkg.Connect(cfg.DSN)
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}

	for _, dir := range []string{cfg.FileStore, cfg.SlurmLogDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Error("cannot create directory", "path", dir, "err", err)
			os.Exit(1)
		}
	}

	filesH := handlers.NewFilesHandler(db, cfg.FileStore)
	batchesH := handlers.NewBatchesHandler(db, cfg)

	e := echo.New()
	e.HideBanner = true

	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod: true, LogURI: true, LogStatus: true, LogLatency: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			slog.Info("request",
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
				"latency", v.Latency,
				"user", c.Request().Header.Get("X-User-Id"),
			)
			return nil
		},
	}))
	e.Use(middleware.Recover())

	v1 := e.Group("/v1")

	// Files — full spec coverage including list endpoint
	v1.GET("/files", filesH.List)
	v1.POST("/files", filesH.Upload)
	v1.GET("/files/:file_id", filesH.GetMetadata)
	v1.GET("/files/:file_id/content", filesH.GetContent)
	v1.DELETE("/files/:file_id", filesH.Delete)

	// Batches
	v1.POST("/batches", batchesH.Create)
	v1.GET("/batches", batchesH.List)
	v1.GET("/batches/:batch_id", batchesH.Get)
	v1.POST("/batches/:batch_id/cancel", batchesH.Cancel)

	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "service": "batch-shim"})
	})

	go func() {
		addr := ":" + cfg.Port
		slog.Info("batch-shim listening", "addr", addr)
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = e.Shutdown(ctx)
}
