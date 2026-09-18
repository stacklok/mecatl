package boxenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

type apiClient struct {
	base   *url.URL
	apiKey string
	http   *http.Client
}

type boxInfo struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type createBoxRequest struct {
	TTLSeconds int  `json:"ttlSeconds"`
	NoEnv      bool `json:"noEnv"`
}

type commandRequest struct {
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type commandResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("box API: status %d (%s): %s", e.status, e.code, e.message)
	}
	return fmt.Sprintf("box API: status %d: %s", e.status, e.message)
}

func isStatus(err error, status int) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.status == status
}

func (c *apiClient) createBox(ctx context.Context, in createBoxRequest) (boxInfo, error) {
	var out struct {
		Box boxInfo `json:"box"`
	}
	if err := c.request(ctx, http.MethodPost, "/boxes", nil, in, &out); err != nil {
		return boxInfo{}, err
	}
	if out.Box.ID == "" {
		return boxInfo{}, errors.New("box API: create returned empty box id")
	}
	return out.Box, nil
}

func (c *apiClient) getBox(ctx context.Context, id string) (boxInfo, error) {
	var out struct {
		Box boxInfo `json:"box"`
	}
	if err := c.request(ctx, http.MethodGet, "/boxes/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return boxInfo{}, err
	}
	if out.Box.ID == "" {
		out.Box.ID = id
	}
	return out.Box, nil
}

func (c *apiClient) stopBox(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodPost, "/boxes/"+url.PathEscape(id)+"/stop", nil, struct{}{}, nil)
}

func (c *apiClient) resumeBox(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodPost, "/boxes/"+url.PathEscape(id)+"/resume", nil, struct{}{}, nil)
}

func (c *apiClient) command(ctx context.Context, id string, in commandRequest) (commandResult, error) {
	var out struct {
		Result   *commandResult `json:"result"`
		ExitCode int            `json:"exitCode"`
		Stdout   string         `json:"stdout"`
		Stderr   string         `json:"stderr"`
	}
	if err := c.request(ctx, http.MethodPost, "/boxes/"+url.PathEscape(id)+"/commands", nil, in, &out); err != nil {
		return commandResult{}, err
	}
	if out.Result != nil {
		return *out.Result, nil
	}
	return commandResult{ExitCode: out.ExitCode, Stdout: out.Stdout, Stderr: out.Stderr}, nil
}

func (c *apiClient) readFile(ctx context.Context, id, filePath string) ([]byte, error) {
	query := url.Values{"path": {filePath}, "encoding": {"base64"}}
	var out struct {
		Content *string `json:"content"`
		File    *struct {
			Content string `json:"content"`
		} `json:"file"`
	}
	if err := c.request(ctx, http.MethodGet, "/boxes/"+url.PathEscape(id)+"/files", query, nil, &out); err != nil {
		if isMissingFileError(err) {
			return nil, &fs.PathError{Op: "read", Path: filePath, Err: fs.ErrNotExist}
		}
		return nil, err
	}

	var encoded string
	switch {
	case out.Content != nil:
		encoded = *out.Content
	case out.File != nil:
		encoded = out.File.Content
	default:
		return nil, errors.New("box API: file response missing content")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("box API: decode file content: %w", err)
	}
	return data, nil
}

func isMissingFileError(err error) bool {
	if isStatus(err, http.StatusNotFound) {
		return true
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	return strings.EqualFold(apiErr.code, "enoent") ||
		strings.Contains(strings.ToLower(apiErr.message), "enoent")
}

func (c *apiClient) writeFile(ctx context.Context, id, filePath string, data []byte) error {
	body := struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{Path: filePath, Content: base64.StdEncoding.EncodeToString(data), Encoding: "base64"}
	return c.request(ctx, http.MethodPut, "/boxes/"+url.PathEscape(id)+"/files", nil, body, nil)
}

func (c *apiClient) request(ctx context.Context, method, endpoint string, query url.Values, body any, out any) error {
	u := *c.base
	u.Path = strings.TrimSuffix(c.base.Path, "/") + endpoint
	u.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return err
	}
	if len(payload) > maxResponseBody {
		return errors.New("box API: response exceeds limit")
	}
	var apiBody struct {
		OK      *bool  `json:"ok"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &apiBody)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (apiBody.OK != nil && !*apiBody.OK) {
		if apiBody.Error != nil {
			if apiBody.Code == "" {
				apiBody.Code = apiBody.Error.Code
			}
			if apiBody.Message == "" {
				apiBody.Message = apiBody.Error.Message
			}
		}
		return &apiError{status: resp.StatusCode, code: bounded(apiBody.Code, 64), message: bounded(apiBody.Message, 256)}
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("box API: decode response: %w", err)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func bounded(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
