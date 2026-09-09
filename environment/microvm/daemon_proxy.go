package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

type servicesRuntime interface {
	Services(EnvironmentRef) (*guestagent.Services, error)
}

type proxyWorkspaceRequest struct {
	Operation    string `json:"operation"`
	Path         string `json:"path,omitempty"`
	Pattern      string `json:"pattern,omitempty"`
	PathGlob     string `json:"path_glob,omitempty"`
	Data         []byte `json:"data,omitempty"`
	Version      string `json:"version,omitempty"`
	VersionValid bool   `json:"version_valid,omitempty"`
}
type proxyWorkspaceResponse struct {
	Data         []byte           `json:"data,omitempty"`
	Version      string           `json:"version,omitempty"`
	VersionValid bool             `json:"version_valid,omitempty"`
	Info         *tool.FileInfo   `json:"info,omitempty"`
	Paths        []string         `json:"paths,omitempty"`
	Matches      []tool.GrepMatch `json:"matches,omitempty"`
	ErrorCode    string           `json:"error_code,omitempty"`
}

func (d *Daemon) proxyExecStream(ctx context.Context, request LifecycleRequest, record EnvironmentRecord, send func(LifecycleExecStream) error) (LifecycleResponse, error) {
	runtime, ok := d.runtime.(servicesRuntime)
	if !ok {
		return LifecycleResponse{}, ErrEnvironmentUnavailable
	}
	services, err := runtime.Services(record.Ref)
	if err != nil {
		return LifecycleResponse{}, err
	}
	var input struct {
		Command        string              `json:"command"`
		TemporaryScope tool.TemporaryScope `json:"temporary_scope,omitempty"`
	}
	if json.Unmarshal(request.Payload, &input) != nil || input.Command == "" {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	var execLease *AdmissionLease
	if d.admission != nil {
		execLease, err = d.admission.AcquireContext(ctx, record.Owner, ResourceUsage{Execs: 1})
		if err != nil {
			if d.observer != nil {
				d.observer.QuotaRejected(QuotaExecs)
			}
			return LifecycleResponse{}, err
		}
		defer execLease.Release()
	}
	exit, err := services.Runner.RunFramesWithTemporaryScope(ctx, input.Command, input.TemporaryScope, func(frame guestexec.OutputFrame) error {
		return send(LifecycleExecStream{Channel: frame.Channel, Data: frame.Data})
	})
	if d.observer != nil {
		outcome := OutcomeSuccess
		if err != nil {
			outcome = OutcomeFailure
		}
		d.observer.ExecFinished(outcome, input.Command)
	}
	if err != nil {
		return LifecycleResponse{}, err
	}
	payload, err := json.Marshal(struct {
		ExitCode int `json:"exit_code"`
	}{ExitCode: exit})
	if err != nil {
		return LifecycleResponse{}, err
	}
	return LifecycleResponse{Binding: bindingForRecord(record), Record: recordPointer(record), Payload: payload}, nil
}

func (d *Daemon) proxy(ctx context.Context, request LifecycleRequest, record EnvironmentRecord) (LifecycleResponse, error) {
	runtime, ok := d.runtime.(servicesRuntime)
	if !ok {
		return LifecycleResponse{}, ErrEnvironmentUnavailable
	}
	services, err := runtime.Services(record.Ref)
	if err != nil {
		return LifecycleResponse{}, err
	}
	var payload any
	switch request.Operation {
	case LifecycleWorkspace:
		payload, err = proxyWorkspace(ctx, services.Workspace, request.Payload)
	case LifecycleExec:
		var input struct {
			Command        string              `json:"command"`
			TemporaryScope tool.TemporaryScope `json:"temporary_scope,omitempty"`
		}
		if json.Unmarshal(request.Payload, &input) != nil || input.Command == "" {
			return LifecycleResponse{}, errLifecycleProtocol
		}
		var execLease *AdmissionLease
		if d.admission != nil {
			execLease, err = d.admission.AcquireContext(ctx, record.Owner, ResourceUsage{Execs: 1})
			if err != nil {
				if d.observer != nil {
					d.observer.QuotaRejected(QuotaExecs)
				}
				return LifecycleResponse{}, err
			}
			defer execLease.Release()
		}
		var result tool.CommandResult
		result, err = services.Runner.RunWithTemporaryScope(ctx, input.Command, input.TemporaryScope)
		if d.observer != nil {
			outcome := OutcomeSuccess
			if err != nil {
				outcome = OutcomeFailure
			}
			d.observer.ExecFinished(outcome, input.Command)
		}
		payload = struct {
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exit_code"`
		}{result.Stdout, result.Stderr, result.ExitCode}
	}
	if err != nil {
		return LifecycleResponse{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return LifecycleResponse{}, err
	}
	return LifecycleResponse{Binding: bindingForRecord(record), Record: recordPointer(record), Payload: encoded}, nil
}

func proxyWorkspace(ctx context.Context, ws tool.Workspace, raw json.RawMessage) (proxyWorkspaceResponse, error) {
	var request proxyWorkspaceRequest
	if json.Unmarshal(raw, &request) != nil {
		return proxyWorkspaceResponse{}, errLifecycleProtocol
	}
	response := proxyWorkspaceResponse{}
	var err error
	switch request.Operation {
	case "read":
		var version tool.FileVersion
		response.Data, version, err = ws.ReadVersion(ctx, request.Path)
		if err == nil {
			if encoded, encodeErr := tool.EncodeFileVersion(version); encodeErr == nil {
				response.Version, response.VersionValid = encoded, true
			}
		}
	case "stat":
		info, statErr := ws.Stat(ctx, request.Path)
		err = statErr
		if err == nil {
			response.Info = &info
		}
	case "create":
		version, createErr := ws.CreateFile(ctx, request.Path, request.Data)
		err = createErr
		if err == nil {
			if encoded, encodeErr := tool.EncodeFileVersion(version); encodeErr == nil {
				response.Version, response.VersionValid = encoded, true
			}
		}
	case "replace":
		version, replaceErr := ws.ReplaceFile(ctx, request.Path, tool.NewFileVersion(request.Version), request.Data)
		err = replaceErr
		if !request.VersionValid {
			err = &tool.VersionMismatchError{Path: request.Path}
		}
		if err == nil {
			if encoded, encodeErr := tool.EncodeFileVersion(version); encodeErr == nil {
				response.Version, response.VersionValid = encoded, true
			}
		}
	case "glob":
		response.Paths, err = ws.Glob(ctx, request.Pattern)
	case "grep":
		response.Matches, err = ws.Grep(ctx, request.Pattern, request.PathGlob)
	default:
		return response, errLifecycleProtocol
	}
	if err != nil {
		response.ErrorCode = proxyWorkspaceError(err)
	}
	return response, nil
}

func proxyWorkspaceError(err error) string {
	var mismatch *tool.VersionMismatchError
	switch {
	case errors.As(err, &mismatch):
		return "version_mismatch"
	case errors.Is(err, fs.ErrExist):
		return "exists"
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	default:
		return "internal"
	}
}
