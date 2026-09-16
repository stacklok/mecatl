package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"

	"github.com/stacklok/mecatl/engine/tool"
)

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
			} else {
				err = encodeErr
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
			encoded, encodeErr := tool.EncodeFileVersion(version)
			if encodeErr != nil {
				err = encodeErr
			} else {
				response.Version, response.VersionValid = encoded, true
			}
		}
	case "replace":
		if !request.VersionValid {
			err = &tool.VersionMismatchError{Path: request.Path}
			break
		}
		version, replaceErr := ws.ReplaceFile(ctx, request.Path, tool.NewFileVersion(request.Version), request.Data)
		err = replaceErr
		if err == nil {
			encoded, encodeErr := tool.EncodeFileVersion(version)
			if encodeErr != nil {
				err = encodeErr
			} else {
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
