package pgstore

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// migrationByID returns the registered migration with the given ID.
func migrationByID(t *testing.T, id string) migration {
	t.Helper()
	for _, m := range schemaMigrations {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("migration %s is not registered", id)
	return migration{}
}

// sqlInListLiterals extracts the single-quoted literals of the first
// `event_type IN (...)` predicate in the SQL.
func sqlInListLiterals(t *testing.T, sql string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)event_type IN \(([^)]*)\)`).FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("no event_type IN (...) predicate in %q", sql)
	}
	lit := regexp.MustCompile(`'([^']*)'`)
	var out []string
	for _, g := range lit.FindAllStringSubmatch(m[1], -1) {
		out = append(out, g[1])
	}
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestActivityPartialIndexPredicatesLockstepWithStoreTypeLists enforces the
// cross-package contract the #1077-item-7 indexes depend on: partial-index
// matching requires the query predicate to IMPLY the index predicate, and
// the store queries inline their Go type lists as SQL literals to make that
// provable. If store.LifecycleEventTypes or the unread type lists change
// without the same PR updating migrations 0153/0154 (a NEW migration —
// applied migrations are checksum-immutable), the indexes silently stop
// matching and every lifecycle event regresses to the unindexed scan this
// work removed. This test turns that silent regression into a compile-time
// red.
func TestActivityPartialIndexPredicatesLockstepWithStoreTypeLists(t *testing.T) {
	lifecycle := sqlInListLiterals(t, migrationByID(t, "0153").SQL)
	if got, want := sortedCopy(lifecycle), sortedCopy(store.LifecycleEventTypes); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("migration 0153 predicate %v != store.LifecycleEventTypes %v — ship a follow-up index migration in the same PR", got, want)
	}

	unread := sqlInListLiterals(t, migrationByID(t, "0154").SQL)
	union := map[string]bool{}
	for _, v := range store.UnreadOutputItemTypes {
		union[v] = true
	}
	for _, v := range store.UnreadOutputTurnTypes {
		union[v] = true
	}
	var want []string
	for v := range union {
		want = append(want, v)
	}
	if got, wantSorted := sortedCopy(unread), sortedCopy(want); strings.Join(got, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("migration 0154 predicate %v != UnreadOutputItemTypes ∪ UnreadOutputTurnTypes %v — ship a follow-up index migration in the same PR", got, wantSorted)
	}
}
