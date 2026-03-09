package linksocks

import "testing"

func TestFormatBatchProgressSuffix(t *testing.T) {
	tests := []struct {
		name  string
		count int
		total int
		want  string
	}{
		{name: "hide zero total", count: 0, total: 0, want: ""},
		{name: "hide single total", count: 1, total: 1, want: ""},
		{name: "show multi total", count: 1, total: 3, want: " (1/3)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBatchProgressSuffix(tt.count, tt.total); got != tt.want {
				t.Fatalf("formatBatchProgressSuffix(%d, %d) = %q, want %q", tt.count, tt.total, got, tt.want)
			}
		})
	}
}