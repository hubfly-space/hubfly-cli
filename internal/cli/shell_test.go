package cli

import "testing"

func TestSplitAtDetach(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantData   string
		wantDetach bool
	}{
		{"no detach key", "ls -la\r", "ls -la\r", false},
		{"detach key alone", "\x1d", "", true},
		{"input before detach key", "ls\x1d", "ls", true},
		{"input after detach key dropped", "a\x1db", "a", true},
		{"ctrl-d still forwarded", "\x04", "\x04", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, detach := splitAtDetach([]byte(tt.in))
			if string(data) != tt.wantData || detach != tt.wantDetach {
				t.Fatalf("splitAtDetach(%q) = (%q, %v), want (%q, %v)", tt.in, data, detach, tt.wantData, tt.wantDetach)
			}
		})
	}
}
