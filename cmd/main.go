package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	//"github.com/containerd/containerd/log"
	//"github.com/containerd/log"
	"github.com/containerd/log"
	"github.com/google/uuid"
	commonIL "github.com/intertwin-eu/interlink-docker-plugin/pkg/common"
	docker "github.com/intertwin-eu/interlink-docker-plugin/pkg/docker"
	"github.com/intertwin-eu/interlink-docker-plugin/pkg/docker/dindmanager"
	"github.com/intertwin-eu/interlink-docker-plugin/pkg/docker/fpgastrategies"
	"github.com/sirupsen/logrus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"

	"github.com/virtual-kubelet/virtual-kubelet/trace"
	"github.com/virtual-kubelet/virtual-kubelet/trace/opentelemetry"

	"time"
)

func initProvider(ctx context.Context) (func(context.Context) error, error) {

	log.G(ctx).Info("\u2705 Tracing is enabled, setting up the TracerProvider")

	// Get the TELEMETRY_UNIQUE_ID from the environment, if it is not set, use the hostname
	uniqueID := os.Getenv("TELEMETRY_UNIQUE_ID")
	if uniqueID == "" {
		log.G(ctx).Info("No TELEMETRY_UNIQUE_ID set, generating a new one")
		newUUID := uuid.New()
		uniqueID = newUUID.String()
		log.G(ctx).Info("Generated unique ID: ", uniqueID, " use Plugin-"+uniqueID+" as service name from Grafana")
	}

	serviceName := "Plugin-" + uniqueID

	res, err := resource.New(ctx,
		resource.WithAttributes(
			// the service name used to display traces in backends
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	otlpEndpoint := os.Getenv("TELEMETRY_ENDPOINT")

	if otlpEndpoint == "" {
		otlpEndpoint = "localhost:4317"
	}

	log.G(ctx).Info("\u2705 TELEMETRY_ENDPOINT: ", otlpEndpoint)

	caCrtFilePath := os.Getenv("TELEMETRY_CA_CRT_FILEPATH")

	conn := &grpc.ClientConn{}
	if caCrtFilePath != "" {

		// if the CA certificate is provided, set up mutual TLS

		log.G(ctx).Info("CA certificate provided, setting up mutual TLS")

		caCert, err := ioutil.ReadFile(caCrtFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to load CA certificate: %w", err)
		}

		clientKeyFilePath := os.Getenv("TELEMETRY_CLIENT_KEY_FILEPATH")
		if clientKeyFilePath == "" {
			return nil, fmt.Errorf("client key file path not provided. Since a CA certificate is provided, a client key is required for mutual TLS")
		}

		clientCrtFilePath := os.Getenv("TELEMETRY_CLIENT_CRT_FILEPATH")
		if clientCrtFilePath == "" {
			return nil, fmt.Errorf("client certificate file path not provided. Since a CA certificate is provided, a client certificate is required for mutual TLS")
		}

		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to append CA certificate")
		}

		cert, err := tls.LoadX509KeyPair(clientCrtFilePath, clientKeyFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}

		tlsConfig := &tls.Config{
			Certificates:       []tls.Certificate{cert},
			RootCAs:            certPool,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		}
		creds := credentials.NewTLS(tlsConfig)
		conn, err = grpc.NewClient(otlpEndpoint, grpc.WithTransportCredentials(creds), grpc.WithBlock())

	} else {
		conn, err = grpc.NewClient(otlpEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn.WaitForStateChange(ctx, connectivity.Ready)

	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection to collector: %w", err)
	}

	// Set up a trace exporter
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	// Register the trace exporter with a TracerProvider, using a batch
	// span processor to aggregate spans before export.
	bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)
	otel.SetTracerProvider(tracerProvider)

	// set global propagator to tracecontext (the default is no-op).
	otel.SetTextMapPropagator(propagation.TraceContext{})

	log.G(ctx).Info("\u2705 Tracing setup complete")

	return tracerProvider.Shutdown, nil
}

func main() {
	logger := logrus.StandardLogger()

	interLinkConfig, err := commonIL.NewInterLinkConfig()
	if err != nil {
		log.G(context.Background()).Fatal(err)
	}

	if interLinkConfig.VerboseLogging {
		logger.SetLevel(logrus.DebugLevel)
	} else if interLinkConfig.ErrorsOnlyLogging {
		logger.SetLevel(logrus.ErrorLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	availableDinds := os.Getenv("AVAILABLEDINDS")
	if availableDinds == "" {
		availableDinds = "2"
	}
	var dindHandler dindmanager.DindManagerInterface = &dindmanager.DindManager{
		DindList: []dindmanager.DindSpecs{},
		Ctx:      ctx,
	}
	availableDindsInt, err := strconv.ParseInt(availableDinds, 10, 8)
	if err != nil {
		log.G(ctx).Info("\u2705 Error parsing availableDinds")
	}
	dindHandler.CleanDindContainers()
	dindHandler.BuildDindContainers(int8(availableDindsInt))

	SidecarAPIs := docker.SidecarHandler{
		Config:      interLinkConfig,
		Ctx:         ctx,
		DindManager: dindHandler,
	}

	log.G(ctx).Info("\u2705 Start cleaning zombie DIND containers")

	if os.Getenv("ENABLE_TRACING") == "1" {
		shutdown, err := initProvider(ctx)
		if err != nil {
			log.G(ctx).Fatal("\u274C failed to initialize TracerProvider: %w", err)
		}
		defer func() {
			if err = shutdown(ctx); err != nil {
				log.G(ctx).Fatal("\u274C failed to shutdown TracerProvider: %w", err)
			}
		}()

		log.G(ctx).Info("\u2705 Tracing is enabled")
		// TODO: disable this through options
		trace.T = opentelemetry.Adapter{}
	} else {
		log.G(ctx).Info("\u2705 Tracing is disabled")
	}

	if os.Getenv("FPGAENABLED") == "1" {
		fpgaManager := &fpgastrategies.FPGAManager{
			FPGASpecsList: []fpgastrategies.FPGASpecs{},
			Ctx:           ctx,
		}
		err = fpgaManager.Init()
		if err != nil {
			log.G(ctx).Info("\u274C Error during fpga init: %w", err)
		}

		err = fpgaManager.Discover()
		if err != nil {
			log.G(ctx).Info("\u274C Error during fpga discover: %w", err)
		}

		SidecarAPIs.FPGAManager = fpgaManager
	}

	log.G(ctx).Info(fmt.Sprintf("\u2705 Going to start the sidecar on port %s", interLinkConfig.Sidecarport))

	mutex := http.NewServeMux()
	mutex.HandleFunc("/status", SidecarAPIs.StatusHandler)
	mutex.HandleFunc("/create", SidecarAPIs.CreateHandler)
	mutex.HandleFunc("/delete", SidecarAPIs.DeleteHandler)
	mutex.HandleFunc("/getLogs", SidecarAPIs.GetLogsHandler)

	if strings.HasPrefix(interLinkConfig.Socket, "unix://") {
		// Create a Unix domain socket and listen for incoming connections.
		socket, err := net.Listen("unix", strings.ReplaceAll(interLinkConfig.Socket, "unix://", ""))
		if err != nil {
			panic(err)
		}

		// Cleanup the sockfile.
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			os.Remove(strings.ReplaceAll(interLinkConfig.Socket, "unix://", ""))
			os.Exit(1)
		}()
		server := http.Server{
			Handler: mutex,
		}

		log.G(ctx).Info(socket)

		if err := server.Serve(socket); err != nil {
			log.G(ctx).Fatal(err)
		}
	} else {
		err = http.ListenAndServe(":"+interLinkConfig.Sidecarport, mutex)
		if err != nil {
			log.G(ctx).Fatal(err)
		}
	}

	if err != nil {
		log.G(ctx).Fatal(err)
	}
}
