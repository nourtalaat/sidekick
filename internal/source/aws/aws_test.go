package aws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/awsauth"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The X-Amz-Target of each operation the tests answer.
const (
	targetStartQuery   = "Logs_20140328.StartQuery"
	targetQueryResults = "Logs_20140328.GetQueryResults"
	targetStopQuery    = "Logs_20140328.StopQuery"
	targetLogGroups    = "Logs_20140328.DescribeLogGroups"
	targetLookupEvents = "CloudTrail_20131101.LookupEvents"
)

// Console links of the tests.
const (
	trailConsole = "https://us-east-1.console.aws.amazon.com/cloudtrailv2/home?region=us-east-1#/events"
	insightsLink = "https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logsV2:logs-insights$3FqueryDetail$3D"
)

// The last day and the last hour before testNow, as LookupEvents and
// StartQuery take them.
const (
	lastDay  = `"StartTime":1777802400,"EndTime":1777888800`
	lastHour = `"startTime":1777885200,"endTime":1777888800`
)

func TestSourceCall(t *testing.T) {
	t.Parallel()

	checkoutQuery := "fields @timestamp, @message | filter @message like /ERROR/"
	checkoutLink := source.Evidence{
		Kind:  source.EvidenceLogQuery,
		Title: "Logs Insights query in account " + testAccount + ", Region us-east-1",
		URL: insightsLink + "~(end~'2026-05-04T10*3a00*3a00.000Z~start~'2026-05-04T09*3a00*3a00.000Z~timeType~'ABSOLUTE~tz~'UTC" +
			"~editorString~'fields*20*40timestamp*2c*20*40message*20*7c*20filter*20*40message*20like*20*2fERROR*2f~source~(~'*2fecs*2fcheckout))",
		At: testNow,
	}
	checkoutInput := `{"logGroups":["/ecs/checkout"],"query":"` + checkoutQuery + `","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`
	historyLink := source.Evidence{Kind: source.EvidenceDeploy, Title: "CloudTrail event history of account " + testAccount + ", Region us-east-1",
		URL: trailConsole + "?ReadOnly=false", At: testNow}

	tests := []struct {
		name  string
		edit  func(*Config)
		tool  string
		input string
		logs  []fakehttp.Response
		trail []fakehttp.Response
		// wantRegion is the Region every request is signed for. Empty
		// means testRegion.
		wantRegion string
		// wantTargets are the X-Amz-Target headers of the requests, in
		// order.
		wantTargets []string
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
			name:  "query_logs starts the query, polls it until it is complete and leaves out @ptr",
			tool:  toolQueryLogs,
			input: checkoutInput,
			logs: []fakehttp.Response{
				answer(`{"queryId":"q-1"}`),
				answer(`{"status":"Running","results":[]}`),
				answer(`{"status":"Complete","results":[
					[{"field":"@timestamp","value":"2026-05-04 09:58:00.000"},{"field":"@message","value":"ERROR pay failed"},{"field":"@ptr","value":"p-1"}],
					[{"field":"@timestamp","value":"2026-05-04 09:57:00.000"},{"field":"@ptr","value":"p-2"}]],
					"statistics":{"recordsMatched":2,"recordsScanned":100,"bytesScanned":2048}}`),
			},
			wantTargets: []string{targetStartQuery, targetQueryResults, targetQueryResults},
			wantBodies: []string{
				`{"logGroupNames":["/ecs/checkout"],"queryString":"` + checkoutQuery + `","queryLanguage":"CWLI",` + lastHour + `,"limit":100}`,
				`{"queryId":"q-1"}`,
				`{"queryId":"q-1"}`,
			},
			want: `{"account":"` + testAccount + `","region":"us-east-1","logGroups":["/ecs/checkout"],"fields":["@timestamp","@message"],
				"rows":[["2026-05-04 09:58:00.000","ERROR pay failed"],["2026-05-04 09:57:00.000",null]],
				"statistics":{"recordsMatched":2,"recordsScanned":100,"bytesScanned":2048}}`,
			wantEvidence: []source.Evidence{checkoutLink},
		},
		{
			name:  "query_logs expands a prefix in another Region and says the rows were cut at the limit",
			edit:  func(c *Config) { c.Regions = []string{testRegion, otherRegion} },
			tool:  toolQueryLogs,
			input: `{"region":"eu-west-1","logGroups":["/aws/eks/prod/*"],"query":"fields @message","since":"2026-05-04T09:30:00Z","until":"2026-05-04T10:00:00Z","limit":1}`,
			logs: []fakehttp.Response{
				answer(`{"logGroups":[{"logGroupName":"/aws/eks/prod/app"},{"logGroupName":"/aws/eks/prod/db"}]}`),
				answer(`{"queryId":"q-2"}`),
				answer(`{"status":"Complete","results":[[{"field":"@message","value":"panic"}]],"statistics":{"recordsMatched":40,"recordsScanned":900,"bytesScanned":4096}}`),
			},
			wantRegion:  otherRegion,
			wantTargets: []string{targetLogGroups, targetStartQuery, targetQueryResults},
			wantBodies: []string{
				`{"logGroupNamePrefix":"/aws/eks/prod/"}`,
				`{"logGroupNames":["/aws/eks/prod/app","/aws/eks/prod/db"],"queryString":"fields @message","queryLanguage":"CWLI",
					"startTime":1777887000,"endTime":1777888800,"limit":1}`,
			},
			want: `{"account":"` + testAccount + `","region":"eu-west-1","logGroups":["/aws/eks/prod/app","/aws/eks/prod/db"],"fields":["@message"],
				"rows":[["panic"]],"statistics":{"recordsMatched":40,"recordsScanned":900,"bytesScanned":4096}}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: "Logs Insights query in account " + testAccount + ", Region eu-west-1",
				URL: "https://eu-west-1.console.aws.amazon.com/cloudwatch/home?region=eu-west-1#logsV2:logs-insights$3FqueryDetail$3D" +
					"~(end~'2026-05-04T10*3a00*3a00.000Z~start~'2026-05-04T09*3a30*3a00.000Z~timeType~'ABSOLUTE~tz~'UTC" +
					"~editorString~'fields*20*40message~source~(~'*2faws*2feks*2fprod*2fapp~'*2faws*2feks*2fprod*2fdb))",
				At: testNow,
			}},
			wantTruncated: true,
		},
		{
			name:       "query_logs with no rows is no data with a link to the query",
			tool:       toolQueryLogs,
			input:      checkoutInput,
			logs:       []fakehttp.Response{answer(`{"queryId":"q-3"}`), answer(`{"status":"Complete","results":[]}`)},
			wantNoData: true,
			wantText: "The query returned no rows from 1 log groups in account " + testAccount + ", Region us-east-1 " +
				"between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{checkoutLink},
		},
		{
			name:       "a prefix no log group starts with is no data, and no query runs",
			tool:       toolQueryLogs,
			input:      `{"logGroups":["/ecs/gone*"],"query":"fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			logs:       []fakehttp.Response{answer(`{"logGroups":[]}`)},
			wantNoData: true,
			wantText:   "No log group in account " + testAccount + ", Region us-east-1 matches /ecs/gone*.",
		},
		{
			name:       "a log group that does not exist is no data",
			tool:       toolQueryLogs,
			input:      checkoutInput,
			logs:       []fakehttp.Response{refused(http.StatusBadRequest, "ResourceNotFoundException", "Log group does not exist")},
			wantNoData: true,
			wantText:   notFoundText,
		},
		{
			name:          "a query Logs Insights cannot parse is a tool error with its message",
			tool:          toolQueryLogs,
			input:         checkoutInput,
			logs:          []fakehttp.Response{refused(http.StatusBadRequest, "MalformedQueryException", "unexpected symbol found ( at line 1")},
			wantToolError: "AWS answered 400 MalformedQueryException: unexpected symbol found ( at line 1",
		},
		{
			name:  "a permission the role lacks is a tool error",
			tool:  toolQueryLogs,
			input: checkoutInput,
			logs: []fakehttp.Response{refused(http.StatusBadRequest, "AccessDeniedException",
				"User is not authorized to perform: logs:StartQuery")},
			wantToolError: "AWS answered 400 AccessDeniedException: User is not authorized to perform: logs:StartQuery",
		},
		{
			name:    "a rate limit is a failure of the source",
			tool:    toolQueryLogs,
			input:   checkoutInput,
			logs:    []fakehttp.Response{refused(http.StatusBadRequest, "ThrottlingException", "Rate exceeded")},
			wantErr: "ThrottlingException",
		},
		{
			name:    "a server error without a body is a failure of the source",
			tool:    toolQueryLogs,
			input:   checkoutInput,
			logs:    []fakehttp.Response{{Status: http.StatusServiceUnavailable}},
			wantErr: "StatusCode: 503",
		},
		{
			name:          "a query that fails in Logs Insights is a tool error",
			tool:          toolQueryLogs,
			input:         checkoutInput,
			logs:          []fakehttp.Response{answer(`{"queryId":"q-4"}`), answer(`{"status":"Failed"}`)},
			wantToolError: "the query ended with status Failed",
		},
		{
			name:  "list_changes reads the write events page by page and links each one",
			tool:  toolListChanges,
			input: `{}`,
			trail: []fakehttp.Response{
				answer(`{"Events":[{"EventId":"e-1","EventName":"UpdateService","EventSource":"ecs.amazonaws.com","EventTime":1777887000,
					"ReadOnly":"false","Username":"deploy-bot",
					"Resources":[{"ResourceType":"AWS::ECS::Service","ResourceName":"arn:aws:ecs:us-east-1:111122223333:service/shop/checkout"}],
					"CloudTrailEvent":"{\"userIdentity\":{\"arn\":\"arn:aws:sts::111122223333:assumed-role/deploy/deploy-bot\"},\"requestParameters\":{\"service\":\"checkout\",\"desiredCount\":0}}"}],
					"NextToken":"t2"}`),
				answer(`{"Events":[
					{"EventId":"e-2","EventName":"DeleteService","EventSource":"ecs.amazonaws.com","EventTime":1777885800,"ReadOnly":"false","Username":"ops",
					 "CloudTrailEvent":"{\"errorCode\":\"AccessDenied\",\"errorMessage\":\"not allowed\",\"requestParameters\":null}"},
					{"EventId":"e-3","EventName":"DescribeServices","EventSource":"ecs.amazonaws.com","EventTime":1777885200,"ReadOnly":"true"}]}`),
			},
			wantTargets: []string{targetLookupEvents, targetLookupEvents},
			wantBodies: []string{
				`{"LookupAttributes":[{"AttributeKey":"ReadOnly","AttributeValue":"false"}],` + lastDay + `,"MaxResults":50}`,
				`{"LookupAttributes":[{"AttributeKey":"ReadOnly","AttributeValue":"false"}],` + lastDay + `,"MaxResults":50,"NextToken":"t2"}`,
			},
			want: `{"account":"` + testAccount + `","region":"us-east-1","changes":[
				{"time":"2026-05-04T09:30:00Z","event":"UpdateService","service":"ecs.amazonaws.com","user":"deploy-bot",
				 "principal":"arn:aws:sts::111122223333:assumed-role/deploy/deploy-bot",
				 "resources":[{"type":"AWS::ECS::Service","name":"arn:aws:ecs:us-east-1:111122223333:service/shop/checkout"}],
				 "request":"{\"service\":\"checkout\",\"desiredCount\":0}","url":"` + trailConsole + `/e-1"},
				{"time":"2026-05-04T09:10:00Z","event":"DeleteService","service":"ecs.amazonaws.com","user":"ops",
				 "error":"AccessDenied: not allowed","url":"` + trailConsole + `/e-2"}
			]}`,
			wantEvidence: []source.Evidence{historyLink},
		},
		{
			name:  "list_changes of a resource asks for the resource and leaves out reads and other services itself",
			tool:  toolListChanges,
			input: `{"resource":"checkout","eventSource":"ecs.amazonaws.com"}`,
			trail: []fakehttp.Response{answer(`{"Events":[
				{"EventId":"e-4","EventName":"DescribeServices","EventSource":"ecs.amazonaws.com","EventTime":1777885200,"ReadOnly":"true"},
				{"EventId":"e-5","EventName":"ModifyListener","EventSource":"elasticloadbalancing.amazonaws.com","EventTime":1777885200,"ReadOnly":"false"}]}`)},
			wantBodies: []string{`{"LookupAttributes":[{"AttributeKey":"ResourceName","AttributeValue":"checkout"}],` + lastDay + `,"MaxResults":50}`},
			wantNoData: true,
			wantText: "CloudTrail has no write events about checkout from ecs.amazonaws.com in account " + testAccount +
				", Region us-east-1 between 2026-05-03T10:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "CloudTrail event history of account " + testAccount + ", Region us-east-1",
				URL: trailConsole + "?ResourceName=checkout", At: testNow}},
		},
		{
			name:  "list_changes says the events were cut at the limit",
			tool:  toolListChanges,
			input: `{"eventSource":"ecs.amazonaws.com","limit":1}`,
			trail: []fakehttp.Response{answer(`{"Events":[
				{"EventId":"e-6","EventName":"UpdateService","EventSource":"ecs.amazonaws.com","EventTime":1777887000,"ReadOnly":"false"},
				{"EventId":"e-7","EventName":"UpdateService","EventSource":"ecs.amazonaws.com","EventTime":1777885800,"ReadOnly":"false"}]}`)},
			wantBodies: []string{`{"LookupAttributes":[{"AttributeKey":"EventSource","AttributeValue":"ecs.amazonaws.com"}],` + lastDay + `,"MaxResults":50}`},
			want: `{"account":"` + testAccount + `","region":"us-east-1","changes":[
				{"time":"2026-05-04T09:30:00Z","event":"UpdateService","service":"ecs.amazonaws.com","url":"` + trailConsole + `/e-6"}]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "CloudTrail event history of account " + testAccount + ", Region us-east-1",
				URL: trailConsole + "?EventSource=ecs.amazonaws.com", At: testNow}},
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, apis := newTestSource(t, tt.edit)

			for _, r := range []struct {
				api       *fakehttp.Server
				responses []fakehttp.Response
			}{{apis.logs, tt.logs}, {apis.trail, tt.trail}} {
				if len(r.responses) > 0 {
					r.api.Handle(http.MethodPost, "/", r.responses...)
				}
			}

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			region := tt.wantRegion
			if region == "" {
				region = testRegion
			}

			for i, req := range apis.requests() {
				require.Contains(t, req.Header.Get("Authorization"), "Credential="+testAccessKey+"/20", "request %d", i)
				require.Contains(t, req.Header.Get("Authorization"), "/"+region+"/", "request %d is signed for the Region", i)
				require.Contains(t, req.Header.Get("User-Agent"), "app/"+appID)

				if i < len(tt.wantTargets) {
					require.Equal(t, tt.wantTargets[i], req.Header.Get("X-Amz-Target"), "target of request %d", i)
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

	logsInput := func(fields string) string {
		return `{"logGroups":["/ecs/checkout"],"query":"fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"` + fields + `}`
	}

	tests := []struct {
		name  string
		edit  func(*Config)
		tool  string
		input string
		want  string
	}{
		{name: "an account the config does not list", tool: toolListChanges, input: `{"account":"` + otherAccount + `"}`,
			want: `account "` + otherAccount + `" is not one Sidekick may read; allowed: ` + testAccount},
		{name: "no account with several in the config", tool: toolListChanges, input: `{}`,
			edit: func(c *Config) { c.Accounts = append(c.Accounts, Account{ID: roleAccount, RoleARN: testRole}) },
			want: "give account; allowed: " + testAccount + ", " + roleAccount},
		{name: "a Region the config does not list", tool: toolQueryLogs, input: logsInput(`,"region":"ap-south-1"`),
			want: `region "ap-south-1" is not one Sidekick may read; allowed: us-east-1`},
		{name: "no Region with several in the config", tool: toolListChanges, input: `{}`,
			edit: func(c *Config) { c.Regions = []string{testRegion, otherRegion} },
			want: "give region; allowed: us-east-1, eu-west-1"},
		{name: "a log group outside the prefixes", tool: toolQueryLogs,
			input: `{"logGroups":["/aws/lambda/pay"],"query":"fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want:  `logGroups: "/aws/lambda/pay" does not start with a prefix Sidekick may read; allowed: /ecs/, /aws/eks/prod/`},
		{name: "a prefix wider than the allowed ones", tool: toolQueryLogs,
			input: `{"logGroups":["/aws/*"],"query":"fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want:  `logGroups: "/aws/*" does not start with a prefix Sidekick may read`},
		{name: "a log group with a * inside", tool: toolQueryLogs,
			input: `{"logGroups":["/ecs/*/app"],"query":"fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want:  `logGroups: "/ecs/*/app" is not a log group name`},
		{name: "no log groups in the config", tool: toolQueryLogs, input: logsInput(""), edit: func(c *Config) { c.LogGroups = nil },
			want: "no log groups are configured for this source"},
		{name: "too many log groups", tool: toolQueryLogs,
			input: `{"logGroups":[` + strings.Repeat(`"/ecs/a",`, maxInputLogGroups) + `"/ecs/b"],"query":"x","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want:  "logGroups: give 1 to 20 log groups"},
		{name: "a query that picks its own log groups", tool: toolQueryLogs,
			input: `{"logGroups":["/ecs/checkout"],"query":"SOURCE logGroups(namePrefix: ['/']) | fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want:  "the SOURCE command picks other log groups"},
		{name: "an empty query", tool: toolQueryLogs,
			input: `{"logGroups":["/ecs/checkout"],"query":" ","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want:  "query: required"},
		{name: "a query longer than Logs Insights takes", tool: toolQueryLogs,
			input: `{"logGroups":["/ecs/checkout"],"query":"` + strings.Repeat("x", maxQueryLen+1) + `","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want:  "query: 10001 bytes is longer than the 10000 Logs Insights takes"},
		{name: "a log query without since", tool: toolQueryLogs,
			input: `{"logGroups":["/ecs/checkout"],"query":"fields @message","until":"2026-05-04T10:00:00Z"}`,
			want:  "since: required"},
		{name: "a log query without until", tool: toolQueryLogs,
			input: `{"logGroups":["/ecs/checkout"],"query":"fields @message","since":"2026-05-04T09:00:00Z"}`,
			want:  "until: required"},
		{name: "a range longer than seven days", tool: toolListChanges, input: `{"since":"2026-04-01T00:00:00Z"}`,
			want: "longer than the 168h0m0s one call may read"},
		{name: "a time that is not RFC 3339", tool: toolListChanges, input: `{"until":"yesterday"}`,
			want: `until: "yesterday" is not an RFC 3339 time`},
		{name: "an event source that is not one", tool: toolListChanges, input: `{"eventSource":"ecs"}`,
			want: `eventSource: "ecs" is not an AWS event source`},
		{name: "a resource with a line break", tool: toolListChanges, input: `{"resource":"checkout\nx"}`,
			want: "resource: must be a name or ARN"},
		{name: "input that does not decode", tool: toolListChanges, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, apis := newTestSource(t, tt.edit)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, apis.requests(), "the call never reaches AWS")
		})
	}
}

func TestQueryLogsStopsTheQueryInTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{name: "a query still running near the deadline is stopped", timeout: 300 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, apis := newTestSource(t, nil)

			apis.logs.HandleFunc(http.MethodPost, "/", func(w http.ResponseWriter, r *http.Request) {
				body := `{"queryId":"q-slow"}`

				switch r.Header.Get("X-Amz-Target") {
				case targetQueryResults:
					body = `{"status":"Running","results":[]}`
				case targetStopQuery:
					body = `{"success":true}`
				}

				w.Header().Set("Content-Type", jsonType)
				_, _ = io.WriteString(w, body)
			})

			ctx, cancel := context.WithTimeout(t.Context(), tt.timeout)
			defer cancel()

			_, err := s.Call(ctx, source.Call{Tool: toolQueryLogs, Input: json.RawMessage(
				`{"logGroups":["/ecs/checkout"],"query":"fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`)})
			require.NoError(t, ctx.Err(), "the call answers before its deadline")

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, "the query did not finish within ")
			require.Contains(t, toolErr.Message, "so Sidekick stopped it")

			sent := apis.logs.Requests()
			require.Greater(t, len(sent), 2)
			require.Equal(t, targetStartQuery, sent[0].Header.Get("X-Amz-Target"))

			// A poll cut short by the deadline can reach the server after the
			// stop, so the stop is looked for anywhere.
			stops := 0

			for _, req := range sent {
				if req.Header.Get("X-Amz-Target") == targetStopQuery {
					stops++

					require.JSONEq(t, `{"queryId":"q-slow"}`, string(req.Body))
				}
			}

			require.Equal(t, 1, stops, "the query is stopped once")
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
		{name: "a long value is cut", rows: 1, cell: maxCellLen + 100},
		{name: "a result stops before it grows too large", rows: maxRows, cell: maxCellLen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := make([]any, tt.rows)
			for i := range rows {
				rows[i] = []any{map[string]string{"field": "@message", "value": strings.Repeat("é", tt.cell/2)}}
			}

			data, err := json.Marshal(map[string]any{"status": "Complete", "results": rows})
			require.NoError(t, err)

			s, apis := newTestSource(t, nil)
			apis.logs.Handle(http.MethodPost, "/", answer(`{"queryId":"q-big"}`), fakehttp.Response{Headers: map[string]string{"Content-Type": jsonType}, Body: data})

			res, err := s.Call(t.Context(), source.Call{Tool: toolQueryLogs, Input: json.RawMessage(
				`{"logGroups":["/ecs/checkout"],"query":"fields @message","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z","limit":500}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)
			require.LessOrEqual(t, len(res.Structured), maxResultSize+1024)

			var got logsResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.NotEmpty(t, got.Rows)

			for _, row := range got.Rows {
				require.LessOrEqual(t, len(*row[0]), maxCellLen)
			}
		})
	}
}

func TestSourceAccounts(t *testing.T) {
	t.Parallel()

	withRole := func(externalID string) func(*Config) {
		return func(c *Config) { c.Accounts = []Account{{ID: roleAccount, RoleARN: testRole, ExternalID: externalID}} }
	}

	tests := []struct {
		name string
		edit func(*Config)
		// unchecked leaves Sidekick's own credentials to be checked by the
		// call.
		unchecked bool
		sts       []fakehttp.Response
		// wantSTS are the STS actions of the requests, in order.
		wantSTS []string
		// wantKey is the access key the CloudTrail request is signed with.
		// Empty means the call fails before it.
		wantKey string
		wantErr string
	}{
		{
			name:    "an account with a role is read with the role's credentials",
			edit:    withRole("sidekick-external"),
			sts:     []fakehttp.Response{assumedRole},
			wantSTS: []string{"AssumeRole"},
			wantKey: "ASIAASSUMEDNOTREAL",
		},
		{
			name:      "Sidekick's own credentials are checked to belong to the account",
			unchecked: true,
			sts:       []fakehttp.Response{callerIdentity(testAccount)},
			wantSTS:   []string{"GetCallerIdentity"},
			wantKey:   testAccessKey,
		},
		{
			name:      "Sidekick's own credentials of another account are refused",
			unchecked: true,
			sts:       []fakehttp.Response{callerIdentity(otherAccount)},
			wantSTS:   []string{"GetCallerIdentity"},
			wantErr:   "belong to another account: " + otherAccount,
		},
		{
			name:    "a role STS refuses is a failure of the source",
			edit:    withRole(""),
			sts:     []fakehttp.Response{stsDenied},
			wantSTS: []string{"AssumeRole"},
			wantErr: "get credentials for account " + roleAccount,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, apis := newTestSource(t, tt.edit)

			if tt.unchecked {
				s.accounts[testAccount].checked = false
			}

			apis.sts.Handle(http.MethodPost, "/", tt.sts...)

			if tt.wantKey != "" {
				apis.trail.Handle(http.MethodPost, "/", answer(`{"Events":[]}`), answer(`{"Events":[]}`))
			}

			for range 2 {
				res, err := s.Call(t.Context(), source.Call{Tool: toolListChanges, Input: json.RawMessage(`{}`)})
				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)

					_, isToolErr := errors.AsType[*source.ToolError](err)
					require.False(t, isToolErr, "a credential failure is not a tool error")
					require.Empty(t, apis.trail.Requests(), "nothing is sent without credentials")

					return
				}

				require.NoError(t, err)
				require.True(t, res.NoData)
			}

			var actions []string

			for _, req := range apis.sts.Requests() {
				form, err := url.ParseQuery(string(req.Body))
				require.NoError(t, err)

				actions = append(actions, form.Get("Action"))
			}

			require.Equal(t, tt.wantSTS, actions, "the credentials are got once for both calls")

			for _, req := range apis.trail.Requests() {
				require.Contains(t, req.Header.Get("Authorization"), "Credential="+tt.wantKey+"/")
			}
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "delete_log_group"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _ := newTestSource(t, nil)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	valid := Config{Accounts: []Account{{ID: testAccount}}, Regions: []string{testRegion}}
	errLoad := errors.New("no such profile")

	tests := []struct {
		name    string
		cfg     Config
		load    awsauth.Loader
		wantErr string
	}{
		{name: "a valid config", cfg: valid},
		{name: "settings that do not check", cfg: Config{Accounts: valid.Accounts, Regions: valid.Regions, Settings: source.Settings{Concurrency: -1}},
			wantErr: "concurrency: must not be negative"},
		{name: "a config that does not validate", cfg: Config{Regions: valid.Regions}, wantErr: "accounts: list at least one account"},
		{name: "an AWS config that cannot be loaded", cfg: valid,
			load: func(context.Context, ...func(*config.LoadOptions) error) (awssdk.Config, error) {
				return awssdk.Config{}, errLoad
			},
			wantErr: errLoad.Error()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			load := tt.load
			if load == nil {
				load = fakeAWSConfig
			}

			s, err := build(t.Context(), "aws", tt.cfg, registry.Deps{}, load, endpoints{})
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

func TestConsoleURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		region string
		want   string
	}{
		{name: "a commercial Region", region: "eu-west-1", want: "https://eu-west-1.console.aws.amazon.com/cloudwatch/home?region=eu-west-1#x"},
		{name: "a China Region", region: "cn-north-1", want: "https://cn-north-1.console.amazonaws.cn/cloudwatch/home?region=cn-north-1#x"},
		{name: "a GovCloud Region", region: "us-gov-west-1",
			want: "https://us-gov-west-1.console.amazonaws-us-gov.com/cloudwatch/home?region=us-gov-west-1#x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, consoleURL(tt.region, "cloudwatch", "x"))
		})
	}
}

func TestJSURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "letters, digits and _ - . stay", in: "aZ09_-.", want: "aZ09_-."},
		{name: "other ASCII is escaped", in: "a b/@|'~*", want: "a*20b*2f*40*7c*27*7e*2a"},
		{name: "a dollar sign is a !", in: "$x", want: "!x"},
		{name: "a line break", in: "a\nb", want: "a*0ab"},
		{name: "Latin-1 takes two hex digits", in: "é", want: "*e9"},
		{name: "other characters take four", in: "€", want: "**20ac"},
		{name: "a character outside the BMP is a surrogate pair", in: "😀", want: "**d83d**de00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, jsurl(tt.in))
		})
	}
}
