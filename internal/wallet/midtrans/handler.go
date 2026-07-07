// Package midtrans is the wallet's top-up slice for the Midtrans payment
// gateway: the user-facing endpoint that creates a Snap transaction and the
// server-to-server notification webhook that books the paid payment into the
// ledger as one balanced topup transaction (credit user, debit pending).
// Creating a transaction writes nothing to the ledger — money only moves when
// Midtrans reports the payment completed.
//
// Like linking, one subpackage per integration: the topup kind and the order
// ID scheme live here because this is their only writer today; a second
// gateway would lift whatever proves identical, not predicted.
package midtrans

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/avagenc/chat/internal/wallet"
	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
)

// Handler is the HTTP glue for topping up a wallet through Midtrans Snap.
// baseURL selects the Midtrans environment (https://app.sandbox.midtrans.com
// or https://app.midtrans.com); serverKey authenticates the Snap API call and
// verifies notification signatures.
type Handler struct {
	ledger    wallet.Ledger
	client    *http.Client
	serverKey string
	baseURL   string
	// apiBaseURL is the Core API host (transaction status), derived from the
	// Snap host — see coreAPIBaseURL.
	apiBaseURL string
}

func NewHandler(ledger wallet.Ledger, client *http.Client, serverKey, baseURL string) *Handler {
	return &Handler{ledger: ledger, client: client, serverKey: serverKey, baseURL: baseURL, apiBaseURL: coreAPIBaseURL(baseURL)}
}

// coreAPIBaseURL derives Midtrans' Core API host from the Snap host. Snap lives
// at app.{sandbox.}midtrans.com, the Core API (transaction status) at
// api.{sandbox.}midtrans.com — the hosts are fixed, so deriving them avoids a
// second env var that could drift out of sync (one pointing at sandbox, the
// other at production, and money booked against the wrong environment). A host
// with no "app." prefix (a test mock) is returned unchanged.
func coreAPIBaseURL(snapBaseURL string) string {
	return strings.Replace(snapBaseURL, "://app.", "://api.", 1)
}

