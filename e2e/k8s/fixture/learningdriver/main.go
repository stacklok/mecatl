//go:build kind_e2e

// Command learningdriver is a Kind-suite fixture, not a supported daemon.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
	"github.com/stacklok/mecatl/internal/adapter/automaticstore"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/reflectionstore"
	"github.com/stacklok/mecatl/internal/adapter/skillstore"
)

const defaultListenAddress = ":8443"

var fixtureAutomaticPolicy = learning.AutomaticAdmissionPolicy{
	Window: time.Hour, Cooldown: 10 * time.Minute, DedupeWindow: 24 * time.Hour,
	MaxCount: 8, MaxTokens: 100_000, MaxCountPerPrincipal: 4, MaxTokensPerPrincipal: 50_000,
	ReservationClaimDuration: time.Minute,
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("kind-learning-driver", flag.ContinueOnError)
	listenAddress := flags.String("listen", defaultListenAddress, "gRPC listen address")
	dataDir := flags.String("data-dir", "", "writable fixture data directory")
	certFile := flags.String("tls-cert", "", "PEM server certificate")
	keyFile := flags.String("tls-key", "", "PEM server private key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" || *certFile == "" || *keyFile == "" {
		return errors.New("--data-dir, --tls-cert, and --tls-key are required")
	}
	certificate, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		return fmt.Errorf("load fixture TLS keypair: %w", err)
	}
	server, err := newFixtureServer(*dataDir, grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	})))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()
	if err = server.Serve(listener); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// newFixtureServer composes the grpcdriver wrapper constructors exercised by
// internal/adapter/grpcdriver/*repository_test.go over the durable file stores;
// the fixture owns no repository RPC implementation.
func newFixtureServer(dataDir string, opts ...grpc.ServerOption) (*grpc.Server, error) {
	if dataDir == "" {
		return nil, errors.New("fixture data directory is required")
	}
	attempts, err := attemptstore.New(filepath.Join(dataDir, "attempts"))
	if err != nil {
		return nil, fmt.Errorf("open attempt repository: %w", err)
	}
	proposals, err := reflectionstore.New(filepath.Join(dataDir, "proposals"))
	if err != nil {
		return nil, fmt.Errorf("open proposal repository: %w", err)
	}
	skills, err := skillstore.New(filepath.Join(dataDir, "skills"))
	if err != nil {
		return nil, fmt.Errorf("open skill repository: %w", err)
	}
	ledger, err := automaticstore.New(filepath.Join(dataDir, "automatic"), fixtureAutomaticPolicy, wallclock.Clock{})
	if err != nil {
		return nil, fmt.Errorf("open automatic admission ledger: %w", err)
	}

	opts = append(opts,
		grpc.MaxRecvMsgSize(grpcdriver.MaxSnapshotBytes),
		grpc.MaxSendMsgSize(grpcdriver.MaxSnapshotBytes),
	)
	server := grpc.NewServer(opts...)
	driverv1.RegisterLearningRepositoryCapabilitiesServiceServer(server, grpcdriver.NewLearningRepositoryCapabilitiesServer(fixtureCapabilities()))
	driverv1.RegisterAttemptRepositoryServiceServer(server, grpcdriver.NewAttemptRepositoryServer(attempts))
	driverv1.RegisterProposalRepositoryServiceServer(server, grpcdriver.NewProposalRepositoryServer(proposals))
	driverv1.RegisterSkillRepositoryServiceServer(server, grpcdriver.NewSkillRepositoryServer(skills))
	driverv1.RegisterAutomaticAdmissionLedgerServiceServer(server, grpcdriver.NewAutomaticAdmissionLedgerServer(ledger))
	return server, nil
}

func fixtureCapabilities() grpcdriver.LearningRepositoryCapabilities {
	return grpcdriver.LearningRepositoryCapabilities{
		AttemptRepository: true, ProposalRepository: true, SkillRepository: true,
		ValidatedSkillActivation: true, AutomaticAdmissionLedger: true,
		OwnershipMode:                     grpcdriver.LearningRepositoryOwnershipTrusted,
		CallerInfrastructureRPCsSeparated: false,
	}
}
