package azure

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Paths of the fake APIs.
const (
	queryPath    = "/la/v1/workspaces/" + testWorkspaceID + "/query"
	activityPath = "/arm/subscriptions/" + testSub + "/providers/Microsoft.Insights/eventtypes/management/values"
	metricsPath  = "/arm" + testSite + "/providers/Microsoft.Insights/metrics"
	alertsPath   = "/arm/subscriptions/" + testSub + "/providers/Microsoft.AlertsManagement/alerts"
	statusPath   = "/arm" + testSite + "/providers/Microsoft.ResourceHealth/availabilityStatuses/current"
	statusesPath = "/arm/subscriptions/" + testSub + "/providers/Microsoft.ResourceHealth/availabilityStatuses"
	eventsPath   = "/arm/subscriptions/" + testSub + "/providers/Microsoft.ResourceHealth/events"
	clusterID    = "/subscriptions/" + testSub + "/resourceGroups/shop/providers/Microsoft.ContainerService/managedClusters/prod"
	clustersPath = "/arm/subscriptions/" + testSub + "/providers/Microsoft.ContainerService/managedClusters"
)

// Tokens the fake credential gives for each API.
const (
	armToken  = "Bearer token-for-" + armScope
	logsToken = "Bearer token-for-" + logAnalyticsScope
)

// The filter of list_changes over the last day.
const lastDayFilter = "%24filter=eventTimestamp+ge+%272026-05-03T10%3A00%3A00Z%27+and+eventTimestamp+le+%272026-05-04T10%3A00%3A00Z%27"