// HandleCreateTopup creates a Snap transaction at Midtrans for the caller and
// returns the token (for Snap.js) plus redirect URL (for the redirect flow).
// No ledger write happens here; the notification webhook books the payment.
func (h *Handler) HandleCreateTopup(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
		return
	}
	var body struct {
		// Amount in whole rupiah — Midtrans requires an integer gross_amount
		// for IDR.
		Amount int64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Amount <= 0 {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "amount (whole rupiah, positive) required"})
		return
	}

	orderID, err := newOrderID(userID)
	if err != nil {
		log.Printf("error: mint topup order ID for user %s: %v", userID, err)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to create order"})
		return
	}
	if len(orderID) > 50 {
		// Midtrans caps order IDs at 50 chars; only a non-default (long)
		// Firebase UID can get here. Fail loudly rather than let Midtrans
		// reject with a vaguer error.
		log.Printf("error: topup order ID %q is %d chars, over Midtrans' 50-char cap — user ID too long", orderID, len(orderID))
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to create order"})
		return
	}
	payload, err := json.Marshal(map[string]any{
		"transaction_details": map[string]any{
			"order_id":     orderID,
			"gross_amount": body.Amount,
		},
	})
	if err != nil {
		log.Printf("error: marshal snap request for order %s: %v", orderID, err)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to create order"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.baseURL+"/snap/v1/transactions", bytes.NewReader(payload))
	if err != nil {
		log.Printf("error: build snap request for order %s: %v", orderID, err)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to create order"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(h.serverKey, "")
	resp, err := h.client.Do(req)
	if err != nil {
		log.Printf("error: snap create for order %s: %v", orderID, err)
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "midtrans unreachable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		log.Printf("error: snap create for order %s: status %d: %s", orderID, resp.StatusCode, detail)
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "midtrans rejected the transaction"})
		return
	}
	var snap struct {
		Token       string `json:"token"`
		RedirectURL string `json:"redirect_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil || snap.Token == "" {
		log.Printf("error: decode snap response for order %s (token present: %t): %v", orderID, snap.Token != "", err)
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "midtrans returned an unreadable response"})
		return
	}

	apihttp.WriteJSON(w, http.StatusOK, struct {
		Token       string `json:"token"`
		RedirectURL string `json:"redirect_url"`
		OrderID     string `json:"order_id"`
	}{snap.Token, snap.RedirectURL, orderID})
}

// newOrderID mints the Midtrans order ID for one top-up attempt:
// "topup~{uid}~{nonce}". The order ID is the only field the notification is
// guaranteed to echo back, so it carries the user binding itself — stateless,
// the same idea as the linking state parameter. Budget: Midtrans caps order
// IDs at 50 chars; "topup~" (6) + a 28-char Firebase UID + "~" + 12 hex = 47.
// "~" is in Midtrans' allowed set and never in a Firebase UID, so parsing
// back is unambiguous.
func newOrderID(userID string) (string, error) {
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("midtrans: order nonce: %w", err)
	}
	return fmt.Sprintf("topup~%s~%x", userID, nonce), nil
}

// KindTopup is the wallet entry kind for a paid top-up: credit user, debit
// pending — the same posting pair whatever the gateway.
const KindTopup wallet.Kind = "topup"

// HandleNotification is the Midtrans HTTP notification webhook (URL is
// configured in the Midtrans dashboard). It sits outside Firebase auth —
// Midtrans' server calls it — authenticated instead by the SHA-512 signature
// over order_id + status_code + gross_amount + server key. Midtrans retries
// any non-2xx response, so: a completed payment books a topup transaction (a
// retry hits ErrDuplicateRef and is acknowledged 200 — the whole transaction
// is rejected, never partial postings), a not-yet/failed status is
// acknowledged without a ledger write, and a completed payment we cannot book
// answers 500 so the retry keeps it visible.
func (h *Handler) HandleNotification(w http.ResponseWriter, r *http.Request) {
	// Unauthenticated public endpoint: cap the body before parsing. Real
	// notifications are ~1 KB.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var n struct {
		OrderID           string `json:"order_id"`
		StatusCode        string `json:"status_code"`
		GrossAmount       string `json:"gross_amount"`
		SignatureKey      string `json:"signature_key"`
		TransactionStatus string `json:"transaction_status"`
		FraudStatus       string `json:"fraud_status"`
		PaymentType       string `json:"payment_type"`
		TransactionID     string `json:"transaction_id"`
		TransactionTime   string `json:"transaction_time"`
		Currency          string `json:"currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid notification body"})
		return
	}
	sum := sha512.Sum512([]byte(n.OrderID + n.StatusCode + n.GrossAmount + h.serverKey))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(n.SignatureKey)), []byte(hex.EncodeToString(sum[:]))) != 1 {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "invalid signature"})
		return
	}

	// The signed body indicates a completed payment: "settlement", or a card
	// "capture" that passed fraud review — AND only when the signed status_code
	// agrees. The signature covers order_id + status_code + gross_amount but NOT
	// transaction_status/fraud_status, so without the status_code gate a
	// captured non-success notification (signed "201" pending, "202" deny, …)
	// could be replayed with its unsigned status fields edited to "settlement"
	// and still pass verification. Midtrans reports every completed payment with
	// status_code "200".
	bodyClaimsPaid := n.StatusCode == "200" &&
		(n.TransactionStatus == "settlement" || (n.TransactionStatus == "capture" && n.FraudStatus == "accept"))
	if !bodyClaimsPaid {
		if n.TransactionStatus == "refund" || n.TransactionStatus == "partial_refund" {
			// A refund pushed from the Midtrans dashboard moves real money
			// back to the payer with no ledger write here (application policy
			// is no-refund, so none is booked automatically). Reconciliation
			// against Midtrans will show the gap — flag it for a manual
			// balancing transaction.
			log.Printf("error: midtrans notification for order %s: %s of %s at the gateway is NOT booked to the ledger — record a balancing transaction manually", n.OrderID, n.TransactionStatus, n.GrossAmount)
		} else {
			log.Printf("info: midtrans notification for order %s: status %s (code %s), no ledger write", n.OrderID, n.TransactionStatus, n.StatusCode)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Never book from the notification body alone. The webhook signature proves
	// authenticity only as long as the server key is secret; if it ever leaks,
	// a forged "paid" notification would pass. So confirm every completed
	// payment against Midtrans' authoritative status API (which the leaked key
	// cannot make lie) and book the amount, status, and currency IT reports —
	// not the body's. This is also the guard against booking an amount the body
	// carries that never matched a real transaction.
	status, err := h.confirmTransaction(r.Context(), n.OrderID)
	if err != nil {
		// Inconclusive (network, bad key, gateway 5xx). A real payment must not
		// be dropped on an unconfirmed answer — 500 so Midtrans retries and it
		// stays visible.
		log.Printf("error: midtrans confirm order %s: %v", n.OrderID, err)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "confirmation failed"})
		return
	}
	apiPaid := status.StatusCode == "200" &&
		(status.TransactionStatus == "settlement" || (status.TransactionStatus == "capture" && status.FraudStatus == "accept"))
	if !apiPaid {
		// The signed body claimed a completed payment but Midtrans' own record
		// disagrees (still pending, or no such transaction). Do not book;
		// acknowledge so a genuine-but-stale retry stops.
		log.Printf("error: midtrans confirm order %s: body claimed paid but authoritative status is %q (code %s), not booking", n.OrderID, status.TransactionStatus, status.StatusCode)
		w.WriteHeader(http.StatusOK)
		return
	}
	if status.Currency != "" && status.Currency != "IDR" {
		// Booking a non-IDR amount into IDR accounts would corrupt the ledger.
		log.Printf("error: midtrans notification for order %s: currency %s, cannot book", n.OrderID, status.Currency)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "unsupported currency"})
		return
	}
	userID, ok := userIDFromOrderID(n.OrderID)
	if !ok {
		// Authentic payment, but not an order this handler minted (e.g.
		// created straight in the Midtrans dashboard) — not the wallet's to
		// book. Acknowledge so Midtrans stops retrying; the payment stays on
		// record at Midtrans.
		log.Printf("error: midtrans notification for order %s: not a topup order ID, skipping ledger write", n.OrderID)
		w.WriteHeader(http.StatusOK)
		return
	}
	micros, err := amountMicros(status.GrossAmount)
	if err == nil && micros == 0 {
		err = errors.New("midtrans: gross_amount is zero")
	}
	if err != nil {
		log.Printf("error: midtrans notification for order %s: %v", n.OrderID, err)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "unbookable amount"})
		return
	}

	metadata, err := json.Marshal(struct {
		PaymentType     string `json:"payment_type"`
		TransactionID   string `json:"transaction_id"`
		TransactionTime string `json:"transaction_time"`
	}{status.PaymentType, status.TransactionID, status.TransactionTime})
	if err != nil {
		log.Printf("error: marshal topup metadata for order %s: %v", n.OrderID, err)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to record topup"})
		return
	}
	_, err = h.ledger.Transact(r.Context(), wallet.Spec{
		Kind:     KindTopup,
		Ref:      n.OrderID,
		Metadata: metadata,
		Postings: []wallet.Posting{
			{AccountID: wallet.UserAccountID(userID), Amount: micros},
			{AccountID: wallet.AccountPending, Amount: -micros},
		},
	})
	// ErrDuplicateRef = a retry, or settlement arriving after capture was
	// already booked — the idempotency the ref exists for.
	if err != nil && !errors.Is(err, wallet.ErrDuplicateRef) {
		log.Printf("error: book topup for order %s: %v", n.OrderID, err)
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to record topup"})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// midtransStatus is the subset of the Core API transaction-status response the
// webhook books from — the authoritative record, queried straight from Midtrans
// rather than trusted off the notification body.
type midtransStatus struct {
	StatusCode        string `json:"status_code"`
	TransactionStatus string `json:"transaction_status"`
	FraudStatus       string `json:"fraud_status"`
	GrossAmount       string `json:"gross_amount"`
	Currency          string `json:"currency"`
	PaymentType       string `json:"payment_type"`
	TransactionID     string `json:"transaction_id"`
	TransactionTime   string `json:"transaction_time"`
}

// confirmTransaction fetches an order's authoritative status from Midtrans'
// Core API (GET /v2/{order_id}/status), authenticated by the server key. HTTP
// 200 (found) and 404 (no such transaction) both carry a status_code in the
// JSON body and are returned as an authoritative answer; any other code (401
// bad key, 5xx) or an unreadable body is an *inconclusive* answer returned as
// an error, so the caller retries rather than dropping a real payment.
func (h *Handler) confirmTransaction(ctx context.Context, orderID string) (*midtransStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.apiBaseURL+"/v2/"+url.PathEscape(orderID)+"/status", nil)
	if err != nil {
		return nil, fmt.Errorf("midtrans: build status request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(h.serverKey, "")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("midtrans: status request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("midtrans: read status response: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return nil, fmt.Errorf("midtrans: status API HTTP %d: %s", resp.StatusCode, body)
	}
	var s midtransStatus
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("midtrans: unreadable status response (HTTP %d): %w", resp.StatusCode, err)
	}
	return &s, nil
}

// userIDFromOrderID parses the user binding back out of an order ID minted by
// newOrderID.
func userIDFromOrderID(orderID string) (string, bool) {
	rest, ok := strings.CutPrefix(orderID, "topup~")
	if !ok {
		return "", false
	}
	i := strings.LastIndexByte(rest, '~')
	if i < 1 {
		return "", false
	}
	return rest[:i], true
}

// amountMicros converts Midtrans' decimal gross_amount string (e.g.
// "50000.00") to wallet micros — exact integer arithmetic, no floats, per the
// ledger's write-path rule.
func amountMicros(s string) (int64, error) {
	units, frac, _ := strings.Cut(s, ".")
	u, err := strconv.ParseInt(units, 10, 64)
	if err != nil || u < 0 {
		return 0, fmt.Errorf("midtrans: gross_amount %q is not a non-negative decimal", s)
	}
	var f int64
	if frac != "" {
		if len(frac) > 6 {
			return 0, fmt.Errorf("midtrans: gross_amount %q is finer than micros", s)
		}
		f, err = strconv.ParseInt(frac, 10, 64)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("midtrans: gross_amount %q is not a non-negative decimal", s)
		}
		for i := len(frac); i < 6; i++ {
			f *= 10
		}
	}
	if u > (math.MaxInt64-f)/1_000_000 {
		return 0, fmt.Errorf("midtrans: gross_amount %q overflows micros", s)
	}
	return u*1_000_000 + f, nil
}
