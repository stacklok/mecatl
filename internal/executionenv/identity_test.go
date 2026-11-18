package executionenv

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"
)

func TestClientIdentityRejectsCNAndAmbiguousURIs(t *testing.T) {
	cn := &x509.Certificate{Subject: pkix.Name{CommonName: "trusted"}}
	if _, err := ClientIdentity(cn); err == nil {
		t.Fatal("CN accepted")
	}
	u1, _ := url.Parse("spiffe://b/x")
	u2, _ := url.Parse("spiffe://a/x")
	if _, err := ClientIdentity(&x509.Certificate{URIs: []*url.URL{u1, u2}}); err == nil {
		t.Fatal("ambiguous URI SANs accepted")
	}
	mixed, _ := url.Parse("SPIFFE://Mecatl.Test/Provider")
	id, err := ClientIdentity(&x509.Certificate{URIs: []*url.URL{mixed}})
	if err != nil || id != "spiffe://mecatl.test/Provider" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
}