func TestSourceCall(t *testing.T) {
	t.Parallel()

	logsLink := source.Evidence{Kind: source.EvidenceLogQuery, Title: "Log Analytics workspace prod-logs",
		URL: "https://portal.azure.com/#resource" + testWorkspace + "/logs", At: testNow}
	table := `{"tables":[{"name":"PrimaryResult","columns":[{"name":"TimeGenerated","type":"datetime"},{"name":"Name","type":"string"},{"name":"ResultCode","type":"int"}],
		"rows":[["2026-05-04T09:58:00Z","POST /pay",500],["2026-05-04T09:57:00Z","POST /pay",503]]}]`

	tests := []struct {
		name   string
		tool   string
		input  string
		routes []route
		// wantToken is the Authorization header of every request.
		wantToken string
		// wantQueries are the encoded queries of the requests, in order.
		wantQueries []string
		// wantBodies are the JSON bodies of the requests, in order.
		wantBodies    []string
		want          string
		wantEvidence  []source.Evidence
		wantTruncated bool
		wantNoData    bool
		wantText      string
		wantToolError string
		wantErr       string
	}{
		{
			name:       "query_logs runs the query as given over the last hour",
			tool:       toolQueryLogs,
			input:      `{"query":"AppRequests | take 2"}`,
			routes:     []route{{http.MethodPost, queryPath, []fakehttp.Response{body(table + `}`)}}},
			wantToken:  logsToken,
			wantBodies: []string{`{"query":"AppRequests | take 2","timespan":"2026-05-04T09:00:00Z/2026-05-04T10:00:00Z"}`},
			want: `{"workspace":"prod-logs","tables":[{"name":"PrimaryResult","columns":["TimeGenerated","Name","ResultCode"],
				"rows":[["2026-05-04T09:58:00Z","POST /pay",500],["2026-05-04T09:57:00Z","POST /pay",503]]}]}`,
			wantEvidence: []source.Evidence{logsLink},
		},
		{
			name:  "query_logs cuts the rows to the limit and keeps the warning of a partial result",
			tool:  toolQueryLogs,
			input: `{"workspace":"PROD-LOGS","query":"AppRequests","limit":1}`,
			routes: []route{{http.MethodPost, queryPath, []fakehttp.Response{
				body(table + `,"error":{"code":"PartialError","message":"There were some errors when processing your query."}}`),
			}}},
			wantToken: logsToken,
			want: `{"workspace":"prod-logs","warning":"There were some errors when processing your query.",
				"tables":[{"name":"PrimaryResult","columns":["TimeGenerated","Name","ResultCode"],"rows":[["2026-05-04T09:58:00Z","POST /pay",500]]}]}`,
			wantEvidence:  []source.Evidence{logsLink},
			wantTruncated: true,
		},
		{
			name:         "query_logs with no rows is no data with a link to the workspace",
			tool:         toolQueryLogs,
			input:        `{"workspace":"` + testWorkspaceID + `","query":"AppRequests"}`,
			routes:       []route{{http.MethodPost, queryPath, []fakehttp.Response{body(`{"tables":[{"name":"PrimaryResult","columns":[{"name":"x"}],"rows":[]}]}`)}}},
			wantNoData:   true,
			wantText:     "The query returned no rows in workspace prod-logs between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{logsLink},
		},
		{
			name:  "a query Log Analytics cannot parse is a tool error with the inner message",
			tool:  toolQueryLogs,
			input: `{"query":"Foo |"}`,
			routes: []route{{http.MethodPost, queryPath, []fakehttp.Response{{Status: http.StatusBadRequest, Headers: jsonHeaders, Body: []byte(
				`{"error":{"message":"The request had some invalid properties","code":"BadArgumentError","innererror":{"code":"SyntaxError",
				"message":"A recognition error occurred.","innererror":{"code":"SYN0002","message":"Query could not be parsed at 'Foo'"}}}}`)}}}},
			wantToolError: "Azure answered 400 BadArgumentError: Query could not be parsed at 'Foo'",
		},
		{
			name:  "a permission the identity lacks is a tool error",
			tool:  toolQueryLogs,
			input: `{"query":"AppRequests"}`,
			routes: []route{{http.MethodPost, queryPath, []fakehttp.Response{
				failed(http.StatusForbidden, "InsufficientAccessError", "The provided credentials have insufficient access"),
			}}},
			wantToolError: "Azure answered 403 InsufficientAccessError: The provided credentials have insufficient access",
		},
		{
			name:    "a refused token is a failure of the source",
			tool:    toolQueryLogs,
			input:   `{"query":"AppRequests"}`,
			routes:  []route{{http.MethodPost, queryPath, []fakehttp.Response{failed(http.StatusUnauthorized, "InvalidAuthenticationToken", "expired")}}},
			wantErr: "Azure answered 401 InvalidAuthenticationToken: expired",
		},
		{
			name:    "a server error without a body is a failure of the source",
			tool:    toolQueryLogs,
			input:   `{"query":"AppRequests"}`,
			routes:  []route{{http.MethodPost, queryPath, []fakehttp.Response{{Status: http.StatusServiceUnavailable}}}},
			wantErr: "Azure answered 503",
		},
		{
			name:  "list_changes keeps the ended Administrative events, page by page",
			tool:  toolListChanges,
			input: `{}`,
			routes: []route{{http.MethodGet, activityPath, []fakehttp.Response{
				body(`{"value":[
					{"eventTimestamp":"2026-05-04T09:31:00Z","operationName":{"value":"Microsoft.Web/sites/write"},"status":{"value":"Started"},"category":{"value":"Administrative"}},
					{"eventTimestamp":"2026-05-04T09:30:00Z","operationName":{"value":"Microsoft.Web/sites/write","localizedValue":"Update website"},
					 "status":{"value":"Succeeded","localizedValue":"Succeeded"},"category":{"value":"Administrative"},"level":"Informational",
					 "caller":"deploy@example.com","resourceId":"` + testSite + `","correlationId":"c-1"}
				],"nextLink":"{arm}/subscriptions/` + testSub + `/providers/Microsoft.Insights/eventtypes/management/values?api-version=2015-04-01&$skipToken=p2"}`),
				body(`{"value":[
					{"eventTimestamp":"2026-05-04T09:10:00Z","operationName":{"value":"Microsoft.Web/sites/delete"},"status":{"value":"Failed"},
					 "subStatus":{"localizedValue":"Forbidden"},"category":{"value":"Administrative"},"level":"Error","caller":"ops@example.com","resourceId":"` + testSite + `"},
					{"eventTimestamp":"2026-05-04T09:05:00Z","operationName":{"value":"Microsoft.Authorization/policies/audit/action"},"status":{"value":"Succeeded"},"category":{"value":"Policy"}}
				]}`),
			}}},
			wantToken:   armToken,
			wantQueries: []string{lastDayFilter + "&api-version=2015-04-01", "%24skipToken=p2&api-version=2015-04-01"},
			want: `{"subscription":"` + testSub + `","changes":[
				{"time":"2026-05-04T09:30:00Z","operation":"Update website","status":"Succeeded","level":"Informational",
				 "caller":"deploy@example.com","resource":"` + testSite + `","correlationId":"c-1"},
				{"time":"2026-05-04T09:10:00Z","operation":"Microsoft.Web/sites/delete","status":"Failed","subStatus":"Forbidden",
				 "level":"Error","caller":"ops@example.com","resource":"` + testSite + `"}
			]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "Activity Log of /subscriptions/" + testSub,
				URL: "https://portal.azure.com/#resource/subscriptions/" + testSub + "/eventlogs", At: testNow}},
		},
		{
			name:        "list_changes of a resource group with no changes is no data",
			tool:        toolListChanges,
			input:       `{"resourceGroup":"shop"}`,
			routes:      []route{{http.MethodGet, activityPath, []fakehttp.Response{body(`{"value":[]}`)}}},
			wantQueries: []string{lastDayFilter + "+and+resourceGroupName+eq+%27shop%27&api-version=2015-04-01"},
			wantNoData:  true,
			wantText: "The Activity Log of /subscriptions/" + testSub + "/resourceGroups/shop has no changes between " +
				"2026-05-03T10:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "Activity Log of /subscriptions/" + testSub + "/resourceGroups/shop",
				URL: "https://portal.azure.com/#resource/subscriptions/" + testSub + "/resourceGroups/shop/eventlogs", At: testNow}},
		},
		{
			name:  "a next page link to another host is never followed",
			tool:  toolListChanges,
			input: `{"resource":"` + testSite + `"}`,
			routes: []route{{http.MethodGet, activityPath, []fakehttp.Response{
				body(`{"value":[],"nextLink":"https://attacker.example.com/steal"}`),
			}}},
			wantQueries: []string{lastDayFilter + "+and+resourceUri+eq+%27%2Fsubscriptions%2F" + testSub +
				"%2FresourceGroups%2Fshop%2Fproviders%2FMicrosoft.Web%2Fsites%2Fcheckout%27&api-version=2015-04-01"},
			wantErr: errForeignLink.Error(),
		},
		{
			name:  "query_metrics reads the metrics with an interval of at most 120 points",
			tool:  toolQueryMetrics,
			input: `{"resource":"` + testSite + `","metrics":["Requests","Http5xx"]}`,
			routes: []route{{http.MethodGet, metricsPath, []fakehttp.Response{body(`{"value":[
				{"name":{"value":"Requests","localizedValue":"Requests"},"unit":"Count","timeseries":[{"metadatavalues":[{"name":{"value":"instance"},"value":"i-1"}],
				 "data":[{"timeStamp":"2026-05-04T09:00:00Z","average":12.5},{"timeStamp":"2026-05-04T09:01:00Z"}]}]},
				{"name":{"value":"Http5xx"},"unit":"Count","timeseries":[{"data":[{"timeStamp":"2026-05-04T09:00:00Z","average":0}]}]}
			]}`)}}},
			wantToken: armToken,
			wantQueries: []string{"aggregation=Average&api-version=2024-02-01&interval=PT1M&metricnames=Requests%2CHttp5xx" +
				"&timespan=2026-05-04T09%3A00%3A00Z%2F2026-05-04T10%3A00%3A00Z&top=10"},
			want: `{"resource":"` + testSite + `","interval":"PT1M","series":[
				{"metric":"Requests","unit":"Count","dimensions":{"instance":"i-1"},"points":[{"timeStamp":"2026-05-04T09:00:00Z","average":12.5},{"timeStamp":"2026-05-04T09:01:00Z"}]},
				{"metric":"Http5xx","unit":"Count","points":[{"timeStamp":"2026-05-04T09:00:00Z","average":0}]}
			]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceMetric, Title: "Metrics of checkout",
				URL: "https://portal.azure.com/#resource" + testSite + "/metrics", At: testNow}},
		},
		{
			name:  "query_metrics without a value is no data",
			tool:  toolQueryMetrics,
			input: `{"resource":"` + testSite + `","metrics":["Requests"],"aggregation":"Total","interval":"PT5M","filter":"Instance eq '*'"}`,
			routes: []route{{http.MethodGet, metricsPath, []fakehttp.Response{body(`{"value":[{"name":{"value":"Requests"},
				"timeseries":[{"data":[{"timeStamp":"2026-05-04T09:00:00Z"}]}]}]}`)}}},
			wantQueries: []string{"%24filter=Instance+eq+%27%2A%27&aggregation=Total&api-version=2024-02-01&interval=PT5M&metricnames=Requests" +
				"&timespan=2026-05-04T09%3A00%3A00Z%2F2026-05-04T10%3A00%3A00Z&top=10"},
			wantNoData: true,
			wantText: "Azure Monitor has no points for those metrics of " + testSite +
				" between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{{Kind: source.EvidenceMetric, Title: "Metrics of checkout",
				URL: "https://portal.azure.com/#resource" + testSite + "/metrics", At: testNow}},
		},
		{
			name:  "list_alerts reads the fired alerts of the last day",
			tool:  toolListAlerts,
			input: `{"state":"fired"}`,
			routes: []route{{http.MethodGet, alertsPath, []fakehttp.Response{body(`{"value":[{
				"id":"/subscriptions/` + testSub + `/providers/Microsoft.AlertsManagement/alerts/a-1","name":"Checkout 5xx",
				"properties":{"essentials":{"severity":"Sev1","signalType":"Metric","alertState":"New","monitorCondition":"Fired",
				"monitorService":"Platform","targetResource":"` + testSite + `","targetResourceType":"microsoft.web/sites",
				"alertRule":"checkout-5xx","startDateTime":"2026-05-04T09:40:00Z","lastModifiedDateTime":"2026-05-04T09:41:00Z",
				"description":"Too many 5xx"}}}]}`)}}},
			wantToken: armToken,
			wantQueries: []string{"api-version=2023-07-12-preview&customTimeRange=2026-05-03T10%3A00%3A00Z%2F2026-05-04T10%3A00%3A00Z" +
				"&monitorCondition=Fired&pageCount=30&sortBy=startDateTime&sortOrder=desc"},
			want: `{"subscription":"` + testSub + `","alerts":[{"name":"Checkout 5xx","severity":"Sev1","state":"New","condition":"Fired",
				"service":"Platform","signal":"Metric","rule":"checkout-5xx","target":"` + testSite + `","targetType":"microsoft.web/sites",
				"description":"Too many 5xx","started":"2026-05-04T09:40:00Z","url":"` + alertURL + `"}]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "Alert Checkout 5xx", URL: alertURL, At: at(9, 40)}},
		},
		{
			name:          "a permission ARM refuses is a tool error",
			tool:          toolListAlerts,
			input:         `{}`,
			routes:        []route{{http.MethodGet, alertsPath, []fakehttp.Response{failed(http.StatusForbidden, "AuthorizationFailed", "The client does not have authorization")}}},
			wantToolError: "Azure answered 403 AuthorizationFailed: The client does not have authorization",
		},
		{
			name:  "get_resource_health of one resource",
			tool:  toolGetResourceHealth,
			input: `{"resource":"` + testSite + `"}`,
			routes: []route{{http.MethodGet, statusPath, []fakehttp.Response{body(`{"id":"` + testSite + `/providers/Microsoft.ResourceHealth/availabilityStatuses/current",
				"properties":{"availabilityState":"Degraded","title":"Degraded","summary":"Some requests fail","reasonType":"Unplanned",
				"occuredTime":"2026-05-04T09:20:00Z","reportedTime":"2026-05-04T09:55:00Z"}}`)}}},
			wantQueries: []string{"api-version=2025-05-01"},
			want: `{"resource":"` + testSite + `","state":"Degraded","title":"Degraded","summary":"Some requests fail","reason":"Unplanned",
				"since":"2026-05-04T09:20:00Z","reported":"2026-05-04T09:55:00Z"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "Resource Health of checkout",
				URL: "https://portal.azure.com/#resource" + testSite + "/resourceHealth"}},
		},
		{
			name:  "get_resource_health of the subscription lists what is not available and recent service events",
			tool:  toolGetResourceHealth,
			input: `{}`,
			routes: []route{
				{http.MethodGet, statusesPath, []fakehttp.Response{body(`{"value":[
					{"id":"` + testSite + `/providers/Microsoft.ResourceHealth/availabilityStatuses/current","properties":{"availabilityState":"Available"}},
					{"id":"/subscriptions/` + testSub + `/resourceGroups/shop/providers/Microsoft.Sql/servers/db/databases/orders/providers/microsoft.resourcehealth/availabilityStatuses/current",
					 "properties":{"availabilityState":"Unavailable","summary":"Database down","occuredTime":"2026-05-04T09:00:00Z"}}]}`)}},
				{http.MethodGet, eventsPath, []fakehttp.Response{body(`{"value":[
					{"name":"ABC-123","properties":{"title":"Azure SQL in West Europe","eventType":"ServiceIssue","status":"Active","level":"Warning",
					 "summary":"Connections fail","impactStartTime":"2026-05-04T08:50:00Z","lastUpdateTime":"2026-05-04T09:00:00Z",
					 "impact":[{"impactedService":"SQL Database","impactedRegions":[{"impactedRegion":"West Europe"},{"impactedRegion":"West Europe"}]}]}},
					{"name":"OLD-1","properties":{"title":"Old","status":"Resolved","lastUpdateTime":"2026-04-30T09:00:00Z"}}]}`)}},
			},
			wantQueries: []string{"api-version=2025-05-01", "api-version=2025-05-01&queryStartTime=5%2F3%2F2026"},
			want: `{"subscription":"` + testSub + `","unhealthy":[{"resource":"/subscriptions/` + testSub +
				`/resourceGroups/shop/providers/Microsoft.Sql/servers/db/databases/orders","state":"Unavailable","summary":"Database down","since":"2026-05-04T09:00:00Z"}],
				"serviceHealthEvents":[{"id":"ABC-123","title":"Azure SQL in West Europe","type":"ServiceIssue","status":"Active","level":"Warning",
				"services":["SQL Database"],"regions":["West Europe"],"started":"2026-05-04T08:50:00Z","updated":"2026-05-04T09:00:00Z","summary":"Connections fail"}]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "Azure service health", URL: serviceHealthPortal, At: testNow}},
		},
		{
			name:  "get_aks_cluster returns the cluster and its node pools",
			tool:  toolGetAKSCluster,
			input: `{"resourceGroup":"shop","cluster":"prod"}`,
			routes: []route{{http.MethodGet, "/arm" + clusterID, []fakehttp.Response{body(`{"id":"` + clusterID + `","name":"prod","location":"westeurope",
				"properties":{"provisioningState":"Upgrading","powerState":{"code":"Running"},"kubernetesVersion":"1.34.1","currentKubernetesVersion":"1.33.4",
				"nodeResourceGroup":"MC_shop_prod_westeurope","autoUpgradeProfile":{"upgradeChannel":"patch","nodeOSUpgradeChannel":"NodeImage"},
				"agentPoolProfiles":[{"name":"system","mode":"System","count":3,"vmSize":"Standard_D4s_v5","osType":"Linux","provisioningState":"Upgrading",
				"powerState":{"code":"Running"},"orchestratorVersion":"1.34.1","currentOrchestratorVersion":"1.33.4","enableAutoScaling":true,"minCount":3,"maxCount":6}]}}`)}}},
			wantQueries: []string{"api-version=2026-08-01"},
			want: `{"id":"` + clusterID + `","name":"prod","location":"westeurope","provisioningState":"Upgrading","powerState":"Running",
				"version":"1.33.4","targetVersion":"1.34.1","upgradeChannel":"patch","nodeOSUpgradeChannel":"NodeImage","nodeResourceGroup":"MC_shop_prod_westeurope",
				"nodePools":[{"name":"system","mode":"System","count":3,"vmSize":"Standard_D4s_v5","osType":"Linux","provisioningState":"Upgrading",
				"powerState":"Running","version":"1.33.4","autoscaling":true,"minCount":3,"maxCount":6,"targetVersion":"1.34.1"}]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "AKS cluster prod", URL: "https://portal.azure.com/#resource" + clusterID + "/overview"}},
		},
		{
			name:  "get_aks_cluster without a cluster lists the clusters",
			tool:  toolGetAKSCluster,
			input: `{}`,
			routes: []route{{http.MethodGet, clustersPath, []fakehttp.Response{body(`{"value":[{"id":"` + clusterID + `","name":"prod",
				"properties":{"provisioningState":"Succeeded","powerState":{"code":"Running"},"kubernetesVersion":"1.33.4","currentKubernetesVersion":"1.33.4"}}]}`)}}},
			want: `{"subscription":"` + testSub + `","clusters":[{"id":"` + clusterID + `","name":"prod","provisioningState":"Succeeded","powerState":"Running","version":"1.33.4"}]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "AKS clusters of subscription " + testSub,
				URL: "https://portal.azure.com/#browse/Microsoft.ContainerService%2FmanagedClusters"}},
		},
		{
			name:       "a cluster Azure does not have is no data",
			tool:       toolGetAKSCluster,
			input:      `{"resourceGroup":"shop","cluster":"prod"}`,
			routes:     []route{{http.MethodGet, "/arm" + clusterID, []fakehttp.Response{failed(http.StatusNotFound, "ResourceNotFound", "not found")}}},
			wantNoData: true,
			wantText:   notFoundText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			for _, r := range tt.routes {
				responses := make([]fakehttp.Response, 0, len(r.responses))
				for _, resp := range r.responses {
					responses = append(responses, withARM(resp, api.URL()+"/arm"))
				}

				api.Handle(r.method, r.path, responses...)
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api)

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			for i, req := range api.Requests() {
				if tt.wantToken != "" {
					require.Equal(t, tt.wantToken, req.Header.Get("Authorization"))
				}

				require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "sidekick/"))

				if i < len(tt.wantQueries) {
					require.Equal(t, tt.wantQueries[i], req.Query.Encode(), "query of request %d", i)
				}

				if i < len(tt.wantBodies) {
					require.JSONEq(t, tt.wantBodies[i], string(req.Body), "body of request %d", i)
				}
			}

			switch {
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)

				_, isToolErr := errors.AsType[*source.ToolError](err)
				require.False(t, isToolErr, "a failure of the source is not a tool error")
			case tt.wantNoData:
				require.NoError(t, err)
				require.True(t, res.NoData)
				require.Equal(t, tt.wantText, res.Text)
				require.Equal(t, tt.wantEvidence, res.Evidence)
				require.Empty(t, res.Structured)
			default:
				require.NoError(t, err)
				require.JSONEq(t, tt.want, string(res.Structured))
				require.Equal(t, tt.wantEvidence, res.Evidence)
				require.Equal(t, tt.wantTruncated, res.Truncated)
				require.False(t, res.NoData)
			}
		})
	}
}

func TestSourceCallInput(t *testing.T) {
	t.Parallel()

	otherSite := "/subscriptions/" + otherSub + "/resourceGroups/shop/providers/Microsoft.Web/sites/checkout"

	tests := []struct {
		name          string
		subscriptions []string
		workspaces    []Workspace
		tool          string
		input         string
		want          string
	}{
		{name: "a subscription the config does not list", tool: toolListChanges, input: `{"subscription":"` + otherSub + `"}`,
			want: `subscription "` + otherSub + `" is not one Sidekick may read; allowed: ` + testSub},
		{name: "no subscription with several in the config", subscriptions: []string{testSub, otherSub}, tool: toolListAlerts, input: `{}`,
			want: "give subscription; allowed: " + testSub + ", " + otherSub},
		{name: "a resource in another subscription", tool: toolQueryMetrics, input: `{"resource":"` + otherSite + `","metrics":["Requests"]}`,
			want: "resource: the resource is in subscription " + otherSub + ", which Sidekick may not read"},
		{name: "a resource ID that climbs out", tool: toolGetResourceHealth,
			input: `{"resource":"/subscriptions/` + testSub + `/resourceGroups/shop/providers/Microsoft.Web/sites/../../x"}`,
			want:  "is not an Azure resource ID"},
		{name: "a resource ID with a query", tool: toolGetResourceHealth, input: `{"resource":"` + testSite + `?api-version=x"}`,
			want: "is not an Azure resource ID"},
		{name: "a workspace the config does not list", tool: toolQueryLogs, input: `{"workspace":"other","query":"x"}`,
			want: `workspace "other" is not one Sidekick may read; allowed: prod-logs`},
		{name: "no workspace in the config", workspaces: []Workspace{}, tool: toolQueryLogs, input: `{"query":"x"}`,
			want: "no Log Analytics workspace is configured"},
		{name: "an empty query", tool: toolQueryLogs, input: `{"query":" "}`, want: "query: required"},
		{name: "a metric name with a comma", tool: toolQueryMetrics, input: `{"resource":"` + testSite + `","metrics":["a,b"]}`,
			want: `metrics: "a,b" is not a metric name`},
		{name: "no metric names", tool: toolQueryMetrics, input: `{"resource":"` + testSite + `","metrics":[]}`, want: "metrics: give 1 to 10"},
		{name: "a resource group and a resource", tool: toolListChanges, input: `{"resourceGroup":"shop","resource":"` + testSite + `"}`,
			want: "give resourceGroup or resource, not both"},
		{name: "a resource group with a quote", tool: toolListChanges, input: `{"resourceGroup":"shop' or 1 eq 1"}`,
			want: "is not a resource group name"},
		{name: "a cluster without a resource group", tool: toolGetAKSCluster, input: `{"cluster":"prod"}`, want: "resourceGroup: required with cluster"},
		{name: "a cluster name that climbs out", tool: toolGetAKSCluster, input: `{"resourceGroup":"shop","cluster":"../prod"}`,
			want: "is not an AKS cluster name"},
		{name: "a range longer than seven days", tool: toolListAlerts, input: `{"since":"2026-04-01T00:00:00Z"}`,
			want: "longer than the 168h0m0s one call may read"},
		{name: "input that does not decode", tool: toolListChanges, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, tt.subscriptions...)

			if tt.workspaces != nil {
				s.cfg.Workspaces = tt.workspaces
			}

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches Azure")
		})
	}
}

func TestSourceCallCutsLongResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rows int
		cell int
	}{
		{name: "a long cell is cut", rows: 1, cell: maxCellLen + 100},
		{name: "a result stops before it grows too large", rows: maxRows, cell: maxCellLen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := make([][]any, tt.rows)
			for i := range rows {
				rows[i] = []any{strings.Repeat("é", tt.cell/2)}
			}

			data, err := json.Marshal(map[string]any{"tables": []any{map[string]any{
				"name": "PrimaryResult", "columns": []any{map[string]any{"name": "Message"}}, "rows": rows,
			}}})
			require.NoError(t, err)

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, queryPath, fakehttp.Response{Headers: jsonHeaders, Body: data})

			s := newTestSource(t, api)

			res, err := s.Call(t.Context(), source.Call{Tool: toolQueryLogs, Input: json.RawMessage(`{"query":"x","limit":500}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)
			require.LessOrEqual(t, len(res.Structured), maxResultSize+tt.rows+len(`{"workspace":"prod-logs","tables":[{"name":"PrimaryResult","columns":["Message"],"rows":[]}]}`))

			var got logsResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.NotEmpty(t, got.Tables[0].Rows)

			for _, row := range got.Tables[0].Rows {
				text, ok := row[0].(string)
				require.True(t, ok)
				require.LessOrEqual(t, len(text), maxCellLen)
			}
		})
	}
}

func TestSourceTokenFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "the credential fails", err: errors.New("no token file specified")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api)
			s.client.tokens = newTokens(&fakeCredential{err: tt.err})

			_, err := s.Call(t.Context(), source.Call{Tool: toolListChanges, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, tt.err)

			_, isToolErr := errors.AsType[*source.ToolError](err)
			require.False(t, isToolErr)
			require.Empty(t, api.Requests(), "no request is sent without a token")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "delete_resource"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	secret := &registry.SecretRef{Name: "sidekick-azure", Key: "client-secret"}

	tests := []struct {
		name        string
		cfg         Config
		withSecrets bool
		wantErr     string
	}{
		{name: "a client secret without a secrets resolver", cfg: Config{Subscriptions: []string{testSub}, ClientSecret: secret},
			wantErr: errNoSecrets.Error()},
		{name: "settings that do not check", cfg: Config{Subscriptions: []string{testSub}, Settings: source.Settings{Concurrency: -1}},
			wantErr: "concurrency: must not be negative"},
		{name: "workload identity needs no secrets resolver", cfg: Config{Subscriptions: []string{testSub}}},
		{name: "a client secret with a secrets resolver", cfg: Config{Subscriptions: []string{testSub}, ClientSecret: secret}, withSecrets: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var deps registry.Deps
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "azure", tt.cfg, deps)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			tools, err := s.Tools(t.Context())
			require.NoError(t, err)
			require.Len(t, tools, len(toolList))
			require.NoError(t, s.Close())
		})
	}
}

// alertURL is the portal link of the test alert.
const alertURL = "https://portal.azure.com/#view/Microsoft_Azure_Monitoring_Alerts/AlertDetails.ReactView/alertId~/" +
	"%2Fsubscriptions%2F" + testSub + "%2Fproviders%2FMicrosoft.AlertsManagement%2Falerts%2Fa-1"
