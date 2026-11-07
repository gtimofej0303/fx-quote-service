package frankfurter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	quoteservice "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

const (
	// BaseURL is the public Frankfurter v2 API origin.
	BaseURL          = "https://api.frankfurter.dev"
	requestTimeout   = 8 * time.Second
	maxResponseBytes = 32 * 1024
)

// Client implements the existing quote.RateProvider interface.
type Client struct {
	baseURL string
	http    *http.Client
	now     func() time.Time
}

var _ quoteservice.RateProvider = (*Client)(nil)

// New returns the production client. The API requires no authentication.
func New() *Client {
	c, _ := NewWithBaseURL(BaseURL, nil) // The constant BaseURL is always valid.

	return c
}

// NewWithBaseURL permits an injected HTTP server/client in tests.
func NewWithBaseURL(baseURL string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)

	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {

		return nil, errors.New("invalid Frankfurter API base URL")
	}

	if httpClient == nil {
		httpClient = &http.Client{}
	}

	copyClient := *httpClient

	// Do not follow redirects to another origin or make a second hidden request.
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &Client{
		baseURL: strings.TrimSuffix(u.String(), "/"),
		http:    &copyClient,
		now:     time.Now,
	}, nil
}

// FetchRate fetches the provider-supplied rate.
func (c *Client) FetchRate(ctx context.Context, pair domain.CurrencyPair) (quoteservice.FetchedRate, error) {
	if err := pair.Validate(); err != nil {
		return quoteservice.FetchedRate{}, err
	}

	if err := ctx.Err(); err != nil {
		return quoteservice.FetchedRate{}, err // Preserve worker shutdown/deadline.
	}

	base, quote := string(pair[:3]), string(pair[4:])

	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.baseURL + "/v2/rate/" + strings.ToLower(base) + "/" + strings.ToLower(quote)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return quoteservice.FetchedRate{}, providerError(domain.FailureProviderInvalidResponse, "Invalid provider request", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fx-quote-service/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return quoteservice.FetchedRate{}, ctx.Err()
		}

		return quoteservice.FetchedRate{}, classifyTransportError(err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return quoteservice.FetchedRate{}, classifyHTTPStatus(resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return quoteservice.FetchedRate{}, ctx.Err()
		}

		return quoteservice.FetchedRate{}, classifyTransportError(err)
	}

	if len(data) > maxResponseBytes {
		return quoteservice.FetchedRate{}, providerError(domain.FailureProviderInvalidResponse, "Invalid provider response", errors.New("body exceeds size limit"))
	}

	if ctx.Err() != nil {
		return quoteservice.FetchedRate{}, ctx.Err()
	}

	price, sourceDate, err := parseRateResponse(data, base, quote)
	if err != nil {
		return quoteservice.FetchedRate{}, providerError(domain.FailureProviderInvalidResponse, "Invalid provider response", err)
	}

	return quoteservice.FetchedRate{
		Price:      price,
		SourceDate: sourceDate,
		UpdatedAt:  c.now().UTC(),
	}, nil
}

func providerError(code domain.FailureCode, message string, cause error) error {
	return &quoteservice.ProviderError{Code: code, Message: message, Cause: cause}
}

func classifyHTTPStatus(status int) error {
	cause := fmt.Errorf("Frankfurter HTTP status %d", status)

	switch {
	case status == http.StatusNotFound:
		return providerError(domain.FailureProviderRateNotFound, "Rate not found at provider", cause)
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return providerError(domain.FailureProviderTimeout, "Quote provider timed out", cause)
	case status == http.StatusTooManyRequests:
		return providerError(domain.FailureProviderRateLimited, "Quote provider rate limit exceeded", cause)
	case status >= 500:
		return providerError(domain.FailureProviderUnavailable, "Quote provider is temporarily unavailable", cause)
	case status >= 400 && status < 500:
		return providerError(domain.FailureProviderRejectedRequest, "Quote provider rejected the request", cause)
	default: // Redirects and unexpected success codes are invalid for this endpoint.
		return providerError(domain.FailureProviderInvalidResponse, "Unexpected quote provider status", cause)
	}
}

func classifyTransportError(err error) error {
	var netErr net.Error

	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return providerError(domain.FailureProviderTimeout, "Quote provider timed out", err)
	}

	return providerError(domain.FailureProviderUnavailable, "Quote provider is temporarily unavailable", err)
}
