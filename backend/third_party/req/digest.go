package req

import (
	"bytes"
	"io"
	"net/http"
	"sync"

	"github.com/icholy/digest"
	"github.com/imroc/req/v3/internal/header"
)

// cchal is a cached challenge and the number of times it's been used.
type cchal struct {
	c *digest.Challenge
	n int
}

type digestAuth struct {
	Username   string
	Password   string
	HttpClient *http.Client
	cache      map[string]*cchal
	cacheMu    sync.Mutex
}

func (da *digestAuth) digest(req *http.Request, chal *digest.Challenge, count int) (*digest.Credentials, error) {
	opt := digest.Options{
		Method:   req.Method,
		URI:      req.URL.RequestURI(),
		GetBody:  req.GetBody,
		Count:    count,
		Username: da.Username,
		Password: da.Password,
	}
	return digest.Digest(chal, opt)
}

// challenge returns a cached challenge and count for the provided request
func (da *digestAuth) challenge(req *http.Request) (*digest.Challenge, int, bool) {
	da.cacheMu.Lock()
	defer da.cacheMu.Unlock()
	host := req.URL.Hostname()
	cc, ok := da.cache[host]
	if !ok {
		return nil, 0, false
	}
	cc.n++
	return cc.c, cc.n, true
}

// prepare attempts to find a cached challenge that matches the
// requested domain, and use it to set the Authorization header
func (da *digestAuth) prepare(req *http.Request) error {
	// add cookies
	if da.HttpClient.Jar != nil {
		for _, cookie := range da.HttpClient.Jar.Cookies(req.URL) {
			req.AddCookie(cookie)
		}
	}
	// add auth
	chal, count, ok := da.challenge(req)
	if !ok {
		return nil
	}
	cred, err := da.digest(req, chal, count)
	if err != nil {
		return err
	}
	if cred != nil {
		req.Header.Set("Authorization", cred.String())
	}
	return nil
}

func (da *digestAuth) HttpRoundTripWrapperFunc(rt http.RoundTripper) HttpRoundTripFunc {
	return func(req *http.Request) (resp *http.Response, err error) {
		// Replay factories supply the bodies sent to the transport. The
		// original remains ours, including when cloning or preparation fails.
		if req.GetBody != nil && req.Body != nil {
			defer req.Body.Close()
		}
		clone, err := cloner(req)
		if err != nil {
			return nil, err
		}

		// make a copy of the request
		first, err := clone()
		if err != nil {
			return nil, err
		}

		// prepare the first request using a cached challenge
		if err := da.prepare(first); err != nil {
			if first.Body != nil {
				_ = first.Body.Close()
			}
			return nil, err
		}

		// the first request will either succeed or return a 401
		res, err := rt.RoundTrip(first)
		if err != nil || res.StatusCode != http.StatusUnauthorized {
			return res, err
		}

		// find and cache the challenge
		host := req.URL.Hostname()
		chal, err := digest.FindChallenge(res.Header)
		if err != nil {
			// existing cached challenge didn't work, so remove it
			da.cacheMu.Lock()
			delete(da.cache, host)
			da.cacheMu.Unlock()
			if err == digest.ErrNoChallenge {
				// No retry consumes this response; its body belongs to the caller.
				return res, nil
			}
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			return nil, err
		} else {
			// found new challenge, so cache it
			da.cacheMu.Lock()
			da.cache[host] = &cchal{c: chal}
			da.cacheMu.Unlock()
		}

		// The usable challenge response is consumed internally for replay.
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()

		// make a second copy of the request
		second, err := clone()
		if err != nil {
			return nil, err
		}

		// prepare the second request based on the new challenge
		if err := da.prepare(second); err != nil {
			if second.Body != nil {
				_ = second.Body.Close()
			}
			return nil, err
		}

		return rt.RoundTrip(second)
	}
}

// create response middleware for http digest authentication.
func handleDigestAuthFunc(username, password string) ResponseMiddleware {
	return func(client *Client, resp *Response) error {
		if resp.Err != nil || resp.StatusCode != http.StatusUnauthorized {
			return nil
		}
		auth, err := createDigestAuth(resp.Request.RawRequest, resp.Response, username, password)
		if err != nil {
			return err
		}
		r := resp.Request
		req := *r.RawRequest
		if req.Body != nil {
			err = parseRequestBody(client, r) // re-setup body
			if err != nil {
				return err
			}
			if r.GetBody != nil {
				body, err := r.GetBody()
				if err != nil {
					return err
				}
				req.Body = body
				req.GetBody = r.GetBody
			}
		}
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set(header.Authorization, auth)
		resp.Response, err = client.httpClient.Do(&req)
		return err
	}
}

func createDigestAuth(req *http.Request, resp *http.Response, username, password string) (auth string, err error) {
	chal, err := digest.FindChallenge(resp.Header)
	if err != nil {
		return "", err
	}
	cred, err := digest.Digest(chal, digest.Options{
		Username: username,
		Password: password,
		Method:   req.Method,
		URI:      req.URL.RequestURI(),
		GetBody:  req.GetBody,
		Count:    1,
	})
	if err != nil {
		return "", err
	}
	return cred.String(), nil
}

// cloner returns a function which makes clones of the provided request
func cloner(req *http.Request) (func() (*http.Request, error), error) {
	getbody := req.GetBody
	if getbody != nil {
		factory := getbody
		getbody = func() (io.ReadCloser, error) {
			body, err := factory()
			if err != nil && body != nil {
				_ = body.Close()
				return nil, err
			}
			return body, err
		}
	}
	// if there's no GetBody function set we have to copy the body
	// into memory to use for future clones
	if getbody == nil {
		if req.Body == nil || req.Body == http.NoBody {
			getbody = func() (io.ReadCloser, error) {
				return http.NoBody, nil
			}
		} else {
			body, err := io.ReadAll(req.Body)
			closeErr := req.Body.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			getbody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			}
		}
	}
	return func() (*http.Request, error) {
		clone := req.Clone(req.Context())
		body, err := getbody()
		if err != nil {
			return nil, err
		}
		clone.Body = body
		clone.GetBody = getbody
		return clone, nil
	}, nil
}
