package midtrans

// Integration test of the notification webhook against a real PostgreSQL
// database, per the repo convention (behavior over mocks). The webhook's
// authenticity is its signature (computed here the way Midtrans does) plus a
// confirmation call to Midtrans' status API — which a fake HTTP server stands
// in for, letting the test control what Midtrans "authoritatively" reports
// independently of the (signed) notification body. Skipped unless
// WALLET_TEST_DB_URL is set. Every run DROPs the wallet tables and reapplies
// the embedded migrations from scratch — point it at a disposable database,
// never at a shared one.

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/avagenc/chat/internal/wallet"
	walletpostgres "github.com/avagenc/chat/internal/wallet/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeMidtrans stands in for Midtrans' Core API status endpoint
// (GET /v2/{order_id}/status). Tests register per-order responses so they can
// make the authoritative record agree or disagree with the notification body.
type fakeMidtrans struct {
	mu        sync.Mutex
	responses map[string]stubResp
}

type stubResp struct {
	code int
	body string
}

func (f *fakeMidtrans) set(orderID string, code int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[orderID] = stubResp{code: code, body: body}
}

func (f *fakeMidtrans) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v2" || parts[2] != "status" {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	resp, ok := f.responses[parts[1]]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"status_code":"404","status_message":"Transaction doesn't exist."}`)
		return
	}
	w.WriteHeader(resp.code)
	io.WriteString(w, resp.body)
}

// paidStatus is a status-API body for a completed payment of the given amount.
func paidStatus(transactionStatus, fraud, gross string) string {
	return fmt.Sprintf(`{"status_code":"200","transaction_status":%q,"fraud_status":%q,"gross_amount":%q,"currency":"IDR","payment_type":"qris","transaction_id":"9aed5972-5b6a-401e-894b-a32c91ed1a3a","transaction_time":"2026-07-07 10:00:00"}`, transactionStatus, fraud, gross)
}

