// Command sandbox-manager is the only component with container-engine access for
// agent sandboxes. It holds no state: labelled containers and volumes are the record.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kiongahq/platform/internal/runtimeconfig"
	"github.com/kiongahq/platform/internal/sandbox"
)

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if err := run(logger); err != nil {
		logger.Fatal(err)
	}
}

func run(logger *log.Logger) error {
	if err := runtimeconfig.Load(); err != nil {
		return err
	}
	config, err := sandbox.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	driver, err := buildDriver(config)
	if err != nil {
		return err
	}
	service := sandbox.NewService(config, driver, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go service.Run(ctx)
	if config.AllowUnsafe {
		logger.Printf("WARNING: KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME=1: runtime %s is not verified as gVisor. Local development only.", config.Runtime)
	}
	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           sandbox.NewHandler(service, config.Token, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		// No WriteTimeout: an exec may run for 30 minutes, and the service bounds it.
		IdleTimeout: 2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Printf("sandbox manager listening on %s (driver %s, runtime %s)", server.Addr, config.Driver, config.Runtime)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func buildDriver(config sandbox.Config) (sandbox.Driver, error) {
	if config.Driver == "docker" {
		return sandbox.NewDocker(config.DockerSocket, config.DockerAPI), nil
	}
	cluster, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("the kubernetes driver needs an in-cluster service account: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cluster)
	if err != nil {
		return nil, err
	}
	return &sandbox.Kubernetes{
		Client:       clientset,
		Namespace:    config.Namespace,
		StorageClass: config.StorageClass,
		PolicyName:   config.NamePrefix + "default-deny",
		Execer:       &sandbox.RemoteExecer{Client: clientset, Config: cluster},
	}, nil
}

