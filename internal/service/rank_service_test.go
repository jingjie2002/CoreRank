package service

import (
	"errors"
	"testing"
)

func TestNormalizeLeaderboardType(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "default", input: "", want: "global"},
		{name: "trim lower", input: " Season:SS25 ", want: "season:ss25"},
		{name: "invalid", input: "season ss25", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeLeaderboardType(tt.input)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidLeaderboardType) {
					t.Fatalf("expected invalid leaderboard error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize leaderboard type: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
