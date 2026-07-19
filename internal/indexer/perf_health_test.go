package indexer

import "testing"

func TestWALHealthThreshold(t *testing.T) {
	for _, test := range []struct {
		db, wal float64
		want    bool
	}{
		{db: 1500, wal: 345, want: true},
		{db: 2000, wal: 257, want: true},
		{db: 2000, wal: 200, want: false},
		{db: 100, wal: 21, want: true},
	} {
		if got := walHealthDegraded(test.db, test.wal); got != test.want {
			t.Fatalf("walHealthDegraded(%v,%v)=%v want=%v", test.db, test.wal, got, test.want)
		}
	}
}
