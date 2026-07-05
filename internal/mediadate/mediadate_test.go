package mediadate

import "testing"

func TestYear(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"2021-03-15", 2021},
		{"1999-12-31", 1999},
		{"", 0},
		{"abc", 0},
		{"2021", 2021},
	}
	for _, tc := range cases {
		if got := Year(tc.value); got != tc.want {
			t.Errorf("Year(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}
}
