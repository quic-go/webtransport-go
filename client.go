package webtransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

var errNoWebTransport = errors.New("webtransport: server didn't enable WebTransport")

type Dialer struct {
	// TLSClientConfig is the TLS client config used when dialing the QUIC connection.
	// It must set the h3 ALPN.
	TLSClientConfig *tls.Config

	// QUICConfig is the QUIC config used when dialing the QUIC connection.
	QUICConfig *quic.Config

	// StreamReorderingTime is the time an incoming WebTransport stream that cannot be associated
	// with a session is buffered.
	// This can happen if the response to a CONNECT request (that creates a new session) is reordered,
	// and arrives after the first WebTransport stream(s) for that session.
	// Defaults to 5 seconds.
	StreamReorderingTimeout time.Duration

	// DialAddr is the function used to dial the underlying QUIC connection.
	// If unset, quic.DialAddrEarly will be used.
	DialAddr func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error)

	// AvailableProtocols is the list of application protocols to offer during protocol negotiation.
	// If set, the client will send a WT-Available-Protocols header with the CONNECT request.
	// The protocols are listed in preference order (most preferred first).
	// RFC Section 3.3, lines 509-516.
	AvailableProtocols []string

	// CheckRedirect specifies the policy for handling redirects.
	// If CheckRedirect is not nil, the client calls it before following an
	// HTTP redirect. The arguments req and via are the upcoming request and
	// the requests made already, oldest first. If CheckRedirect returns an
	// error, the Dial operation returns both the previous Response (with its
	// Body closed) and CheckRedirect's error (wrapped in a url.Error) instead
	// of issuing the Request req. As a special case, if CheckRedirect returns
	// ErrUseLastResponse, then the most recent response is returned, with its
	// body unclosed, along with a nil error.
	//
	// If CheckRedirect is nil, the Dialer returns redirect responses to the
	// caller without following them automatically. This differs from net/http.Client
	// behavior where nil means automatic following, because the WebTransport RFC
	// (draft-ietf-webtrans-http3) requires application-level control over redirects
	// due to potential optimistic data sending.
	//
	// Use FollowRedirects(n) as a convenience helper to enable automatic redirect
	// following with a limit of n redirects (0 = unlimited).
	CheckRedirect func(req *http.Request, via []*http.Request) error

	ctx       context.Context
	ctxCancel context.CancelFunc

	initOnce sync.Once

	conns sessionManager
}

func (d *Dialer) init() {
	timeout := d.StreamReorderingTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	d.conns = *newSessionManager(timeout)
	d.ctx, d.ctxCancel = context.WithCancel(context.Background())
}

