package main

import (
	"context"
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cephv1alpha1 "github.com/IUCLabs/cephrgw-operator/api/v1alpha1"
	"github.com/IUCLabs/cephrgw-operator/internal/ceph"
	"github.com/IUCLabs/cephrgw-operator/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cephv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr string
	var leaderElect bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics endpoint address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe address")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	logger := ctrl.Log.WithName("setup")

	cephCfg, endpoint, err := cephConfigFromEnv()
	if err != nil {
		logger.Error(err, "invalid Ceph dashboard configuration")
		os.Exit(1)
	}
	cephClient, err := ceph.NewClient(cephCfg)
	if err != nil {
		logger.Error(err, "unable to build Ceph dashboard client")
		os.Exit(1)
	}
	// Fail fast on obviously-broken credentials, but do not block startup on a
	// transiently unreachable dashboard.
	pingCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := cephClient.Ping(pingCtx); err != nil {
		logger.Error(err, "initial dashboard authentication failed; continuing and will retry per-reconcile")
	}
	cancel()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "cephrgw-operator",
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err := controller.SetupAll(mgr, cephClient, endpoint); err != nil {
		logger.Error(err, "unable to set up controllers")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	logger.Info("starting cephrgw-operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "manager exited with error")
		os.Exit(1)
	}
}

// cephConfigFromEnv reads dashboard connection settings from the environment,
// which the deployment sources from the cephrgw-credentials Secret.
func cephConfigFromEnv() (ceph.Config, string, error) {
	cfg := ceph.Config{
		BaseURL:  os.Getenv("CEPH_DASHBOARD_URL"),
		Username: os.Getenv("CEPH_DASHBOARD_USERNAME"),
		Password: os.Getenv("CEPH_DASHBOARD_PASSWORD"),
		Insecure: os.Getenv("CEPH_DASHBOARD_INSECURE") == "true",
	}
	if f := os.Getenv("CEPH_DASHBOARD_CA_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return cfg, "", err
		}
		cfg.CACert = b
	} else if inline := os.Getenv("CEPH_DASHBOARD_CA"); inline != "" {
		cfg.CACert = []byte(inline)
	}
	endpoint := os.Getenv("CEPH_RGW_ENDPOINT")
	return cfg, endpoint, nil
}
