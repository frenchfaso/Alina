package alina

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func newHTTPClient() *http.Client {
	return &http.Client{Transport: hostHTTPTransport(), Timeout: 180 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}

type remoteHTTPError struct{ Status int }

func (e *remoteHTTPError) Error() string {
	return fmt.Sprintf("remote service returned HTTP %d", e.Status)
}

func requestJSON(ctx context.Context, client *http.Client, method, url string, body any, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, url, reader)
	if e != nil {
		return e
	}
	req.Header.Set("User-Agent", "alina/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("network request failed (%T)", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &remoteHTTPError{Status: resp.StatusCode}
	}
	return decodeLimited(resp.Body, out)
}
func decodeLimited(r io.Reader, out any) error {
	b, e := io.ReadAll(io.LimitReader(r, 8<<20+1))
	if e != nil {
		return e
	}
	if len(b) > 8<<20 {
		return fmt.Errorf("response exceeds 8 MiB")
	}
	return json.Unmarshal(b, out)
}
