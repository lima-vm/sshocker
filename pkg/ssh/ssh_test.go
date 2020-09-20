package ssh

import "testing"

func TestParseScriptInterpreter(t *testing.T) {
	type testCase struct {
		script   string
		expected string
	}
	testCases := []testCase{
		{
			script: `#!/bin/sh
echo "Hello world"
		`,
			expected: "/bin/sh",
		},
		{
			script: `echo "Hello world"
		`,
			expected: "",
		},
	}
	for i, tc := range testCases {
		got, err := parseScriptInterpreter(tc.script)
		if tc.expected != "" {
			if err != nil {
				t.Errorf("#%d: %v", i, err)
			}
			if got != tc.expected {
				t.Errorf("#%d: expected %q, got %q", i, tc.expected, got)
			}
		} else {
			if err == nil {
				t.Errorf("#%d: expected error", i)
			}
		}
	}
}
