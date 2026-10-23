package main

import "testing"

func TestParseOptionsSource(t *testing.T) {
	opts, _, err := parseOptions(nil)
	if err != nil || opts.source != "monitor" || opts.sourceSet {
		t.Fatalf("default options = %+v, %v", opts, err)
	}
	opts, _, err = parseOptions([]string{"-source", "window"})
	if err != nil || opts.source != "window" || !opts.sourceSet {
		t.Fatalf("window options = %+v, %v", opts, err)
	}
	if _, _, err := parseOptions([]string{"-source", "any"}); err == nil {
		t.Fatal("the removed Ask source is still accepted")
	}
}
