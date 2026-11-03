package awsauth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsoleRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		region string
		want   string
	}{
		{region: "us-east-1", want: "https://us-east-1.console.aws.amazon.com"},
		{region: "cn-north-1", want: "https://cn-north-1.console.amazonaws.cn"},
		{region: "us-gov-west-1", want: "https://us-gov-west-1.console.amazonaws-us-gov.com"},
	}

	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, ConsoleRoot(tt.region))
		})
	}
}
