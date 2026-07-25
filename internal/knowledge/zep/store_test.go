package zep

// The knowledge port promises a whole graph, so draining Zep's 50-item pages
// is this adapter's obligation. drain is the only real logic here — the rest of
// the file is field mapping — and it is exercised directly with a fake fetch
// function: no Zep account, no HTTP, so these run everywhere.

import (
	"errors"
	"fmt"
	"testing"
)

// pagedFetch replays a fixed sequence of pages and records every cursor it was
// called with, so a test can assert the cursor actually advanced.
type pagedFetch struct {
	pages   [][]string
	cursors []*string
}

func (p *pagedFetch) fetch(cursor *string) ([]string, error) {
	p.cursors = append(p.cursors, cursor)
	if len(p.cursors) > len(p.pages) {
		return nil, nil
	}
	return p.pages[len(p.cursors)-1], nil
}

func page(prefix string, n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return items
}

func identity(s string) string { return s }

func TestDrain(t *testing.T) {
	tests := []struct {
		name        string
		pages       [][]string
		wantItems   int
		wantFetches int
	}{
		{
			name:        "no graph yet",
			pages:       [][]string{{}},
			wantItems:   0,
			wantFetches: 1,
		},
		{
			// A short page cannot be followed by another, so stop without
			// spending a round trip to learn that.
			name:        "single short page",
			pages:       [][]string{page("a", 3)},
			wantItems:   3,
			wantFetches: 1,
		},
		{
			name:        "full page then partial",
			pages:       [][]string{page("a", graphPageLimit), page("b", 7)},
			wantItems:   graphPageLimit + 7,
			wantFetches: 2,
		},
		{
			// Exact multiple of the page size: the extra empty fetch is the
			// only way to know the graph ended on a boundary.
			name:        "full page then empty",
			pages:       [][]string{page("a", graphPageLimit), {}},
			wantItems:   graphPageLimit,
			wantFetches: 2,
		},
		{
			name:        "three pages",
			pages:       [][]string{page("a", graphPageLimit), page("b", graphPageLimit), page("c", 1)},
			wantItems:   2*graphPageLimit + 1,
			wantFetches: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &pagedFetch{pages: tt.pages}
			got, err := drain(p.fetch, identity)
			if err != nil {
				t.Fatalf("drain() error = %v", err)
			}
			if len(got) != tt.wantItems {
				t.Errorf("drain() returned %d items, want %d", len(got), tt.wantItems)
			}
			if len(p.cursors) != tt.wantFetches {
				t.Errorf("drain() made %d fetches, want %d", len(p.cursors), tt.wantFetches)
			}
			if len(p.cursors) > 0 && p.cursors[0] != nil {
				t.Errorf("first fetch cursor = %q, want nil", *p.cursors[0])
			}
		})
	}
}

// The cursor is the UUID of a page's last item; Zep resumes after it.
func TestDrainAdvancesCursor(t *testing.T) {
	first := page("a", graphPageLimit)
	second := page("b", graphPageLimit)
	p := &pagedFetch{pages: [][]string{first, second, {}}}

	if _, err := drain(p.fetch, identity); err != nil {
		t.Fatalf("drain() error = %v", err)
	}
	if len(p.cursors) != 3 {
		t.Fatalf("drain() made %d fetches, want 3", len(p.cursors))
	}
	if p.cursors[1] == nil || *p.cursors[1] != first[len(first)-1] {
		t.Errorf("second fetch cursor = %v, want %q", p.cursors[1], first[len(first)-1])
	}
	if p.cursors[2] == nil || *p.cursors[2] != second[len(second)-1] {
		t.Errorf("third fetch cursor = %v, want %q", p.cursors[2], second[len(second)-1])
	}
}

// A backend that ignores the cursor would otherwise loop forever handing back
// the same page. Detecting an unmoved cursor stops it on the next fetch rather
// than after maxGraphPages of wasted calls.
func TestDrainStopsWhenCursorDoesNotAdvance(t *testing.T) {
	stuck := page("a", graphPageLimit)
	fetches := 0
	got, err := drain(func(cursor *string) ([]string, error) {
		fetches++
		if fetches > maxGraphPages+1 {
			t.Fatal("drain() did not terminate")
		}
		return stuck, nil
	}, identity)
	if err != nil {
		t.Fatalf("drain() error = %v", err)
	}
	if fetches != 2 {
		t.Errorf("drain() made %d fetches, want 2", fetches)
	}
	if len(got) != 2*graphPageLimit {
		t.Errorf("drain() returned %d items, want %d", len(got), 2*graphPageLimit)
	}
}

// The page cap is the last-resort bound: a backend that keeps handing back full
// pages with fresh UUIDs still terminates.
func TestDrainCapsAtMaxPages(t *testing.T) {
	fetches := 0
	got, err := drain(func(cursor *string) ([]string, error) {
		fetches++
		return page(fmt.Sprintf("p%d", fetches), graphPageLimit), nil
	}, identity)
	if err != nil {
		t.Fatalf("drain() error = %v", err)
	}
	if fetches != maxGraphPages {
		t.Errorf("drain() made %d fetches, want %d", fetches, maxGraphPages)
	}
	if len(got) != maxGraphPages*graphPageLimit {
		t.Errorf("drain() returned %d items, want %d", len(got), maxGraphPages*graphPageLimit)
	}
}

// A mid-drain failure yields no partial graph: half a graph is worse than an
// error, because the caller cannot tell it is half.
func TestDrainPropagatesError(t *testing.T) {
	wantErr := errors.New("zep unavailable")
	fetches := 0
	got, err := drain(func(cursor *string) ([]string, error) {
		fetches++
		if fetches == 2 {
			return nil, wantErr
		}
		return page("a", graphPageLimit), nil
	}, identity)
	if !errors.Is(err, wantErr) {
		t.Fatalf("drain() error = %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Errorf("drain() returned %d items alongside an error, want none", len(got))
	}
}
