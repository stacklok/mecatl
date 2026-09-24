package mcp

import (
	"context"
	"errors"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// Resource is the adapter's value-object view of a single resource advertised by
// a remote MCP server. It is deliberately a plain struct (no SDK types) so the
// rest of the harness — and a later gRPC stage — can consume it without taking a
// dependency on the MCP SDK. Server records which connected server owns it.
//
// ReadOnly is always true: a resource is a read; exposing one never mutates the
// server. The field exists so the resource-reading tool can advertise ReadOnly()
// uniformly and so callers do not special-case the concept.
type Resource struct {
	Server      string
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	Size        int64
	ReadOnly    bool
}

// ResourceContents is the adapter's view of one chunk of a read resource's body.
// Text holds UTF-8 textual content; Blob holds raw binary bytes. Exactly one is
// normally populated per chunk by a well-behaved server. The harness never
// base64-dumps Blob into the model context — readResource summarizes binary
// chunks instead (see flattenResourceContents).
type ResourceContents struct {
	URI      string
	MIMEType string
	Text     string
	Blob     []byte
}

// resourceFromSDK translates an SDK *Resource into the adapter value object,
// tagging it with the owning server name. A nil input yields the zero value.
func resourceFromSDK(server string, r *mcpsdk.Resource) Resource {
	if r == nil {
		return Resource{Server: server, ReadOnly: true}
	}
	return Resource{
		Server:      server,
		URI:         r.URI,
		Name:        r.Name,
		Title:       r.Title,
		Description: r.Description,
		MIMEType:    r.MIMEType,
		Size:        r.Size,
		ReadOnly:    true,
	}
}

// resourceContentsFromSDK translates an SDK *ResourceContents chunk into the
// adapter value object. A nil input yields the zero value.
func resourceContentsFromSDK(c *mcpsdk.ResourceContents) ResourceContents {
	if c == nil {
		return ResourceContents{}
	}
	return ResourceContents{
		URI:      c.URI,
		MIMEType: c.MIMEType,
		Text:     c.Text,
		Blob:     c.Blob,
	}
}

// flattenResourceContents renders the chunks of a read resource into a single
// model-facing string. Text chunks are included verbatim; binary (Blob) chunks
// are summarized as "[binary resource: <mime>, <n> bytes]" — never base64-dumped
// — mirroring tool.go's non-text content summarization. The whole result is
// truncated to toolkit.MaxOutputBytes so a large resource cannot blow the
// context budget.
func flattenResourceContents(chunks []ResourceContents) string {
	var out string
	for i, c := range chunks {
		if i > 0 {
			out += "\n"
		}
		switch {
		case len(c.Blob) > 0:
			mime := c.MIMEType
			if mime == "" {
				mime = "application/octet-stream"
			}
			out += fmt.Sprintf("[binary resource: %s, %d bytes]", mime, len(c.Blob))
		default:
			out += c.Text
		}
	}
	return toolkit.Truncate(out, toolkit.MaxOutputBytes)
}

// listResources pages through the server's resources and returns them as adapter
// value objects. It is a thin wrapper over the SDK list call; a nil session is a
// programming error (Connect always sets one).
func (s *Server) listResources(ctx context.Context) ([]Resource, error) {
	var out []Resource
	for r, err := range s.session.Resources(ctx, nil) {
		if err != nil {
			return nil, err
		}
		out = append(out, resourceFromSDK(s.name, r))
	}
	return out, nil
}

func (s *Server) listResourcesBounded(ctx context.Context, sess *mcpsdk.ClientSession, budget *CandidateListBudget) ([]Resource, error) {
	var out []Resource
	cursor := ""
	seen := make(map[string]struct{})
	for {
		page, err := sess.ListResources(ctx, &mcpsdk.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if page == nil {
			return nil, errors.New("mcp: nil resources list page")
		}
		if err := budget.consumePage(len(page.Resources)); err != nil {
			return nil, err
		}
		for _, resource := range page.Resources {
			out = append(out, resourceFromSDK(s.name, resource))
		}
		if page.NextCursor == "" {
			return out, nil
		}
		if _, duplicate := seen[page.NextCursor]; duplicate {
			return nil, errors.New("mcp: resources list cursor cycle")
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

// readResource fetches a single resource by URI and returns its (translated)
// content chunks. The caller decides how to render them (flattenResourceContents
// for the model-facing tool). The ReadResource call rides through withSession so
// a dropped session is re-established transparently (one bounded reconnect).
func (s *Server) readResource(ctx context.Context, uri string) ([]ResourceContents, error) {
	var res *mcpsdk.ReadResourceResult
	err := s.withSession(ctx, func(sess *mcpsdk.ClientSession) error {
		var err error
		res, err = sess.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: uri})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]ResourceContents, 0, len(res.Contents))
	for _, c := range res.Contents {
		out = append(out, resourceContentsFromSDK(c))
	}
	return out, nil
}
