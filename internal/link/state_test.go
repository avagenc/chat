package link

// The OAuth state parameter is a trust boundary, not a formatting detail: it
// is what binds a consent flow to the user who asked for it and to the
// integration it was minted for. These are pure-function table tests — no
// backend, no doubles — so they run everywhere.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignStateVerifyState(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Unix(1_700_000_000, 0)
	valid := SignState(secret, "gworkspace", "user-1", now.Add(StateTTL))

	tests := []struct {
		name        string
		secret      []byte
		state       string
		integration string
		owner       string
		now         time.Time
		want        bool
	}{
		{
			name:  "round trip",
			state: valid, secret: secret, integration: "gworkspace", owner: "user-1", now: now,
			want: true,
		},
		{
			// The CSRF case: a state minted for the attacker never verifies
			// against the victim's authenticated identity.
			name:  "other owner",
			state: valid, secret: secret, integration: "gworkspace", owner: "user-2", now: now,
			want: false,
		},
		{
			// Domain separation: one shared secret, but a gworkspace state must
			// not pass at the spotify connect endpoint.
			name:  "other integration",
			state: valid, secret: secret, integration: "spotify", owner: "user-1", now: now,
			want: false,
		},
		{
			name:  "other secret",
			state: valid, secret: []byte("another-secret"), integration: "gworkspace", owner: "user-1", now: now,
			want: false,
		},
		{
			name:  "at expiry",
			state: valid, secret: secret, integration: "gworkspace", owner: "user-1", now: now.Add(StateTTL),
			want: true,
		},
		{
			name:  "past expiry",
			state: valid, secret: secret, integration: "gworkspace", owner: "user-1", now: now.Add(StateTTL + time.Second),
			want: false,
		},
		{
			// Extending the deadline changes the signed input, so the mac no
			// longer matches: expiry is covered, not merely carried.
			name:   "expiry rewritten",
			state:  rewriteExpiry(t, valid, now.Add(24*time.Hour)),
			secret: secret, integration: "gworkspace", owner: "user-1", now: now.Add(time.Hour),
			want: false,
		},
		{
			name:  "mac tampered",
			state: flipLastByte(t, valid), secret: secret, integration: "gworkspace", owner: "user-1", now: now,
			want: false,
		},
		{
			name:  "mac not base64",
			state: "gworkspace.9999999999.not base64!", secret: secret, integration: "gworkspace", owner: "user-1", now: now,
			want: false,
		},
		{
			name:  "expiry not numeric",
			state: "gworkspace.soon.abc", secret: secret, integration: "gworkspace", owner: "user-1", now: now,
			want: false,
		},
		{
			name:  "no separator",
			state: "gworkspace", secret: secret, integration: "gworkspace", owner: "user-1", now: now,
			want: false,
		},
		{
			name:  "one separator",
			state: "gworkspace.9999999999", secret: secret, integration: "gworkspace", owner: "user-1", now: now,
			want: false,
		},
		{
			name:  "empty",
			state: "", secret: secret, integration: "gworkspace", owner: "user-1", now: now,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VerifyState(tt.secret, tt.state, tt.integration, tt.owner, tt.now)
			if got != tt.want {
				t.Errorf("VerifyState(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

// The frontend callback page routes by URL path, but the integration segment
// stays readable so a state is self-describing in logs and in a support
// conversation.
func TestSignStateFormat(t *testing.T) {
	expiry := time.Unix(1_700_000_900, 0)
	state := SignState([]byte("test-secret"), "spotify", "user-1", expiry)

	parts := strings.Split(state, ".")
	if len(parts) != 3 {
		t.Fatalf("SignState() = %q, want 3 dot-separated segments", state)
	}
	if parts[0] != "spotify" {
		t.Errorf("integration segment = %q, want %q", parts[0], "spotify")
	}
	if parts[1] != "1700000900" {
		t.Errorf("expiry segment = %q, want %q", parts[1], "1700000900")
	}
	if strings.ContainsAny(parts[2], "+/=") {
		t.Errorf("mac segment %q is not URL-safe base64", parts[2])
	}
}

// Two owners must never collide in the mac, whatever their IDs look like.
func TestSignStateDistinguishesOwners(t *testing.T) {
	secret := []byte("test-secret")
	expiry := time.Unix(1_700_000_900, 0)
	if SignState(secret, "spotify", "a", expiry) == SignState(secret, "spotify", "b", expiry) {
		t.Error("states for different owners are identical")
	}
	// The NUL separator keeps the concatenation unambiguous: owner "a\x00b"
	// must not sign the same as integration "spotify" + owner "a" would.
	if SignState(secret, "spotify", "ab", expiry) == SignState(secret, "spotifya", "b", expiry) {
		t.Error("owner/integration boundary is ambiguous")
	}
}

func rewriteExpiry(t *testing.T, state string, expiry time.Time) string {
	t.Helper()
	parts := strings.Split(state, ".")
	if len(parts) != 3 {
		t.Fatalf("state %q is malformed", state)
	}
	parts[1] = strconv.FormatInt(expiry.Unix(), 10)
	return strings.Join(parts, ".")
}

func flipLastByte(t *testing.T, state string) string {
	t.Helper()
	if state == "" {
		t.Fatal("empty state")
	}
	b := []byte(state)
	last := b[len(b)-1]
	if last == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	return string(b)
}
