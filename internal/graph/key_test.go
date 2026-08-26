package graph

import "testing"

func TestKeyString(t *testing.T) {
	cases := []struct {
		key  Key
		want string
	}{
		{Key{Type: "example.com/store.Store"}, "example.com/store.Store"},
		{Key{Type: "example.com/store.Store", Tag: "primary"}, "example.com/store.Store#primary"},
	}
	for _, c := range cases {
		if got := c.key.String(); got != c.want {
			t.Errorf("Key%+v.String() = %q, want %q", c.key, got, c.want)
		}
	}
}
