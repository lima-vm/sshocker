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

func TestConvertMSYS2Path(t *testing.T) {
	inputPath := "/c/Users/lts"
	expectedPath := "C:\\Users\\lts"

	actualPath := convertMSYS2Path(inputPath)

	if actualPath != expectedPath {
		t.Errorf("Conversion failed: expected %q, got %q", expectedPath, actualPath)
	} else {
		t.Logf("Success! Converted path for native Windows OpenSSH: %q", actualPath)
	}
}
