package store

import "testing"

func TestPaths(t *testing.T) {
	tests := []struct {
		w, r              []int
		overlap, replaces bool
	}{
		{w: nil, r: nil, overlap: true, replaces: true},
		{w: nil, r: []int{1}, overlap: true, replaces: true},
		{w: []int{1}, r: []int{1}, overlap: true, replaces: true},
		{w: []int{1}, r: []int{2}, overlap: false, replaces: false},
		{w: []int{1, 0}, r: []int{1}, overlap: true, replaces: false},
		{w: []int{Elem}, r: []int{Elem}, overlap: true, replaces: false},
		{w: []int{Elem, 1}, r: []int{constIndex(3), 1}, overlap: true, replaces: false},
		{w: []int{Elem, 1}, r: []int{constIndex(3), 2}, overlap: false, replaces: false},
		{w: []int{constIndex(0)}, r: []int{constIndex(1)}, overlap: false, replaces: false},
		{w: []int{constIndex(1)}, r: []int{constIndex(1)}, overlap: true, replaces: true},
		{w: []int{constIndex(1)}, r: []int{Elem}, overlap: true, replaces: false},
		{w: []int{Deref, 0}, r: []int{Deref, 0}, overlap: true, replaces: true},
	}
	for _, tt := range tests {
		if got := storeOverlap(tt.w, tt.r); got != tt.overlap {
			t.Errorf("storeOverlap(%v, %v) = %v, want %v", tt.w, tt.r, got, tt.overlap)
		}
		if got := storeReplaces(tt.w, tt.r); got != tt.replaces {
			t.Errorf("storeReplaces(%v, %v) = %v, want %v", tt.w, tt.r, got, tt.replaces)
		}
	}
}
