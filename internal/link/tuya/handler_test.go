package tuya

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apiuser "go.trueardian.com/api/user"
	"go.trueardian.com/tuya/appaccount"
)

type fakeConnector struct {
	owner string
	err   error
}

func (c *fakeConnector) Get(_ context.Context, owner string) (appaccount.Account, error) {
	c.owner = owner
	return appaccount.Account{Owner: owner, TuyaUID: "tuya-user"}, c.err
}

func TestHandleStatus(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		err        error
		statusCode int
		body       string
	}{
		{name: "linked", userID: "user-1", statusCode: http.StatusOK, body: "{\"connected\":true}\n"},
		{name: "not linked", userID: "user-1", err: fmt.Errorf("lookup: %w", appaccount.ErrNotLinked), statusCode: http.StatusOK, body: "{\"connected\":false}\n"},
		{name: "store unavailable", userID: "user-1", err: errors.New("unavailable"), statusCode: http.StatusBadGateway},
		{name: "missing user", statusCode: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector := &fakeConnector{err: test.err}
			request := httptest.NewRequest(http.MethodGet, "/tuya/connection", nil)
			if test.userID != "" {
				ctx, err := apiuser.ContextWithID(request.Context(), test.userID)
				if err != nil {
					t.Fatal(err)
				}
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()

			NewHandler(connector).HandleStatus(response, request)

			if response.Code != test.statusCode {
				t.Fatalf("status = %d, want %d", response.Code, test.statusCode)
			}
			if test.body != "" && response.Body.String() != test.body {
				t.Errorf("body = %q, want %q", response.Body.String(), test.body)
			}
			if connector.owner != test.userID {
				t.Errorf("owner = %q, want %q", connector.owner, test.userID)
			}
		})
	}
}
