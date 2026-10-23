package main

import "testing"

func TestParseOptionsSource(t *testing.T) {
	opts, _, err := parseOptions(nil)
	if err != nil || opts.source != "any" || opts.sourceSet {
		t.Fatalf("default options = %+v, %v", opts, err)
	}
	opts, _, err = parseOptions([]string{"-source", "window"})
	if err != nil || opts.source != "window" || !opts.sourceSet {
		t.Fatalf("window options = %+v, %v", opts, err)
	}
	if _, _, err := parseOptions([]string{"-source", "bogus"}); err == nil {
		t.Fatal("-source accepts an unknown source")
	}
}
