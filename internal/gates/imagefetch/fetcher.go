package imagefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
)

var errBlockedAddress = errors.New("image URL resolves to a non-public address")

// Fetcher downloads images from public https URLs for the MCP attach_image tool.
// Every connection (redirects too) is checked at dial time, so a URL cannot reach
// loopback, private, link-local or other non-public addresses of the server's network.
type Fetcher struct {
	client *http.Client
}

var _ draftDomain.RemoteImageFetcher = (*Fetcher)(nil)

func New(timeout time.Duration) *Fetcher {
	return newFetcher(timeout, publicAddressOnly)
}

func newFetcher(timeout time.Duration, control func(network, address string, c syscall.RawConn) error) *Fetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: control}
	transport := &http.Transport{DialContext: dialer.DialContext, Proxy: nil, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: timeout, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}

			if req.URL.Scheme != "https" {
				return errors.New("redirect to a non-https URL")
			}

			return nil
		}}
	return &Fetcher{client: client}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("image_url must be an https URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "image/*")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return nil, errBlockedAddress
		}

		return nil, errors.New("image download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download failed: status %d", resp.StatusCode)
	}

	if resp.ContentLength > maxBytes {
		return nil, draftDomain.ErrImageTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, errors.New("image download failed")
	}

	if int64(len(data)) > maxBytes {
		return nil, draftDomain.ErrImageTooLarge
	}

	return data, nil
}

func publicAddressOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errBlockedAddress
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		isSharedOrReserved(ip) {
		return errBlockedAddress
	}

	return nil
}

// isSharedOrReserved covers ranges Go's helpers do not: "this network" (0.0.0.0/8), carrier-grade NAT
// (100.64.0.0/10), the IPv4 benchmark, documentation and reserved blocks, and IPv6 ranges that can carry
// an IPv4 address or are not publicly routed (NAT64, 6to4, Teredo, site-local, discard, documentation).
func isSharedOrReserved(ip net.IP) bool {
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "2002::/16", "fec0::/10"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return true
		}
	}

	return false
}
