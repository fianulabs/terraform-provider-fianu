package base

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatID locks down the composite ID format that ends up in state and
// in `terraform state list`. Changing it is a state-format break.
func TestFormatID(t *testing.T) {
	assert.Equal(t, "control/payment-sast", FormatID("control", "payment-sast"))
	assert.Equal(t, "policy/standard.coverage", FormatID("policy", "standard.coverage"))
}

// TestParseID covers the import paths users hit:
//   - composite form (the format FormatID emits)
//   - bare entity_key (legacy / typed by humans)
//   - mismatched type (clear error)
func TestParseID(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		expectedType string
		wantKey      string
		wantErr      bool
	}{
		{name: "composite_matches", raw: "control/payment-sast", expectedType: "control", wantKey: "payment-sast"},
		{name: "bare_key_accepted", raw: "payment-sast", expectedType: "control", wantKey: "payment-sast"},
		{name: "type_mismatch", raw: "policy/foo", expectedType: "control", wantErr: true},
		{name: "composite_with_slash_in_key", raw: "control/path/with/slashes", expectedType: "control", wantKey: "path/with/slashes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseID(tt.raw, tt.expectedType)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, got)
		})
	}
}
