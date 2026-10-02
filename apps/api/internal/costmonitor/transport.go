package costmonitor

import "net/http"

// countingTransport wraps an http.RoundTripper to record one call per
// request against (category, provider), with no effect on the request
// itself: WrapTransport is the only way to construct one, and it returns
// the untouched base transport whenever tracker is nil — so wiring this
// into a client that has no Tracker configured is always a no-op, never a
// new failure mode.
type countingTransport struct {
	tracker  *Tracker
	category string
	provider string
	base     http.RoundTripper
}

// WrapTransport returns an http.RoundTripper that records one call per
// request to tracker under (category, provider) before delegating to base,
// so an existing http.Client can be instrumented by setting
// `client.Transport = costmonitor.WrapTransport(tracker, category, provider, client.Transport)`
// with no changes at any of that client's call sites.
//
// Recording is fire-and-forget (see Tracker.RecordCallAsync): a slow or
// unavailable Redis never adds latency to, or fails, the wrapped request —
// including mainnet transaction submission, which must not gain a new
// failure mode from a monitoring feature.
func WrapTransport(tracker *Tracker, category, provider string, base http.RoundTripper) http.RoundTripper {
	if tracker == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &countingTransport{tracker: tracker, category: category, provider: provider, base: base}
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.tracker.RecordCallAsync(c.category, c.provider)
	return c.base.RoundTrip(req)
}

// ProviderFunc derives a per-request provider label from the outgoing
// request — for a client like stellar.ContractInvoker that calls more than
// one real provider (Soroban RPC and Horizon) through a single http.Client,
// so each is budgeted separately instead of being counted together under
// one label. See WrapTransportFunc.
type ProviderFunc func(*http.Request) string

type dynamicCountingTransport struct {
	tracker    *Tracker
	category   string
	providerFn ProviderFunc
	base       http.RoundTripper
}

// WrapTransportFunc is WrapTransport with a per-request provider label
// instead of one fixed for the whole client.
func WrapTransportFunc(tracker *Tracker, category string, providerFn ProviderFunc, base http.RoundTripper) http.RoundTripper {
	if tracker == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &dynamicCountingTransport{tracker: tracker, category: category, providerFn: providerFn, base: base}
}

func (d *dynamicCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.tracker.RecordCallAsync(d.category, d.providerFn(req))
	return d.base.RoundTrip(req)
}
