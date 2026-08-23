package mysqlstore

import "testing"

func TestProjectionOutboxIdentityIncludesConfiguredVersion(t *testing.T) {
	a := projectionOutboxID("mem-1", "hash-1", "v1")
	b := projectionOutboxID("mem-1", "hash-1", "v1")
	c := projectionOutboxID("mem-1", "hash-1", "v2")
	if a != b || a == c || len(a) != 64 {
		t.Fatalf("ids=%q %q %q", a, b, c)
	}
}
