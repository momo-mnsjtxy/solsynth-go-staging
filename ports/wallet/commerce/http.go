// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strconv"
	"strings"
	"time"
)

type Authorizer interface {
	Require(context.Context, wallet.Principal, string) error
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func body(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 65537))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return ErrInvalid
	}
	var more any
	if e := d.Decode(&more); e != io.EOF {
		return ErrInvalid
	}
	return nil
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, e error, code string) {
	status := 500
	message := "Commerce operation failed."
	var f *authority.Failure
	if errors.As(e, &f) {
		status = f.Status
		code = f.Code
		message = "Authentication or permission check failed."
	} else {
		switch {
		case errors.Is(e, ErrRetry):
			status = 409
			code = "ORDER_RETRY_REQUIRED"
			message = "The transaction rolled back after a concurrent change; retry the same request."
		case errors.Is(e, ErrNotFound), errors.Is(e, wallet.ErrNotFound):
			status = 404
			message = "Resource was not found."
		case errors.Is(e, ErrInvalid), errors.Is(e, ErrStock), errors.Is(e, ErrConflict), errors.Is(e, ErrIdempotency), errors.Is(e, wallet.ErrInsufficient), errors.Is(e, wallet.ErrInvalid):
			status = 400
			message = e.Error()
		case errors.Is(e, ErrUnavailable), errors.Is(e, wallet.ErrUnavailable):
			status = 503
			message = "Required commerce adapter is unavailable."
		case errors.Is(e, ErrForbidden), errors.Is(e, wallet.ErrForbidden), errors.Is(e, wallet.ErrPIN):
			status = 403
			message = "Permission or payment verification failed."
		case errors.Is(e, wallet.ErrUnauthorized):
			status = 401
			message = "Authentication is required."
		}
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="commerce"`)
	}
	reply(w, status, map[string]any{"status": status, "code": code, "message": message})
}
func page(r *http.Request) PageQuery {
	q := PageQuery{Take: 20}
	if v, e := strconv.Atoi(r.URL.Query().Get("offset")); e == nil {
		q.Offset = v
	}
	if v, e := strconv.Atoi(r.URL.Query().Get("take")); e == nil {
		q.Take = v
	}
	if v, e := strconv.Atoi(r.URL.Query().Get("status")); e == nil {
		q.Status = &v
	}
	q.AppIdentifier = r.URL.Query().Get("appIdentifier")
	q.ProductIdentifier = r.URL.Query().Get("productIdentifier")
	return q
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Store == nil {
		problem(w, ErrUnavailable, "WALLET_COMMERCE_UNAVAILABLE")
		return
	}
	if r.Method == http.MethodHead {
		r = r.Clone(r.Context())
		r.Method = "GET"
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.Split(path, "/")
	method := r.Method
	if s.couponIssuerHTTP(w, r, parts) {
		return
	}
	ctx := r.Context()
	done := func(v any, e error, code string) {
		if e != nil {
			problem(w, e, code)
		} else {
			reply(w, 200, v)
		}
	}
	if path == "orders" && method == "POST" {
		var q CreateOrder
		if e := body(r, &q); e != nil {
			problem(w, e, "WALLET_ORDER_CREATE_FAILED")
			return
		}
		v, e := s.Store.CreateOrder(ctx, q, r.Header.Get("Idempotency-Key"))
		done(v, e, "WALLET_ORDER_CREATE_FAILED")
		return
	}
	if path == "orders/metrics" && method == "POST" {
		var q struct {
			Credentials
			StartDate *time.Time `json:"start_date"`
			EndDate   *time.Time `json:"end_date"`
		}
		if e := body(r, &q); e != nil {
			problem(w, e, "WALLET_ORDER_METRICS_FAILED")
			return
		}
		v, e := s.Store.Metrics(ctx, q.Credentials, q.StartDate, q.EndDate)
		done(v, e, "WALLET_ORDER_METRICS_FAILED")
		return
	}
	if len(parts) == 3 && parts[0] == "orders" && parts[2] == "fulfillment" && method == "POST" {
		var credentials Credentials
		if e := body(r, &credentials); e != nil {
			problem(w, e, "WALLET_ORDER_FULFILLMENT_FAILED")
			return
		}
		repo, ok := s.Store.(FulfillmentStore)
		if !ok {
			problem(w, ErrUnavailable, "WALLET_ORDER_FULFILLMENT_UNAVAILABLE")
			return
		}
		v, e := repo.OrderFulfillment(ctx, parts[1], credentials)
		done(v, e, "WALLET_ORDER_FULFILLMENT_FAILED")
		return
	}
	if len(parts) == 3 && parts[0] == "orders" && parts[2] == "status" && method == "PATCH" {
		var q UpdateStatus
		if e := body(r, &q); e != nil {
			problem(w, e, "WALLET_ORDER_INVALID_STATUS")
			return
		}
		v, e := s.Store.UpdateOrder(ctx, parts[1], q)
		done(v, e, "WALLET_ORDER_INVALID_STATUS")
		return
	}
	if len(parts) == 2 && parts[0] == "orders" && parts[1] != "mine" && method == "GET" {
		v, e := s.Store.GetOrder(ctx, "", parts[1], true)
		done(v, e, "WALLET_ORDER_NOT_FOUND")
		return
	}
	if path == "subscriptions/catalog" && method == "GET" {
		v, e := s.Store.Catalog(ctx)
		done(v, e, "WALLET_SUBSCRIPTION_CATALOG_FAILED")
		return
	}
	if path == "subscriptions/groups" && method == "GET" {
		defs, e := s.Store.Catalog(ctx)
		if e != nil {
			problem(w, e, "WALLET_SUBSCRIPTION_CATALOG_FAILED")
			return
		}
		groups := []GroupCatalog{}
		indexes := map[string]int{}
		for _, d := range defs {
			key := d.GroupIdentifier
			if key == "" {
				key = d.Identifier
			}
			i, ok := indexes[key]
			if !ok {
				i = len(groups)
				indexes[key] = i
				groups = append(groups, GroupCatalog{GroupIdentifier: key, DisplayName: d.DisplayName, Items: []Definition{}})
			}
			groups[i].Items = append(groups[i].Items, d)
		}
		reply(w, 200, groups)
		return
	}

	if len(parts) == 4 && parts[0] == "subscriptions" && parts[1] == "order" && parts[2] == "handle" && method == "POST" {
		s.providerWebhook(w, r, parts[3])
		return
	}
	if s.Identity == nil {
		problem(w, wallet.ErrUnauthorized, "UNAUTHORIZED")
		return
	}
	p, e := s.Identity.Authenticate(r)
	if e != nil {
		problem(w, e, "UNAUTHORIZED")
		return
	}
	if !wallet.ValidID(p.AccountID) || p.SessionID == "" {
		problem(w, wallet.ErrUnauthorized, "UNAUTHORIZED")
		return
	}
	ctx = r.Context()
	actor := p.AccountID
	if method == "POST" && len(parts) >= 3 && parts[0] == "orders" && parts[2] == "coupon" && (len(parts) == 3 || len(parts) == 4 && parts[3] == "preview") {
		var q struct {
			Coupon string `json:"coupon"`
		}
		if e = body(r, &q); e != nil || q.Coupon == "" {
			problem(w, ErrInvalid, "COUPON_INVALID")
			return
		}
		a, ok := s.Identity.(Authorizer)
		if !ok {
			problem(w, ErrUnavailable, "COUPON_UNAVAILABLE")
			return
		}
		if e = a.Require(ctx, p, "orders.pay"); e != nil {
			problem(w, e, "COUPON_FORBIDDEN")
			return
		}
		repo, ok := s.Store.(CouponStore)
		if !ok {
			problem(w, ErrUnavailable, "COUPON_UNAVAILABLE")
			return
		}
		v, err := repo.ApplyOrderCoupon(ctx, actor, parts[1], q.Coupon, len(parts) == 4)
		done(v, err, "COUPON_FAILED")
		return
	}
	if path == "subscriptions/pending-activations" && method == "GET" {
		repo, ok := s.Store.(ActivationStore)
		if !ok {
			problem(w, ErrUnavailable, "WALLET_SUBSCRIPTION_PENDING_FAILED")
			return
		}
		v, e := repo.PendingActivations(ctx, actor, page(r))
		done(v, e, "WALLET_SUBSCRIPTION_PENDING_FAILED")
		return
	}
	if len(parts) == 4 && parts[0] == "subscriptions" && parts[1] == "groups" && parts[3] == "activate" && method == "POST" {
		var q ActivateSubscription
		if e = body(r, &q); e != nil || !wallet.ValidID(q.SubscriptionID) {
			problem(w, ErrInvalid, "WALLET_SUBSCRIPTION_ACTIVATE_FAILED")
			return
		}
		a, ok := s.Identity.(Authorizer)
		if !ok {
			problem(w, ErrUnavailable, "WALLET_SUBSCRIPTION_ACTIVATE_FAILED")
			return
		}
		if e = a.Require(ctx, p, "subscriptions.groups.manage"); e != nil {
			problem(w, e, "WALLET_SUBSCRIPTION_ACTIVATE_FAILED")
			return
		}
		repo, ok := s.Store.(ActivationStore)
		if !ok {
			problem(w, ErrUnavailable, "WALLET_SUBSCRIPTION_ACTIVATE_FAILED")
			return
		}
		v, e := repo.SwitchSubscriptionActivation(ctx, actor, parts[2], q.SubscriptionID)
		done(v, e, "WALLET_SUBSCRIPTION_ACTIVATE_FAILED")
		return
	}
	if method == "GET" && len(parts) >= 3 && parts[0] == "subscriptions" && (parts[1] == "groups" || parts[1] == "fuzzy") {
		g, ok := s.Store.(GroupStore)
		if !ok {
			problem(w, ErrUnavailable, "WALLET_SUBSCRIPTION_GROUP_NOT_FOUND")
			return
		}
		v, e := g.SubscriptionGroup(ctx, actor, parts[2])
		if e != nil {
			problem(w, e, "WALLET_SUBSCRIPTION_GROUP_NOT_FOUND")
			return
		}
		if len(parts) == 4 && parts[3] == "active" {
			if v.Current == nil {
				problem(w, ErrNotFound, "WALLET_SUBSCRIPTION_NOT_FOUND")
				return
			}
			reply(w, 200, v.Current.Subscription)
			return
		}
		if len(parts) == 3 {
			reply(w, 200, v)
			return
		}
	}

	if path == "orders/mine" && method == "GET" {
		v, total, e := s.Store.ListOrders(ctx, actor, page(r))
		w.Header().Set("X-Total", strconv.Itoa(total))
		done(v, e, "WALLET_ORDER_LIST_FAILED")
		return
	}
	if len(parts) == 3 && parts[0] == "orders" && parts[1] == "mine" && method == "GET" {
		v, e := s.Store.GetOrder(ctx, actor, parts[2], false)
		done(v, e, "WALLET_ORDER_NOT_FOUND")
		return
	}
	if len(parts) == 3 && parts[0] == "orders" && parts[2] == "pay" && method == "POST" {
		var q PayOrder
		if e := body(r, &q); e != nil {
			problem(w, e, "WALLET_ORDER_PAY_FAILED")
			return
		}
		a, ok := s.Identity.(Authorizer)
		if !ok {
			problem(w, ErrUnavailable, "WALLET_ORDER_PAY_FAILED")
			return
		}
		if e = a.Require(ctx, p, "orders.pay"); e != nil {
			problem(w, e, "WALLET_ORDER_PAY_FAILED")
			return
		}
		if s.Payments == nil {
			problem(w, ErrUnavailable, "WALLET_ORDER_PAY_FAILED")
			return
		}
		pin := ""
		if q.PINCode != nil {
			pin = *q.PINCode
		}
		if payments, ok := s.Payments.(wallet.PaymentAuthorizer); ok {
			ctx, e = payments.PreparePayment(ctx, p, pin, wallet.PaymentOrder, wallet.OrderPaymentBinding(parts[1], q.PayerWalletID))
		} else {
			e = s.Payments.VerifyPayment(ctx, p, pin)
		}
		if e != nil {
			problem(w, e, "WALLET_ORDER_PAY_FAILED")
			return
		}
		v, e := s.Store.PayOrder(ctx, actor, parts[1], q.PayerWalletID)
		done(v, e, "WALLET_ORDER_PAY_FAILED")
		return
	}
	if path == "subscriptions" {
		if method == "GET" {
			v, total, e := s.Store.Subscriptions(ctx, actor, page(r))
			w.Header().Set("X-Total", strconv.Itoa(total))
			done(v, e, "WALLET_SUBSCRIPTION_LIST_FAILED")
			return
		}
		if method == "POST" {
			var q CreateSubscription
			if e := body(r, &q); e != nil {
				problem(w, e, "WALLET_SUBSCRIPTION_CREATE_FAILED")
				return
			}
			noop := strings.EqualFold(r.Header.Get("X-Noop"), "true")
			v, e := s.Store.CreateSubscription(ctx, actor, q, r.Header.Get("Idempotency-Key"), noop)
			done(v, e, "WALLET_SUBSCRIPTION_CREATE_FAILED")
			return
		}
	}
	if len(parts) == 2 && parts[0] == "subscriptions" && parts[1] != "gifts" && method == "GET" {
		v, e := s.Store.Subscription(ctx, actor, parts[1])
		done(v, e, "WALLET_SUBSCRIPTION_NOT_FOUND")
		return
	}
	if len(parts) == 3 && parts[0] == "subscriptions" && parts[1] != "gifts" && method == "POST" {
		if parts[2] == "cancel" {
			v, e := s.Store.CancelSubscription(ctx, actor, parts[1])
			done(v, e, "WALLET_SUBSCRIPTION_CANCEL_FAILED")
			return
		}
		if parts[2] == "order" {
			v, e := s.Store.SubscriptionOrder(ctx, actor, parts[1])
			done(v, e, "WALLET_SUBSCRIPTION_ORDER_CREATE_FAILED")
			return
		}
	}
	if len(parts) == 4 && parts[0] == "subscriptions" && parts[1] == "order" && parts[2] == "restore" && method == "POST" {
		s.providerRestore(w, r, p, parts[3])
		return
	}
	if len(parts) == 4 && parts[0] == "subscriptions" && parts[2] == "checkout" && method == "POST" {
		s.providerCheckout(w, r, p, parts[1], parts[3])
		return
	}
	if len(parts) >= 3 && parts[0] == "subscriptions" && parts[1] == "gifts" {
		switch {
		case len(parts) == 3 && (parts[2] == "sent" || parts[2] == "received") && method == "GET":
			v, total, e := s.Store.Gifts(ctx, actor, parts[2], page(r))
			w.Header().Set("X-Total", strconv.Itoa(total))
			done(v, e, "WALLET_GIFT_LIST_FAILED")
			return
		case len(parts) == 3 && parts[2] == "purchase" && method == "POST":
			var q PurchaseGift
			if e := body(r, &q); e != nil {
				problem(w, e, "WALLET_GIFT_PURCHASE_FAILED")
				return
			}
			v, e := s.Store.PurchaseGift(ctx, actor, q, r.Header.Get("Idempotency-Key"))
			done(v, e, "WALLET_GIFT_PURCHASE_FAILED")
			return
		case len(parts) == 3 && parts[2] == "redeem" && method == "POST":
			var q struct {
				GiftCode string `json:"gift_code"`
			}
			if e := body(r, &q); e != nil {
				problem(w, e, "WALLET_GIFT_REDEEM_FAILED")
				return
			}
			v, e := s.Store.RedeemGift(ctx, actor, q.GiftCode)
			done(v, e, "WALLET_GIFT_REDEEM_FAILED")
			return
		case len(parts) == 4 && parts[2] == "check" && method == "GET":
			v, e := s.Store.CheckGift(ctx, actor, parts[3])
			done(v, e, "WALLET_GIFT_NOT_FOUND")
			return
		case len(parts) == 3 && method == "GET":
			v, e := s.Store.Gift(ctx, actor, parts[2])
			done(v, e, "WALLET_GIFT_NOT_FOUND")
			return
		case len(parts) == 4 && method == "POST" && (parts[3] == "send" || parts[3] == "cancel"):
			v, e := s.Store.GiftAction(ctx, actor, parts[2], parts[3] == "send")
			done(v, e, "WALLET_GIFT_CHANGE_FAILED")
			return
		case len(parts) == 4 && method == "POST" && parts[3] == "order":
			v, e := s.Store.GiftOrder(ctx, actor, parts[2])
			done(v, e, "WALLET_GIFT_ORDER_CREATE_FAILED")
			return
		}
	}
	http.NotFound(w, r)
}
func (s *Service) provider(name string) (Provider, error) {
	if name != "paddle" && name != "afdian" && name != "apple" {
		return nil, ErrNotFound
	}
	p := s.Providers[name]
	if p == nil {
		return nil, ErrUnavailable
	}
	return p, nil
}
func rawBody(r *http.Request) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	if e != nil || len(b) > 65536 {
		return nil, ErrInvalid
	}
	return b, nil
}
func (s *Service) providerWebhook(w http.ResponseWriter, r *http.Request, name string) {
	p, e := s.provider(name)
	if e != nil {
		problem(w, e, "WALLET_PROVIDER_UNAVAILABLE")
		return
	}
	b, e := rawBody(r)
	if e != nil {
		problem(w, e, "WALLET_PROVIDER_INVALID")
		return
	}
	purchase, e := p.VerifyWebhook(r.Context(), r.Header.Clone(), b)
	if e != nil {
		problem(w, ErrForbidden, "WALLET_PROVIDER_INVALID")
		return
	}
	if purchase.Provider != name {
		problem(w, ErrForbidden, "WALLET_PROVIDER_INVALID")
		return
	}
	store, ok := s.Store.(ProviderStore)
	if !ok {
		problem(w, ErrUnavailable, "WALLET_PROVIDER_UNAVAILABLE")
		return
	}
	_, e = store.ApplyProvider(r.Context(), purchase)
	if e != nil {
		problem(w, e, "WALLET_SUBSCRIPTION_RESTORE_FAILED")
		return
	}
	reply(w, 200, map[string]bool{"processed": true})
}
func (s *Service) providerRestore(w http.ResponseWriter, r *http.Request, actor wallet.Principal, name string) {
	p, e := s.provider(name)
	if e != nil {
		problem(w, e, "WALLET_PROVIDER_UNAVAILABLE")
		return
	}
	b, e := rawBody(r)
	if e != nil || !json.Valid(b) {
		problem(w, ErrInvalid, "WALLET_PROVIDER_INVALID")
		return
	}
	purchase, e := p.Restore(r.Context(), actor.AccountID, b)
	if e != nil {
		problem(w, e, "WALLET_SUBSCRIPTION_RESTORE_FAILED")
		return
	}
	if purchase.Provider != name || purchase.AccountID != actor.AccountID {
		problem(w, ErrForbidden, "WALLET_PROVIDER_ACCOUNT_MISMATCH")
		return
	}
	store, ok := s.Store.(ProviderStore)
	if !ok {
		problem(w, ErrUnavailable, "WALLET_PROVIDER_UNAVAILABLE")
		return
	}
	v, e := store.ApplyProvider(r.Context(), purchase)
	if e != nil {
		problem(w, e, "WALLET_SUBSCRIPTION_RESTORE_FAILED")
		return
	}
	reply(w, 200, v)
}
func (s *Service) providerCheckout(w http.ResponseWriter, r *http.Request, actor wallet.Principal, identifier, name string) {
	p, e := s.provider(name)
	if e != nil {
		problem(w, e, "WALLET_PROVIDER_UNAVAILABLE")
		return
	}
	a, ok := s.Identity.(Authorizer)
	if !ok {
		problem(w, ErrUnavailable, "WALLET_PROVIDER_UNAVAILABLE")
		return
	}
	if e = a.Require(r.Context(), actor, "subscriptions.checkout"); e != nil {
		problem(w, e, "WALLET_SUBSCRIPTION_CHECKOUT_FAILED")
		return
	}
	b, e := rawBody(r)
	if e != nil {
		problem(w, e, "WALLET_PROVIDER_INVALID")
		return
	}
	if len(b) == 0 {
		b = []byte(`{}`)
	}
	if !json.Valid(b) {
		problem(w, ErrInvalid, "WALLET_PROVIDER_INVALID")
		return
	}
	v, e := p.Checkout(r.Context(), actor.AccountID, identifier, b)
	if e != nil {
		problem(w, e, "WALLET_SUBSCRIPTION_CHECKOUT_FAILED")
		return
	}
	reply(w, 200, v)
}