func (d *Dialer) Dial(ctx context.Context, urlStr string, reqHdr http.Header) (*http.Response, *Session, error) {
	d.initOnce.Do(func() { d.init() })

	// Technically, this is not true. DATAGRAMs could be sent using the Capsule protocol.
	// However, quic-go currently enforces QUIC datagram support if HTTP/3 datagrams are enabled.
	quicConf := d.QUICConfig
	if quicConf == nil {
		quicConf = &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		}
	} else {
		if !d.QUICConfig.EnableDatagrams {
			return nil, nil, errors.New("webtransport: DATAGRAM support required, enable it via QUICConfig.EnableDatagrams")
		}
		// Clone the config to avoid modifying the user's config
		quicConf = d.QUICConfig.Clone()
		// Enable RESET_STREAM_AT support for draft-14 reliable stream association (RFC Section 4.4)
		quicConf.EnableStreamResetPartialDelivery = true
	}

	tlsConf := d.TLSClientConfig
	if tlsConf == nil {
		tlsConf = &tls.Config{}
	} else {
		tlsConf = tlsConf.Clone()
	}
	if len(tlsConf.NextProtos) == 0 {
		tlsConf.NextProtos = []string{http3.NextProtoH3}
	}

	// Via chain for redirect tracking - persists across all redirects in this Dial call
	var via []*http.Request

	// Redirect following loop (similar to net/http.Client)
	for {
		u, err := url.Parse(urlStr)
		if err != nil {
			return nil, nil, err
		}
		if reqHdr == nil {
			reqHdr = http.Header{}
		}
		reqHdr.Set(webTransportDraftOfferHeaderKey, "1")

		// Add WT-Available-Protocols header if protocols are specified (RFC Section 3.3)
		var availableProtocols []string
		if len(d.AvailableProtocols) > 0 {
			availableProtocols = d.AvailableProtocols
			protocolsHeader, err := MarshalAvailableProtocols(d.AvailableProtocols)
			if err != nil {
				return nil, nil, fmt.Errorf("webtransport: failed to marshal protocols: %w", err)
			}
			reqHdr.Set(HeaderWTAvailableProtocols, protocolsHeader)
		}

		req := &http.Request{
			Method: http.MethodConnect,
			Header: reqHdr,
			Proto:  "webtransport",
			Host:   u.Host,
			URL:    u,
		}
		req = req.WithContext(ctx)

		dialAddr := d.DialAddr
		if dialAddr == nil {
			dialAddr = quic.DialAddrEarly
		}
		qconn, err := dialAddr(ctx, u.Host, tlsConf, quicConf)
		if err != nil {
			return nil, nil, err
		}
		tr := &http3.Transport{
			EnableDatagrams: true,
			// Send SETTINGS_WT_MAX_SESSIONS for draft-14 compliance (RFC Section 3.1).
			// Default value of 100 allows for session pooling.
			AdditionalSettings: map[uint64]uint64{
				SettingsWTMaxSessions: 100,
			},
			StreamHijacker: func(ft http3.FrameType, connTracingID quic.ConnectionTracingID, str *quic.Stream, e error) (hijacked bool, err error) {
				if isWebTransportError(e) {
					return true, nil
				}
				if ft != webTransportFrameType {
					return false, nil
				}
				id, err := quicvarint.Read(quicvarint.NewReader(str))
				if err != nil {
					if isWebTransportError(err) {
						return true, nil
					}
					return false, err
				}
				d.conns.AddStream(connTracingID, str, sessionID(id))
				return true, nil
			},
			UniStreamHijacker: func(st http3.StreamType, connTracingID quic.ConnectionTracingID, str *quic.ReceiveStream, err error) (hijacked bool) {
				if st != webTransportUniStreamType && !isWebTransportError(err) {
					return false
				}
				d.conns.AddUniStream(connTracingID, str)
				return true
			},
		}

		conn := tr.NewClientConn(qconn)

		select {
		case <-conn.ReceivedSettings():
		case <-d.ctx.Done():
			return nil, nil, context.Cause(d.ctx)
		}
		settings := conn.Settings()
		if !settings.EnableExtendedConnect {
			return nil, nil, errors.New("server didn't enable Extended CONNECT")
		}
		if !settings.EnableDatagrams {
			return nil, nil, errors.New("server didn't enable HTTP/3 datagram support")
		}

		if settings.Other == nil {
			return nil, nil, errNoWebTransport
		}
		maxSessions, ok := settings.Other[SettingsWTMaxSessions]
		if !ok || maxSessions < 1 {
			return nil, nil, errNoWebTransport
		}

		requestStr, err := conn.OpenRequestStream(ctx) // TODO: put this on the Connection (maybe introduce a ClientConnection?)
		if err != nil {
			return nil, nil, err
		}
		if err := requestStr.SendRequestHeader(req); err != nil {
			return nil, nil, err
		}
		// TODO(#136): create the session to allow optimistic opening of streams and sending of datagrams
		rsp, err := requestStr.ReadResponse()
		if err != nil {
			return nil, nil, err
		}

		// Handle redirect responses (3xx status codes)
		// Per WebTransport RFC draft-ietf-webtrans-http3 Section 3.2, the client MUST NOT
		// automatically follow redirects, as optimistic data may have already been sent for
		// the session. The application MUST explicitly opt-in to redirect following via
		// CheckRedirect to maintain application-level control over the session lifecycle.
		if isRedirectStatus(rsp.StatusCode) {
			// Validate Location header is present
			// RFC 3986 Section 4.4 defines the Location header as containing a URI-reference.
			location, err := extractLocation(rsp)
			if err != nil {
				return rsp, nil, err
			}

			// If CheckRedirect is nil, return redirect response without following
			// This is the default behavior and differs from net/http.Client
			if d.CheckRedirect == nil {
				return rsp, nil, nil
			}

			// Resolve redirect URL per RFC 3986 Section 5.3 (Component Recomposition)
			// The Location URI-reference is resolved against the CONNECT request URI.
			redirectURL, err := resolveRedirectLocation(req.URL, location)
			if err != nil {
				closeRedirectBody(rsp)
				return rsp, nil, err
			}

			// Check context before proceeding with redirect
			if ctx.Err() != nil {
				closeRedirectBody(rsp)
				return rsp, nil, ctx.Err()
			}

			// Create redirect request for CheckRedirect callback
			// draft-ietf-webtrans-http3 Section 3.2 requires application-level decision
			// on whether to follow the redirect. This allows applications to validate
			// the redirect target and control session reuse/creation.
			redirectReq := &http.Request{
				Method: http.MethodConnect,
				Header: reqHdr.Clone(),
				Proto:  "webtransport",
				Host:   redirectURL.Host,
				URL:    redirectURL,
			}
			redirectReq = redirectReq.WithContext(ctx)

			// Invoke CheckRedirect callback with a copy of via chain to prevent modification.
			// The via chain is provided per draft-ietf-webtrans-http3 for applications to
			// implement redirect loop detection and audit redirect chains.
			viaCopy := make([]*http.Request, len(via))
			copy(viaCopy, via)
			err = d.CheckRedirect(redirectReq, viaCopy)
			if err != nil {
				// Handle ErrUseLastResponse special case
				if errors.Is(err, ErrUseLastResponse) {
					// Return redirect response without closing body
					return rsp, nil, nil
				}
				// For other errors, close body and return error with response
				closeRedirectBody(rsp)
				return rsp, nil, err
			}

			// CheckRedirect returned nil, follow the redirect
			closeRedirectBody(rsp)

			// Add current request to via chain (AFTER CheckRedirect call)
			via = append(via, req)

			// Update urlStr for next iteration of loop
			urlStr = redirectURL.String()
			continue // Follow the redirect
		}

		// Not a redirect - check for other error status codes
		if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
			return rsp, nil, fmt.Errorf("webtransport: received status %d", rsp.StatusCode)
		}

		// Success! Parse WT-Protocol header from response (RFC Section 3.3)
		var negotiatedProtocol string
		if protocolHeader := rsp.Header.Get(HeaderWTProtocol); protocolHeader != "" {
			negotiatedProtocol, err = ParseProtocol(protocolHeader)
			if err != nil {
				return rsp, nil, fmt.Errorf("webtransport: failed to parse WT-Protocol header: %w", err)
			}

			// Validate that the selected protocol was in our available list (RFC Section 3.3, lines 525-527)
			if len(availableProtocols) > 0 {
				if err := ValidateSelectedProtocol(negotiatedProtocol, availableProtocols); err != nil {
					return rsp, nil, err
				}
			}
		}

		sess := d.conns.AddSession(conn.Conn(), sessionID(requestStr.StreamID()), requestStr, maxSessions)
		sess.setNegotiatedProtocol(negotiatedProtocol)
		return rsp, sess, nil
	}
}

func (d *Dialer) Close() error {
	d.ctxCancel()
	return nil
}
