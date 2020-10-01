package main

import "testing"

func TestParseFlagP(t *testing.T) {
	testCases := map[string]string{
		"80":                          "0.0.0.0:80:localhost:80",
		"8080:80":                     "0.0.0.0:8080:localhost:80",
		"127.0.0.1:8080:80":           "127.0.0.1:8080:localhost:80",
		"127.0.0.1:8080:127.0.0.1:80": "",
	}
	for k, v := range testCases {
		got, err := parseFlagP(k)
		if v == "" {
			if err == nil {
				t.Errorf("error is expected for %q", k)
			}
			continue
		}
		if err != nil {
			t.Errorf("failed to parse %q: %v", k, err)
			continue
		}
		if got != v {
			t.Errorf("expected %q, got %q for %q", v, got, k)
		}
	}
}
