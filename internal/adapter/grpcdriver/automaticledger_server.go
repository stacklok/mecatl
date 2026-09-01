package grpcdriver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
)

type automaticAdmissionLedgerServer struct {
	driverv1.UnimplementedAutomaticAdmissionLedgerServiceServer
	ledger learning.AutomaticAdmissionLedger
}

// NewAutomaticAdmissionLedgerServer wraps a domain ledger as a driver service.
func NewAutomaticAdmissionLedgerServer(ledger learning.AutomaticAdmissionLedger) driverv1.AutomaticAdmissionLedgerServiceServer {
	return &automaticAdmissionLedgerServer{ledger: ledger}
}

func (s *automaticAdmissionLedgerServer) ReserveAutomaticAdmission(ctx context.Context, wire *driverv1.AutomaticReservationRequest) (*driverv1.AutomaticReservationResponse, error) {
	req := learning.AutomaticReservationRequest{
		ID: learning.AutomaticReservationID(wire.GetId()), AttemptID: learning.AttemptID(wire.GetAttemptId()),
		Principal: learning.AttemptPartition(wire.GetPrincipal()), Digest: learning.CanonicalDigest(wire.GetDigest()),
		Class: learning.AdmissionClass(wire.GetAdmissionClass()), Tokens: wire.GetTokens(),
		ExpectedPolicyRevision: learning.AutomaticAdmissionPolicyRevision(wire.GetExpectedPolicyRevision()),
	}
	if err := req.Validate(); err != nil {
		return nil, automaticRepositoryStatus(err)
	}
	reservation, err := s.ledger.Reserve(ctx, req)
	return serverAutomaticResponse(reservation, err)
}

func (s *automaticAdmissionLedgerServer) GetAutomaticReservation(ctx context.Context, req *driverv1.GetAutomaticReservationRequest) (*driverv1.GetAutomaticReservationResponse, error) {
	if req.GetId() == "" {
		return nil, automaticRepositoryStatus(learning.ErrInvalidAutomaticReservation)
	}
	reservation, found, err := s.ledger.Get(ctx, learning.AutomaticReservationID(req.GetId()))
	if err != nil {
		return nil, automaticRepositoryStatus(err)
	}
	if !found {
		return &driverv1.GetAutomaticReservationResponse{}, nil
	}
	if err = reservation.Validate(); err != nil {
		return nil, status.Error(codes.Internal, "automatic ledger returned an invalid reservation")
	}
	return &driverv1.GetAutomaticReservationResponse{Found: true, Reservation: automaticReservationToProto(reservation)}, nil
}

func (s *automaticAdmissionLedgerServer) ReassignAutomaticReservation(ctx context.Context, req *driverv1.ReassignAutomaticReservationRequest) (*driverv1.AutomaticReservationResponse, error) {
	if req.GetId() == "" || req.GetExpectedVersion() == "" {
		return nil, automaticRepositoryStatus(learning.ErrInvalidAutomaticReservation)
	}
	reservation, err := s.ledger.Reassign(ctx, learning.AutomaticReservationID(req.GetId()), learning.AutomaticReservationVersion(req.GetExpectedVersion()))
	return serverAutomaticResponse(reservation, err)
}

func (s *automaticAdmissionLedgerServer) RetainAutomaticReservation(ctx context.Context, req *driverv1.ResolveAutomaticReservationRequest) (*driverv1.AutomaticReservationResponse, error) {
	return s.resolve(ctx, req, true)
}

func (s *automaticAdmissionLedgerServer) ReclaimAutomaticReservation(ctx context.Context, req *driverv1.ResolveAutomaticReservationRequest) (*driverv1.AutomaticReservationResponse, error) {
	return s.resolve(ctx, req, false)
}

func (s *automaticAdmissionLedgerServer) resolve(ctx context.Context, req *driverv1.ResolveAutomaticReservationRequest, retain bool) (*driverv1.AutomaticReservationResponse, error) {
	fence, err := automaticFenceFromProto(req.GetFence(), true)
	if err != nil || req.GetId() == "" || req.GetExpectedVersion() == "" {
		return nil, automaticRepositoryStatus(learning.ErrInvalidAutomaticReservation)
	}
	var reservation learning.AutomaticReservation
	if retain {
		reservation, err = s.ledger.Retain(ctx, learning.AutomaticReservationID(req.GetId()), learning.AutomaticReservationVersion(req.GetExpectedVersion()), fence)
	} else {
		reservation, err = s.ledger.Reclaim(ctx, learning.AutomaticReservationID(req.GetId()), learning.AutomaticReservationVersion(req.GetExpectedVersion()), fence)
	}
	return serverAutomaticResponse(reservation, err)
}

func serverAutomaticResponse(reservation learning.AutomaticReservation, err error) (*driverv1.AutomaticReservationResponse, error) {
	if err != nil {
		return nil, automaticRepositoryStatus(err)
	}
	if err = reservation.Validate(); err != nil {
		return nil, status.Error(codes.Internal, "automatic ledger returned an invalid reservation")
	}
	return &driverv1.AutomaticReservationResponse{Reservation: automaticReservationToProto(reservation)}, nil
}
