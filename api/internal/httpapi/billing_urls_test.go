package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing/stripetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

type customerMap struct {
	mu sync.Mutex
	m  map[uuid.UUID]string
}

func (c *customerMap) StripeCustomerID(_ context.Context, id uuid.UUID) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id], nil
}

func (c *customerMap) SetStripeCustomerID(_ context.Context, id uuid.UUID, customer string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[id] == "" {
		c.m[id] = customer
	}
	return c.m[id], nil
}

func (c *customerMap) ClearStripeCustomerID(_ context.Context, id uuid.UUID, customer string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[id] == customer {
		delete(c.m, id)
	}
	return nil
}

func TestBillingAnswers502UnlessStripeReturnsAnHTTPSURL(t *testing.T) {
	for _, link := range []string{
		"http://checkout.stripe.test/c/pay/cs_1",
		"https://user:pass@checkout.stripe.test/c/pay/cs_1",
		"not a url",
	} {
		fake := stripetest.New(t)
		fake.ReturnSessionURL(link)
		user := store.User{ID: uuid.New(), WorkOSUserID: "user_01"}
		customers := &customerMap{m: map[uuid.UUID]string{}}
		biller := billing.New(config.Stripe{SecretKey: config.NewSecret(stripetest.SecretKey), PriceID: "price_test"}, "https://civic.example", customers, billing.WithAPIURL(fake.URL), billing.WithoutRetryDelay())
		h := NewHandler(fakeStore{users: &userCalls{user: user}}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(billingFlags(true, true)), WithBilling(biller))

		for _, path := range []string{checkoutPath, portalPath} {
			rec := post(t, h, path, "Bearer good")
			if rec.Code != http.StatusBadGateway || decodeError(t, rec).Code != "billing_unavailable" {
				t.Errorf("%s with %q: status %d, body %s", path, link, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), link) {
				t.Errorf("%s: the body carries Stripe's url", path)
			}
		}
		if n := len(fake.RequestsTo("/v1/billing_portal/sessions")); n != 1 {
			t.Errorf("%q: portal calls %d, want 1", link, n)
		}
	}
}