func newTestHandler(t *testing.T) (*Handler, *fakeMidtrans) {
	t.Helper()
	url := os.Getenv("WALLET_TEST_DB_URL")
	if url == "" {
		t.Skip("WALLET_TEST_DB_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, table := range []string{"entries", "transactions", "accounts"} {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	// The goose directives in the migration files are plain SQL comments, so
	// applying them is just executing the files in order.
	migrations, err := fs.Glob(walletpostgres.MigrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	slices.Sort(migrations)
	if len(migrations) == 0 {
		t.Fatal("no migration files found")
	}
	for _, path := range migrations {
		sql, err := fs.ReadFile(walletpostgres.MigrationsFS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}

	ledger, err := walletpostgres.NewLedger(ctx, pool)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	// The fake stands in for Midtrans. Its URL has no "app." prefix, so
	// coreAPIBaseURL leaves it unchanged and the status confirmation hits the
	// fake too.
	fake := &fakeMidtrans{responses: map[string]stubResp{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return NewHandler(ledger, server.Client(), testServerKey, server.URL), fake
}

const testServerKey = "SB-Mid-server-testkey"

// notify signs the fields the way Midtrans does and POSTs the notification.
func notify(t *testing.T, h *Handler, orderID, statusCode, status, fraud, gross string) *httptest.ResponseRecorder {
	t.Helper()
	sum := sha512.Sum512([]byte(orderID + statusCode + gross + testServerKey))
	body := `{
		"order_id": "` + orderID + `",
		"status_code": "` + statusCode + `",
		"gross_amount": "` + gross + `",
		"signature_key": "` + hex.EncodeToString(sum[:]) + `",
		"transaction_status": "` + status + `",
		"fraud_status": "` + fraud + `",
		"payment_type": "qris",
		"transaction_id": "9aed5972-5b6a-401e-894b-a32c91ed1a3a",
		"transaction_time": "2026-07-07 10:00:00",
		"currency": "IDR"
	}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/wallet/topup/notification", strings.NewReader(body))
	h.HandleNotification(w, r)
	return w
}

func mustBalance(t *testing.T, h *Handler, accountID string) int64 {
	t.Helper()
	balance, err := h.ledger.Balance(context.Background(), accountID)
	if err != nil {
		t.Fatalf("balance %s: %v", accountID, err)
	}
	return balance
}

func TestHandleNotification(t *testing.T) {
	h, fake := newTestHandler(t)
	alice := wallet.UserAccountID("alice", wallet.IDR)
	orderID, err := newOrderID("alice")
	if err != nil {
		t.Fatalf("newOrderID: %v", err)
	}

	// A settled payment books one balanced topup: credit user, debit pending —
	// but only after Midtrans' authoritative status confirms it.
	fake.set(orderID, http.StatusOK, paidStatus("settlement", "", "50000.00"))
	if w := notify(t, h, orderID, "200", "settlement", "", "50000.00"); w.Code != http.StatusOK {
		t.Fatalf("settlement: got %d, want 200: %s", w.Code, w.Body)
	}
	if got := mustBalance(t, h, alice); got != 50_000_000_000 {
		t.Errorf("user balance after settlement: got %d, want 50_000_000_000", got)
	}
	if got := mustBalance(t, h, wallet.PendingAccountID(wallet.IDR)); got != -50_000_000_000 {
		t.Errorf("pending balance after settlement: got %d, want -50_000_000_000", got)
	}

	// A Midtrans retry of the same order is acknowledged without double credit.
	if w := notify(t, h, orderID, "200", "settlement", "", "50000.00"); w.Code != http.StatusOK {
		t.Fatalf("retry: got %d, want 200: %s", w.Code, w.Body)
	}
	if got := mustBalance(t, h, alice); got != 50_000_000_000 {
		t.Errorf("user balance after retry: got %d, want unchanged 50_000_000_000", got)
	}

	// The booked amount comes from the authoritative status, not the signed
	// body: even though the body's gross_amount is honest here, confirmation is
	// the source of truth.

	// A pending status is acknowledged and writes nothing (body not paid, so no
	// confirmation call is even made).
	pendingOrder, err := newOrderID("alice")
	if err != nil {
		t.Fatalf("newOrderID: %v", err)
	}
	if w := notify(t, h, pendingOrder, "201", "pending", "", "10000.00"); w.Code != http.StatusOK {
		t.Fatalf("pending: got %d, want 200: %s", w.Code, w.Body)
	}
	if got := mustBalance(t, h, alice); got != 50_000_000_000 {
		t.Errorf("user balance after pending: got %d, want unchanged 50_000_000_000", got)
	}

	// A pending-signed body whose UNSIGNED status fields were edited to claim
	// settlement must not book: the signature covers status_code, and only
	// "200" is a completed payment.
	if w := notify(t, h, pendingOrder, "201", "settlement", "accept", "10000.00"); w.Code != http.StatusOK {
		t.Fatalf("forged status: got %d, want 200 ack: %s", w.Code, w.Body)
	}
	if got := mustBalance(t, h, alice); got != 50_000_000_000 {
		t.Errorf("user balance after forged-status notification: got %d, want unchanged 50_000_000_000", got)
	}

	// Server-key-leak defense: a body correctly signed as a completed payment
	// (status_code 200 + settlement) must still NOT book when Midtrans' own
	// record says the transaction is only pending. Confirmation, not the
	// signature, has the final say.
	leakOrder, err := newOrderID("alice")
	if err != nil {
		t.Fatalf("newOrderID: %v", err)
	}
	fake.set(leakOrder, http.StatusOK, `{"status_code":"201","transaction_status":"pending","gross_amount":"99999.00","currency":"IDR"}`)
	if w := notify(t, h, leakOrder, "200", "settlement", "", "99999.00"); w.Code != http.StatusOK {
		t.Fatalf("unconfirmed paid: got %d, want 200 ack: %s", w.Code, w.Body)
	}
	if got := mustBalance(t, h, alice); got != 50_000_000_000 {
		t.Errorf("user balance after unconfirmed-paid notification: got %d, want unchanged 50_000_000_000", got)
	}

	// A capture that passed fraud review books like a settlement.
	captureOrder, err := newOrderID("alice")
	if err != nil {
		t.Fatalf("newOrderID: %v", err)
	}
	fake.set(captureOrder, http.StatusOK, paidStatus("capture", "accept", "25000.00"))
	if w := notify(t, h, captureOrder, "200", "capture", "accept", "25000.00"); w.Code != http.StatusOK {
		t.Fatalf("capture accept: got %d, want 200: %s", w.Code, w.Body)
	}
	if got := mustBalance(t, h, alice); got != 75_000_000_000 {
		t.Errorf("user balance after capture: got %d, want 75_000_000_000", got)
	}

	// An inconclusive confirmation (gateway 5xx) must NOT drop a real payment:
	// answer 500 so Midtrans retries and it stays visible.
	flakyOrder, err := newOrderID("alice")
	if err != nil {
		t.Fatalf("newOrderID: %v", err)
	}
	fake.set(flakyOrder, http.StatusInternalServerError, `{"status_code":"500"}`)
	if w := notify(t, h, flakyOrder, "200", "settlement", "", "30000.00"); w.Code != http.StatusInternalServerError {
		t.Fatalf("inconclusive confirm: got %d, want 500: %s", w.Code, w.Body)
	}
	if got := mustBalance(t, h, alice); got != 75_000_000_000 {
		t.Errorf("user balance after inconclusive confirm: got %d, want unchanged 75_000_000_000", got)
	}

	// A forged signature is rejected before any confirmation and writes nothing.
	forged := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/wallet/topup/notification", strings.NewReader(
		`{"order_id": "`+orderID+`", "status_code": "200", "gross_amount": "99999.00", "signature_key": "deadbeef", "transaction_status": "settlement"}`,
	))
	h.HandleNotification(forged, r)
	if forged.Code != http.StatusForbidden {
		t.Fatalf("forged signature: got %d, want 403: %s", forged.Code, forged.Body)
	}
	if got := mustBalance(t, h, alice); got != 75_000_000_000 {
		t.Errorf("user balance after forged notification: got %d, want unchanged 75_000_000_000", got)
	}
}

func TestAmountMicros(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "50000.00", want: 50_000_000_000},
		{in: "50000", want: 50_000_000_000},
		{in: "0.5", want: 500_000},
		{in: "10000.123456", want: 10_000_123_456},
		{in: "0.1234567", wantErr: true},
		{in: "-1.00", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "", wantErr: true},
		{in: "10000000000000", wantErr: true}, // 1e13 rupiah overflows int64 micros
	} {
		got, err := amountMicros(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("amountMicros(%q): got %d, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("amountMicros(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("amountMicros(%q): got %d, want %d", tc.in, got, tc.want)
		}
	}
}
