package azure

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestCheckScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		// want is part of the tool error; empty means the query is allowed.
		want string
	}{
		{name: "a query of the workspace's own tables", query: "AppRequests | where Success == false | summarize count() by bin(TimeGenerated, 5m)"},
		{name: "a function whose name only starts like one", query: "AppRequests | summarize arg_max(TimeGenerated, *) by AppRoleName"},
		{name: "a column named like a function, not called", query: "AzureActivity | project resource = ResourceId, app = AppId"},
		{name: "another workspace", query: `workspace("other").AppRequests`, want: "workspace() reads outside the workspace"},
		{name: "another workspace with a space", query: `workspace ("other").AppRequests`, want: "workspace() reads outside the workspace"},
		{name: "another workspace with a tab and a line break", query: "union workspace\t\n(\"other\").Heartbeat", want: "workspace() reads outside"},
		{name: "upper case", query: `WORKSPACE("other").AppRequests`, want: "workspace() reads outside"},
		{name: "an Application Insights app", query: `app("checkout").requests`, want: "app() reads outside"},
		{name: "an Azure resource", query: `resource("/subscriptions/x/resourceGroups/y").AzureMetrics`, want: "resource() reads outside"},
		{name: "Azure Data Explorer", query: `adx("https://help.kusto.windows.net/Samples").StormEvents`, want: "adx() reads outside"},
		{name: "Azure Resource Graph", query: `arg("").Resources | take 10`, want: "arg() reads outside"},
		{name: "a cluster", query: `cluster("help").database("Samples").StormEvents`, want: "cluster() reads outside"},
		{name: "a database", query: `Database("Samples").StormEvents`, want: "database() reads outside"},
		{name: "externaldata", query: "externaldata (Name: string) [@\"https://example.com/data.csv\"]", want: "externaldata fetches data"},
		{name: "externaldata in mixed case", query: "ExternalData(x: string) [h@'https://example.com']", want: "externaldata fetches data"},
		{name: "a function name inside a string literal is refused too", query: `AppTraces | where Message has "workspace(prod)"`,
			want: "workspace() reads outside"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkScope(tt.query)
			if tt.want == "" {
				require.NoError(t, err)

				return
			}

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Contains(t, toolErr.Message, "each call reads one configured workspace")
		})
	}
}
