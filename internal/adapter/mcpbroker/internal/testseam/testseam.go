// Package testseam holds what only tests may set on the MCP broker; see
// mcpbroker.TestSeam.
package testseam

import "time"

// Seam is what only tests may set on the MCP broker.
type Seam struct {
	// GatewayMaxCallDuration, when positive, replaces the vMCP's maximum call
	// duration, and with it the grace after a gateway token's exp, so a test
	// can outlive both.
	GatewayMaxCallDuration time.Duration
	// SignInClient, when set and AS-2 is set up, is registered in AS-2's
	// store at construction, so a test can sign users in at an AS-2 whose
	// store it cannot reach, such as one composition builds.
	SignInClient *Client
}

// Client is a public client at AS-2 for its vMCP resource, signing users in
// with the authorization code grant and PKCE, scope openid.
type Client struct {
	// ID is the client ID, and RedirectURI its only redirect URI.
	ID, RedirectURI string
}
