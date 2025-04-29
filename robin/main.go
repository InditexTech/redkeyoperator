package main

import (
	"context"
	"flag"
	"log"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/inditextech/redisoperator/robin/httpserver"
	"github.com/inditextech/redisoperator/robin/metrics"
	"github.com/inditextech/redisoperator/robin/redis"
	"github.com/inditextech/redisoperator/robin/redisopconf"
)

const configmapFilePath = "/opt/conf/configmap/application-configmap.yml"

func main() {
	var metricsAddr string

	// Load Redis configuration (e.g., from environment or file) and build a metrics manager.
	conf := redisopconf.GetConfiguration()
	metricsManager := metrics.NewMetricsManager()
	ctx := context.Background()

	// Create a new RedisPollMetrics instance to gather metrics in the background.
	redisPollMetrics, err := redis.NewRedisPollMetrics(conf, metricsManager)
	if err != nil {
		log.Fatalf("failed to create Redis metrics poller: %v", err)
	}
	go redisPollMetrics.Start(ctx)

	// Parse CLI flags (e.g., --metrics-bind-address :8080).
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.Parse()

	// Set up controller-runtime Manager options.
	ctrlOptions := ctrl.Options{
		Logger: ctrl.Log.WithName("metrics-server"),
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},
	}

	// Create the manager for metrics and additional HTTP endpoints.
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrlOptions)
	if err != nil {
		log.Fatalf("unable to start manager: %v", err)
	}

	// Build a FileConfigProvider and Server to serve /configmap and /amiga/health.
	configProvider := &httpserver.FileConfigProvider{
		Path: configmapFilePath,
	}
	serverHandler := &httpserver.Server{
		ConfigProvider: configProvider,
	}

	// Attach the Server’s handlers to the manager’s metrics server.
	// The server’s ServeHTTP method will route /configmap and /amiga/health internally.
	if err := mgr.AddMetricsServerExtraHandler("/configmap", serverHandler); err != nil {
		log.Fatalf("unable to attach /configmap handler: %v", err)
	}
	if err := mgr.AddMetricsServerExtraHandler("/amiga/health", serverHandler); err != nil {
		log.Fatalf("unable to attach /amiga/health handler: %v", err)
	}

	// Start the manager (blocking call until shutdown).
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Fatalf("problem running manager: %v", err)
	}
}
