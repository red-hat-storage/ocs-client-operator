package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/service/webhook/handlers/validatesubscription"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

type serverConfig struct {
	listenPort int
}

const (
	pathToCertFile = "/etc/tls/private/tls.crt"
	pathToKeyFile  = "/etc/tls/private/tls.key"
)

func readEnvVar[T any](envVarName string, defaultValue T, parser func(str string) (T, error)) (T, error) {
	if str := os.Getenv(envVarName); str == "" {
		klog.Infof("no user-defined %s provided, defaulting to %v", envVarName, defaultValue)
		return defaultValue, nil
	} else if value, err := parser(str); err != nil {
		return *new(T), fmt.Errorf("malformed user-defined %s value %s: %v", envVarName, str, err)
	} else { //nolint:revive
		// skipping revive lint as it isn't allowing use of scoped values in else
		return value, nil
	}
}

func loadAndValidateServerConfig() (*serverConfig, error) {
	var config serverConfig
	var err error

	config.listenPort, err = readEnvVar("WEBHOOK_PORT", 8080, strconv.Atoi)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func newKubeClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	err := v1alpha1.AddToScheme(scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add v1alpha1 to the scheme. %v", err)
	}

	cfg, err := config.GetConfig()
	if err != nil {
		klog.Error("failed to get rest.Config", err)
		return nil, err
	}

	newClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}

	return newClient, nil
}

func main() {

	klog.Info("Starting webhook server with TLS enabled")

	config, err := loadAndValidateServerConfig()
	if err != nil {
		klog.Exitf("shutting down, failed to load server config: %v", err)
	}

	cl, err := newKubeClient()
	if err != nil {
		klog.Exitf("shutting down, failed to create kubernetes api client: %v", err)
	}

	klog.Info("Webhook server listening on port ", config.listenPort)
	httpServer := &http.Server{
		Addr: fmt.Sprintf("%s%d", ":", config.listenPort),
	}

	// handlers
	http.HandleFunc("/validate-subscription", func(w http.ResponseWriter, r *http.Request) {
		validatesubscription.HandleMessage(w, r, cl)
	})

	// start server
	go func() {
		if err := httpServer.ListenAndServeTLS(pathToCertFile, pathToKeyFile); !errors.Is(err, http.ErrServerClosed) {
			klog.Exitf("HTTP server error: %v", err)
		}
		klog.Info("Stopped serving new connections.")
	}()

	// cancellation signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownRelease()

	// shutdown and wait (via context) for cleanup of http resources
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		klog.Exitf("HTTP shutdown error: %v", err)
	}
	klog.Info("Graceful shutdown complete.")
}
