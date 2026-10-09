package main

import (
	"flag"
	"github.com/kiongahq/platform/internal/runtimeconfig"
	"log"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kiongahq/platform/internal/integrations"
	platformoperator "github.com/kiongahq/platform/internal/operator"
	mlaiopsv1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

func main() {
	if err := runtimeconfig.Load(); err != nil {
		log.Fatal(err)
	}
	var metricsAddress, probeAddress string
	var leaderElection bool
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "Metrics endpoint address")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8082", "Health endpoint address")
	flag.BoolVar(&leaderElection, "leader-elect", true, "Enable leader election")
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseDevMode(os.Getenv("ENVIRONMENT") != "production")))
	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(appsv1.AddToScheme(scheme))
	must(corev1.AddToScheme(scheme))
	must(mlaiopsv1.AddToScheme(scheme))
	cacheOptions := cache.Options{}
	if target := os.Getenv("MLAIOPS_TARGET_NAMESPACE"); target != "" {
		cacheOptions.DefaultNamespaces = map[string]cache.Config{target: {}, os.Getenv("MLAIOPS_CONTROL_NAMESPACE"): {}}
		if os.Getenv("MLAIOPS_CONTROL_NAMESPACE") == "" {
			log.Fatal("MLAIOPS_CONTROL_NAMESPACE is required with namespace-scoped operator")
		}
	}
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Cache:  cacheOptions,
		Scheme: scheme, LeaderElection: leaderElection, LeaderElectionID: "mlaiops-operator.mlaiops.io",
		HealthProbeBindAddress: probeAddress, Metrics: metricsserver.Options{BindAddress: metricsAddress},
	})
	must(err)
	must((&platformoperator.AgentReconciler{Client: manager.GetClient(), TraceProxyImage: env("TRACE_PROXY_IMAGE", "ghcr.io/mlaiops/trace-proxy:latest")}).SetupWithManager(manager))
	must((&platformoperator.WorkspaceReconciler{
		Client: manager.GetClient(), WorkbenchImage: env("WORKBENCH_IMAGE", "ghcr.io/kiongahq/jupyter:latest"),
		IDEImage: env("IDE_IMAGE", "ghcr.io/kiongahq/ide:latest"), GatewayURL: env("MLAIOPS_URL", "http://mlaiops-gateway.mlaiops-system:8080"),
		FeatureURL: env("WORKSPACE_FEATURE_URL", "http://mlaiops-feature-gateway.mlaiops-system:8083"), StorageURL: env("WORKSPACE_STORAGE_URL", "http://mlaiops-storage-proxy.mlaiops-system:8084"),
		MLflowURL: env("WORKSPACE_MLFLOW_URL", "http://mlflow.mlaiops-system:5000"), PrefectURL: env("WORKSPACE_PREFECT_URL", "http://prefect-server.mlaiops-system:4200/api"),
		LangfuseURL: env("WORKSPACE_LANGFUSE_URL", "http://langfuse.mlaiops-system:3000"), KafkaRESTURL: env("WORKSPACE_KAFKA_REST_URL", "http://kafka-rest.mlaiops-system:8082"),
		StorageClass:          os.Getenv("WORKSPACE_STORAGE_CLASS"),
		AllowedStorageClasses: platformoperator.ParseStorageClasses(os.Getenv("WORKSPACE_ALLOWED_STORAGE_CLASSES")),
	}).SetupWithManager(manager))
	must((&platformoperator.PipelineReconciler{
		Client: manager.GetClient(), KFP: integrations.NewKFP(os.Getenv("KFP_URL"), os.Getenv("KFP_TOKEN")),
		ExperimentID: os.Getenv("KFP_EXPERIMENT_ID"),
	}).SetupWithManager(manager))
	must((&platformoperator.ModelPromotionReconciler{
		Client: manager.GetClient(), MLflow: integrations.NewMLflow(os.Getenv("MLFLOW_URL"), os.Getenv("MLFLOW_TOKEN")),
	}).SetupWithManager(manager))
	must(manager.AddHealthzCheck("healthz", healthz.Ping))
	must(manager.AddReadyzCheck("readyz", healthz.Ping))
	log.Printf("starting mlaiops Kubernetes operator")
	must(manager.Start(ctrl.SetupSignalHandler()))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
