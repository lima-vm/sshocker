package reversesshfs

import (
	"runtime"
	"testing"
)

func TestAddQuotes(t *testing.T) {
	type testCase struct {
		input    string
		expected string
	}
	var testCases []testCase
	if runtime.GOOS != "windows" {
		testCases = []testCase{
			{
				input:    "/user/test/path",
				expected: "\"/user/test/path\"",
			},
		}
	} else {
		testCases = []testCase{
			{
				input:    "/user/test/path",
				expected: "'/user/test/path'",
			},
		}
	}
	for i, tc := range testCases {
		got := addQuotes(tc.input)
		if got != tc.expected {
			t.Errorf("#%d: expected %q, got %q", i, tc.expected, got)
		}
	}
}
