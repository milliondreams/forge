package oauth

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
)

// resourceParam is the RFC 8707 resource indicator parameter name. It names the
// protected resource a token is requested for, so the authorization server can
// restrict the token's audience to it.
const resourceParam = "resource"

// resourceParamOption sends resource as the RFC 8707 resource indicator on an
// authorization or token request.
func resourceParamOption(resource string) oauth2.AuthCodeOption {
	return oauth2.SetAuthURLParam(resourceParam, resource)
}

// resourceParamClient returns an HTTP client that adds the RFC 8707 resource
// indicator to the refresh request. Refresh is the one request x/oauth2 builds
// internally — Config.TokenSource takes no oauth2.AuthCodeOption, unlike
// AuthCodeURL and Exchange — so the parameter can only be added on the way out.
//
// The client is handed to nothing else, so the transport assumes what a refresh
// request is: a form-encoded POST to the token endpoint.
func (m *Manager) resourceParamClient(resource string) *http.Client {
	return &http.Client{
		Timeout:   m.httpClient.Timeout,
		Transport: &resourceParamTransport{base: m.httpClient.Transport, resource: resource},
	}
}

type resourceParamTransport struct {
	base     http.RoundTripper // nil means http.DefaultTransport
	resource string
}

func (t *resourceParamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if req.Body == nil {
		return base.RoundTrip(req)
	}

	body, err := io.ReadAll(req.Body)
	req.Body.Close() //nolint:errcheck // the body is fully consumed above
	if err != nil {
		return nil, fmt.Errorf("reading token request body: %w", err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("parsing token request body: %w", err)
	}
	form.Set(resourceParam, t.resource)
	encoded := []byte(form.Encode())

	// RoundTrip must leave the caller's request alone.
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(encoded))
	clone.ContentLength = int64(len(encoded))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}
	return base.RoundTrip(clone)
}
