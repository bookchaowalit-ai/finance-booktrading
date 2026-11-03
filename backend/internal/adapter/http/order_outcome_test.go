package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"trading-bot-system/backend/internal/adapter/exchange"
)

// The strategy treats 4xx as "not placed" and 5xx as "maybe placed", so an
// unknown exchange outcome must never come back as 400.
func TestPlaceOrderErrorStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: HTTP 503", exchange.ErrOrderStateUnknown), http.StatusBadGateway},
		{context.DeadlineExceeded, http.StatusBadGateway},
		{errors.New("insufficient balance"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := placeOrderErrorStatus(tc.err); got != tc.want {
			t.Errorf("%v: got %d, want %d", tc.err, got, tc.want)
		}
	}
}
