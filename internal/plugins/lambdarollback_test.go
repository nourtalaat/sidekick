package plugins_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
)

// lambdaRollbackConfig is a config with the real plugin types, a Lambda rollback
// action and one route. The route's mode and the action's extra keys are
// filled in.
const lambdaRollbackConfig = `
database: {urlSecret: {name: sidekick-db, key: url}}
model:
  connectors:
    claude: {provider: anthropic, model: claude-sonnet-5-5, apiKeySecret: {name: sidekick-anthropic, key: apiKey}}
  roles: {investigate: claude}
intake:
  webhooks:
    alertmanager:
      adapter: alertmanager
      auth: {bearer: {name: sidekick-hooks, key: alertmanager}}
      trusted: true
actions:
  lambda-rollback:
    type: aws.lambda_rollback
    accounts: [{id: "111122223333", roleARN: "arn:aws:iam::111122223333:role/sidekick-lambda-rollback"}]
    regions: [us-east-1]
    limits: {functions: [checkout], aliases: [live]}
%s
routes:
  - {name: ops, match: {intake: [alertmanager]}, mode: %s, actions: [lambda-rollback]}
  - {name: default}
`

func TestLambdaRollbackIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		extra   string
		mode    string
		wantErr string
	}{
		{name: "a read-write route without the opt-in", mode: "read-write", wantErr: `action "lambda-rollback" (type aws.lambda_rollback) is approval-only`},
		{name: "a read-write route with the opt-in turned off", extra: "    allowReadWrite: false", mode: "read-write", wantErr: "is approval-only"},
		{name: "a read-write route with the opt-in", extra: "    allowReadWrite: true", mode: "read-write"},
		{name: "an approved route", mode: "approved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			yaml := []byte(fmt.Sprintf(lambdaRollbackConfig, tt.extra, tt.mode))

			_, err := config.Parse(t.Context(), yaml, config.LoadOptions{})
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
