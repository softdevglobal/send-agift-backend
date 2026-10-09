package services

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"0771234567", "+94771234567", true},
		{"+94 77 123 4567", "+94771234567", true},
		{"0094771234567", "+94771234567", true},
		{"+61 412 345 678", "+61412345678", true},
		{"12", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, err := NormalizePhone(c.raw, "+94")
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q ok=%v", c.raw, got, err, c.want, c.ok)
		}
	}
}
