package client

import (
	"context"
	"fmt"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// WorkspaceEnrollmentStatus is the safe whole-bundle client state.
type WorkspaceEnrollmentStatus string

const (
	// WorkspaceEnrollmentPending indicates that workspace services are connecting.
	WorkspaceEnrollmentPending WorkspaceEnrollmentStatus = "pending"
	// WorkspaceEnrollmentConnected indicates that all workspace services connected.
	WorkspaceEnrollmentConnected WorkspaceEnrollmentStatus = "connected"
	// WorkspaceEnrollmentCancelled indicates that the enrollment was cancelled.
	WorkspaceEnrollmentCancelled WorkspaceEnrollmentStatus = "cancelled"
	// WorkspaceEnrollmentFailed indicates that the enrollment failed.
	WorkspaceEnrollmentFailed WorkspaceEnrollmentStatus = "failed"
)

// WorkspaceEnrollment contains only whole-bundle progress. PresentationURL is
// ephemeral command output: the UI opens it and never stores or renders it.
type WorkspaceEnrollment struct {
	ID               string
	Status           WorkspaceEnrollmentStatus
	RequiredServices uint32
	PresentationURL  string
}

// WorkspaceEnrollmentController is deliberately separate from permission and
// per-tool MCP authorization controls. No method accepts a backend selector.
type WorkspaceEnrollmentController interface {
	ConnectWorkspaceServices(context.Context, string) (WorkspaceEnrollment, error)
	RetryWorkspaceEnrollment(context.Context, string, string) (WorkspaceEnrollment, error)
	CancelWorkspaceEnrollment(context.Context, string, string) (WorkspaceEnrollment, error)
}

// ConnectWorkspaceServices starts whole-bundle workspace service enrollment.
func (c *Client) ConnectWorkspaceServices(ctx context.Context, sessionID string) (WorkspaceEnrollment, error) {
	response, err := c.svc.ConnectWorkspaceServices(ctx, &mecatlv1.WorkspaceEnrollmentConnectRequest{SessionId: sessionID})
	if err != nil {
		return WorkspaceEnrollment{}, fmt.Errorf("connect workspace services: %w", err)
	}
	return workspaceEnrollmentFrom(response), nil
}

// RetryWorkspaceEnrollment retries an incomplete workspace service enrollment.
func (c *Client) RetryWorkspaceEnrollment(ctx context.Context, sessionID, enrollmentID string) (WorkspaceEnrollment, error) {
	response, err := c.svc.RetryWorkspaceEnrollment(ctx, &mecatlv1.WorkspaceEnrollmentControlRequest{SessionId: sessionID, EnrollmentId: enrollmentID})
	if err != nil {
		return WorkspaceEnrollment{}, fmt.Errorf("retry workspace enrollment: %w", err)
	}
	return workspaceEnrollmentFrom(response), nil
}

// CancelWorkspaceEnrollment cancels an incomplete workspace service enrollment.
func (c *Client) CancelWorkspaceEnrollment(ctx context.Context, sessionID, enrollmentID string) (WorkspaceEnrollment, error) {
	response, err := c.svc.CancelWorkspaceEnrollment(ctx, &mecatlv1.WorkspaceEnrollmentControlRequest{SessionId: sessionID, EnrollmentId: enrollmentID})
	if err != nil {
		return WorkspaceEnrollment{}, fmt.Errorf("cancel workspace enrollment: %w", err)
	}
	return workspaceEnrollmentFrom(response), nil
}

func workspaceEnrollmentFrom(response *mecatlv1.WorkspaceEnrollment) WorkspaceEnrollment {
	if response == nil {
		return WorkspaceEnrollment{}
	}
	return WorkspaceEnrollment{
		ID: response.GetEnrollmentId(), Status: WorkspaceEnrollmentStatus(response.GetStatus()),
		RequiredServices: response.GetRequiredServices(), PresentationURL: response.GetPresentationUrl(),
	}
}
