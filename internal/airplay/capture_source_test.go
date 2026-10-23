package airplay

import "testing"

func TestOfferedSourceTypes(t *testing.T) {
	any := SourceMonitor | SourceWindow
	for _, test := range []struct {
		requested, available, want uint32
	}{
		{any, any, any},
		{any, SourceMonitor, SourceMonitor},          // backend without window capture
		{SourceWindow, SourceMonitor, SourceMonitor}, // nothing requested is available
		{SourceMonitor, any | 4, SourceMonitor},
	} {
		if got := offeredSourceTypes(test.requested, test.available); got != test.want {
			t.Errorf("offeredSourceTypes(%d, %d) = %d, want %d", test.requested, test.available, got, test.want)
		}
	}
}
