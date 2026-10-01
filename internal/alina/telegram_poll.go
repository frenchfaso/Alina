package alina

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
)

// Long polls own their connection pool. Recovering a mobile connection must
// never reset Telegram delivery/typing or a provider request sharing the client.
type telegramPoll struct {
	client          *http.Client
	transport       *http.Transport
	failures        int
	networkFailures int
}

func newTelegramPoll(base *http.Client) *telegramPoll {
	client := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	p := &telegramPoll{client: &client}
	if original, ok := transport.(*http.Transport); ok {
		p.transport = original.Clone()
		transport = p.transport
	}
	// Preserve non-clonable injected transports; never replace one with a real
	// network client or close its shared connections.
	client.Transport = transport
	return p
}

func (p *telegramPoll) failed(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	p.failures++
	if !telegramPollNetworkError(err) {
		p.networkFailures = 0
		return false
	}
	p.networkFailures++
	if p.networkFailures < 2 {
		return false
	}
	p.networkFailures = 0
	if p.transport == nil {
		return false
	}
	previous := p.transport
	p.transport = previous.Clone()
	p.client.Transport = p.transport
	previous.CloseIdleConnections()
	return true
}

func (p *telegramPoll) succeeded() { p.failures, p.networkFailures = 0, 0 }

func (p *telegramPoll) close() {
	if p.transport != nil {
		p.transport.CloseIdleConnections()
	}
}

func telegramPollNetworkError(err error) bool {
	var api *telegramAPIError
	var status *remoteHTTPError
	if errors.As(err, &api) || errors.As(err, &status) {
		return false
	}
	var failure *networkFailure
	var network net.Error
	return errors.As(err, &failure) || errors.As(err, &network) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF)
}
