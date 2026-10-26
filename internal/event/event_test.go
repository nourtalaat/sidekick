package event

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

type fingerprintArgs struct {
	senderType string
	alertName  string
	labels     map[string]string
	keys       []string
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	base := fingerprintArgs{
		senderType: "alertmanager",
		alertName:  "HighErrorRate",
		labels:     map[string]string{"service": "checkout", "env": "prod", "pod": "checkout-1"},
		keys:       []string{"service", "env"},
	}

	tests := []struct {
		name     string
		a        fingerprintArgs
		b        fingerprintArgs
		wantSame bool
	}{
		{
			name:     "same input gives the same fingerprint",
			a:        base,
			b:        base,
			wantSame: true,
		},
		{
			name:     "key order does not matter",
			a:        base,
			b:        fingerprintArgs{base.senderType, base.alertName, base.labels, []string{"env", "service"}},
			wantSame: true,
		},
		{
			name:     "labels not named in keys are ignored",
			a:        base,
			b:        fingerprintArgs{base.senderType, base.alertName, map[string]string{"service": "checkout", "env": "prod", "pod": "checkout-2"}, base.keys},
			wantSame: true,
		},
		{
			name:     "a missing label counts as empty",
			a:        fingerprintArgs{base.senderType, base.alertName, map[string]string{"service": "checkout"}, base.keys},
			b:        fingerprintArgs{base.senderType, base.alertName, map[string]string{"service": "checkout", "env": ""}, base.keys},
			wantSame: true,
		},
		{
			name:     "a different label value changes the fingerprint",
			a:        base,
			b:        fingerprintArgs{base.senderType, base.alertName, map[string]string{"service": "checkout", "env": "staging"}, base.keys},
			wantSame: false,
		},
		{
			name:     "a different alert name changes the fingerprint",
			a:        base,
			b:        fingerprintArgs{base.senderType, "HighLatency", base.labels, base.keys},
			wantSame: false,
		},
		{
			name:     "a different sender type changes the fingerprint",
			a:        base,
			b:        fingerprintArgs{"generic", base.alertName, base.labels, base.keys},
			wantSame: false,
		},
		{
			name:     "moving text between sender type and alert name changes the fingerprint",
			a:        fingerprintArgs{"ab", "c", nil, nil},
			b:        fingerprintArgs{"a", "bc", nil, nil},
			wantSame: false,
		},
		{
			name:     "an extra key changes the fingerprint",
			a:        base,
			b:        fingerprintArgs{base.senderType, base.alertName, base.labels, []string{"service", "env", "pod"}},
			wantSame: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keysA := append([]string(nil), tt.a.keys...)

			got := Fingerprint(tt.a.senderType, tt.a.alertName, tt.a.labels, tt.a.keys)
			other := Fingerprint(tt.b.senderType, tt.b.alertName, tt.b.labels, tt.b.keys)

			require.Len(t, got, hex.EncodedLen(fingerprintBytes))
			_, err := hex.DecodeString(got)
			require.NoError(t, err)
			require.Equal(t, keysA, tt.a.keys, "Fingerprint must not reorder the caller's keys")

			if tt.wantSame {
				require.Equal(t, got, other)

				return
			}

			require.NotEqual(t, got, other)
		})
	}
}
