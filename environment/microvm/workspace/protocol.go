package workspace

import (
	"errors"
	"io/fs"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

const (
	guestRoot      = "/workspace"
	maxContentSize = 512 << 10
	maxResults     = 200
)

type operation string

const (
	opRead    operation = "read"
	opStat    operation = "stat"
	opCreate  operation = "create"
	opReplace operation = "replace"
	opGlob    operation = "glob"
	opGrep    operation = "grep"
)

type request struct {
	Operation    operation `json:"operation"`
	Path         string    `json:"path,omitempty"`
	Pattern      string    `json:"pattern,omitempty"`
	PathGlob     string    `json:"path_glob,omitempty"`
	Data         []byte    `json:"data,omitempty"`
	Version      string    `json:"version,omitempty"`
	VersionValid bool      `json:"version_valid,omitempty"`
}

type response struct {
	Data         []byte           `json:"data,omitempty"`
	Version      string           `json:"version,omitempty"`
	VersionValid bool             `json:"version_valid,omitempty"`
	Info         *wireFileInfo    `json:"info,omitempty"`
	Paths        []string         `json:"paths,omitempty"`
	Matches      []tool.GrepMatch `json:"matches,omitempty"`
	ErrorCode    string           `json:"error_code,omitempty"`
}

type wireFileInfo struct {
	Name    string      `json:"name"`
	Size    int64       `json:"size"`
	Mode    fs.FileMode `json:"mode"`
	ModTime time.Time   `json:"mod_time"`
	IsDir   bool        `json:"is_dir"`
}

func errorResponse(err error) response {
	code := "internal"
	var mismatch *tool.VersionMismatchError
	switch {
	case errors.As(err, &mismatch):
		code = "version_mismatch"
	case errors.Is(err, fs.ErrExist):
		code = "exists"
	case errors.Is(err, fs.ErrNotExist):
		code = "not_found"
	case errors.Is(err, errPathEscape):
		code = "path_escape"
	case errors.Is(err, errResultBound):
		code = "result_bound"
	}
	return response{ErrorCode: code}
}
