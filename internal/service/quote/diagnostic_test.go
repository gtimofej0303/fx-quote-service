package quote_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
	service "github.com/gtimofej0303/fx-quote-service/internal/service/quote"
)

// TestProviderDiagnosticFallbackIsNotSerialized checks that the original provider error is kept internally and left out of JSON.
func TestProviderDiagnosticFallbackIsNotSerialized(t *testing.T) {
	providerErr := &service.ProviderError{Code: domain.FailureProviderUnavailable, Message: "Safe public message"}
	wrapped := fmt.Errorf("internal diagnostic marker: %w", providerErr)
	provider := &fakeRateProvider{fetchFn: func(context.Context, domain.CurrencyPair) (service.FetchedRate, error) {
		return service.FetchedRate{}, wrapped
	}}

	result, err := newService(t, &fakeRepository{}).ProcessJob(context.Background(), processingJob(t), provider)
	if err != nil || !errors.Is(result.Diagnostic, wrapped) || result.Failure.Message != providerErr.Message {
		t.Fatalf("missing diagnostic fallback: %+v / %v", result, err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), "Diagnostic") || strings.Contains(string(data), "internal diagnostic marker") {
		t.Fatalf("diagnostic must not be serialized: %s", data)
	}
}
