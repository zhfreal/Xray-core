package singmux

import (
	"testing"
)

func TestStringToBps(t *testing.T) {
	tests := []struct {
		input string
		want  uint64
	}{
		{"", 0},
		{"10", 1250000}, // 10 Mbps = 10 * 1000 * 1000 / 8 = 1250000 bytes/sec
		{"100", 12500000},
		{"10 Mbps", 1250000},
		{"10 Mbps", 1250000},
		{"1 Gbps", 125000000}, // 1 Gbps = 1000 * 1000 * 1000 / 8 = 125,000,000 bytes/sec
		{"100 Kbps", 12500},
		{"8 bps", 1},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := StringToBps(tt.input)
		if got != tt.want {
			t.Errorf("StringToBps(%q) = %d; want %d", tt.input, got, tt.want)
		}
	}
}
