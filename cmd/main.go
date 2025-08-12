// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"

	corev1 "k8s.io/api/core/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"github.com/inditextech/redkeyoperator/controllers"
	//+kubebuilder:scaffold:imports
)

const (
	USER_AGENT_NAME    = "redkey-cluster-operator"
	USER_AGENT_VERSION = "1.3.0"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(redkeyv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var maxConcurrentReconciles int
	var concurrentMigrates int

	// NOTE: This is needed to enable RSA again after update to go v1.22
	// https://tip.golang.org/doc/go1.22 (crypto/tls)
	os.Setenv("GODEBUG", "tlsrsakex=1")

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", 10, "Maximum number of concurrent events.")
	flag.IntVar(&concurrentMigrates, "concurrent-migrates", 3, "Number of concurrent migrate operations.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	godotenv.Load("./.env")

	// Get namespace(s) to watch
	watchNamespace, err := getWatchNamespace()
	if err != nil {
		setupLog.Info("unable to get WatchNamespace, the manager will watch and manage resources in all namespaces")
	}

	// Controller options
	ctrlOptions := ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port: 9443,
		}),
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "db95d8a6.inditex.dev",
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{
					&corev1.ConfigMap{},
					&corev1.Secret{},
				},
			},
		},
		Cache: cache.Options{
			DefaultNamespaces: watchNamespace,
		},
	}
	config := ctrl.GetConfigOrDie()
	config.UserAgent = USER_AGENT_NAME + "/" + USER_AGENT_VERSION
	mgr, err := ctrl.NewManager(config, ctrlOptions)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = controllers.NewRedKeyClusterReconciler(mgr, maxConcurrentReconciles, concurrentMigrates).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RedisCluster")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// getWatchNamespace returns the Namespace the operator should be watching for changes
func getWatchNamespace() (map[string]cache.Config, error) {
	// WatchNamespaceEnvVar is the constant for env variable WATCH_NAMESPACE
	// which specifies the Namespace to watch.
	// An empty value means the operator is running with cluster scope.
	var watchNamespaceEnvVar = "WATCH_NAMESPACE"

	namespaces, found := os.LookupEnv(watchNamespaceEnvVar)
	if !found {
		return nil, fmt.Errorf("%s must be set", watchNamespaceEnvVar)
	}

	watchNamespaces := make(map[string]cache.Config)
	for _, ns := range strings.Split(namespaces, ",") {
		setupLog.Info("manager set up with namespace", "namespace", ns)
		watchNamespaces[ns] = cache.Config{}
	}

	return watchNamespaces, nil
}
