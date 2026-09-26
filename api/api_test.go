package api

import (
	"reflect"
	"testing"
	"unsafe"
)

func TestAuditUTF16Pointer(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []uint16
	}{
		{"", []uint16{0}},
		{"a", []uint16{'a', 0}},
		{"é", []uint16{0xe9, 0}},
		{"🦋", []uint16{0xd83e, 0xdd8b, 0}},
	} {
		p := StringToUTF16Ptr(tc.text)
		if p == nil {
			t.Fatalf("nil UTF-16 pointer for %q", tc.text)
		}
		if got := unsafe.Slice(p, len(tc.want)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("UTF-16 for %q: %x; want %x", tc.text, got, tc.want)
		}
	}
}
