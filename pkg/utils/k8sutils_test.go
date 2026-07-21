package utils

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestAdjustCPU(t *testing.T) {
	tests := []struct {
		name         string
		cpu          string
		adjustFactor float64
		expected     string
	}{
		{
			name:         "halves millicpu request",
			cpu:          "100m",
			adjustFactor: 0.5,
			expected:     "50m",
		},
		{
			name:         "truncates fractional millicpu",
			cpu:          "25m",
			adjustFactor: 0.5,
			expected:     "12m",
		},
		{
			name:         "halves whole cpu value",
			cpu:          "1",
			adjustFactor: 0.5,
			expected:     "500m",
		},
		{
			name:         "scales with custom factor",
			cpu:          "100m",
			adjustFactor: 0.25,
			expected:     "25m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdjustCPU(resource.MustParse(tt.cpu), tt.adjustFactor)
			expected := resource.MustParse(tt.expected)
			if got.Cmp(expected) != 0 {
				t.Fatalf("expected %s, got %s", expected.String(), got.String())
			}
		})
	}
}
