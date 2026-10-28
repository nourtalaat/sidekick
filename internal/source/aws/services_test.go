package aws

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Paths of the CloudWatch and AWS Health operations, in Smithy RPC v2.
const (
	pathMetricData   = "/service/GraniteServiceVersion20100801/operation/GetMetricData"
	pathAlarms       = "/service/GraniteServiceVersion20100801/operation/DescribeAlarms"
	pathAlarmHistory = "/service/GraniteServiceVersion20100801/operation/DescribeAlarmHistory"
	pathHealthEvents = "/service/AWSHealth_20160804/operation/DescribeEvents"
	pathEventDetails = "/service/AWSHealth_20160804/operation/DescribeEventDetails"
)

// Console links of the tests.
const (
	cloudWatchConsole = "https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#"
	eksConsole        = "https://us-east-1.console.aws.amazon.com/eks/home?region=us-east-1#/clusters"
	ecsConsole        = "https://us-east-1.console.aws.amazon.com/ecs/v2/clusters/shop"
	healthEventLink   = healthConsole + "?eventID=arn%3Aaws%3Ahealth%3Aus-east-1%3A%3Aevent%2FEC2%2FAWS_EC2_OPERATIONAL_ISSUE%2Fabc&eventTab=details"
)

// fakeRoute is one route of a fake API and the answers it gives, in order.
type fakeRoute struct {
	api       func(fakeAPIs) *fakehttp.Server
	method    string
	path      string
	responses []fakehttp.Response
}

// Each fake API, for routes.
var (
	onCloudWatch = func(f fakeAPIs) *fakehttp.Server { return f.cloudWatch }
	onEKS        = func(f fakeAPIs) *fakehttp.Server { return f.eks }
	onECS        = func(f fakeAPIs) *fakehttp.Server { return f.ecs }
	onHealth     = func(f fakeAPIs) *fakehttp.Server { return f.health }
)

func TestSourceCallServices(t *testing.T) {
	t.Parallel()

	alarmLink := source.Evidence{Kind: source.EvidenceResource, Title: "CloudWatch alarm checkout-5xx",
		URL: cloudWatchConsole + "alarmsV2:alarm/checkout-5xx", At: at(9, 30)}
	metricAlarm := `{"AlarmName":"checkout-5xx","StateValue":"ALARM","StateReason":"Threshold Crossed: 1 datapoint [12.0] was greater than the threshold (5.0).",
		"StateUpdatedTimestamp":{"$time":1777887000},"Namespace":"AWS/ApplicationELB","MetricName":"HTTPCode_Target_5XX_Count",
		"Dimensions":[{"Name":"LoadBalancer","Value":"app/shop/abc"}],"Statistic":"Sum","Period":60,"ComparisonOperator":"GreaterThanThreshold","Threshold":5}`
	metricAlarmItem := `{"name":"checkout-5xx","type":"metric","state":"ALARM","reason":"Threshold Crossed: 1 datapoint [12.0] was greater than the threshold (5.0).",
		"since":"2026-05-04T09:30:00Z","metric":{"namespace":"AWS/ApplicationELB","name":"HTTPCode_Target_5XX_Count","dimensions":{"LoadBalancer":"app/shop/abc"},
		"stat":"Sum","period":60},"comparison":"GreaterThanThreshold","threshold":5,"url":"` + cloudWatchConsole + `alarmsV2:alarm/checkout-5xx"}`

	tests := []struct {
		name   string
		tool   string
		input  string
		routes func(*testing.T) []fakeRoute
		// wantBodies are the bodies of the requests as JSON, in order; an
		// empty one is not checked.
		wantBodies []string
		// wantQueries are the encoded queries of the requests, in order.
		wantQueries   []string
		want          string
		wantEvidence  []source.Evidence
		wantTruncated bool
		wantNoData    bool
		wantText      string
		wantToolError string
	}{
		{
			name: "query_metrics reads metrics and expressions with a period that keeps 120 points",
			tool: toolQueryMetrics,
			input: `{"queries":[{"id":"m1","namespace":"AWS/ApplicationELB","metric":"HTTPCode_Target_5XX_Count",
				"dimensions":{"LoadBalancer":"app/shop/abc"},"stat":"Sum","hidden":true},{"id":"e1","expression":"100 * m1 / 60","label":"error rate"}]}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{{onCloudWatch, http.MethodPost, pathMetricData, []fakehttp.Response{cborAnswer(t, 0, `{"MetricDataResults":[
					{"Id":"e1","Label":"error rate","StatusCode":"PartialData","Timestamps":[{"$time":1777885200},{"$time":1777885260}],"Values":[1.5,2]}],
					"Messages":[{"Code":"MaxMetricsExceeded","Value":"too many"}]}`)}}}
			},
			wantBodies: []string{`{"MetricDataQueries":[
				{"Id":"m1","MetricStat":{"Metric":{"Namespace":"AWS/ApplicationELB","MetricName":"HTTPCode_Target_5XX_Count",
				 "Dimensions":[{"Name":"LoadBalancer","Value":"app/shop/abc"}]},"Period":60,"Stat":"Sum"},"ReturnData":false},
				{"Id":"e1","Expression":"100 * m1 / 60","Label":"error rate","Period":60,"ReturnData":true}],
				"StartTime":1777885200,"EndTime":1777888800,"ScanBy":"TimestampAscending","MaxDatapoints":6000}`},
			want: `{"account":"` + testAccount + `","region":"us-east-1","period":60,"series":[{"id":"e1","label":"error rate","status":"PartialData",
				"points":[{"time":"2026-05-04T09:00:00Z","value":1.5},{"time":"2026-05-04T09:01:00Z","value":2}]}],
				"messages":["MaxMetricsExceeded: too many"]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceMetric, Title: "CloudWatch metrics in account " + testAccount + ", Region us-east-1",
				URL: cloudWatchConsole + "metricsV2:graph=~(metrics~(~(~'AWS*2fApplicationELB~'HTTPCode_Target_5XX_Count~'LoadBalancer~'app*2fshop*2fabc" +
					"~(stat~'Sum~id~'m1~visible~false))~(~(expression~'100*20*2a*20m1*20*2f*2060~label~'error*20rate~id~'e1)))" +
					"~view~'timeSeries~stacked~false~region~'us-east-1~start~'2026-05-04T09*3a00*3a00.000Z~end~'2026-05-04T10*3a00*3a00.000Z~period~60)",
				At: testNow}},
			wantTruncated: true,
		},
		{
			name:  "query_metrics without points is no data",
			tool:  toolQueryMetrics,
			input: `{"queries":[{"id":"m1","namespace":"AWS/ECS","metric":"CPUUtilization"}],"since":"2026-04-28T10:00:00Z"}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{{onCloudWatch, http.MethodPost, pathMetricData, []fakehttp.Response{
					cborAnswer(t, 0, `{"MetricDataResults":[{"Id":"m1","StatusCode":"Complete","Timestamps":[],"Values":[]}]}`)}}}
			},
			wantBodies: []string{`{"MetricDataQueries":[{"Id":"m1","MetricStat":{"Metric":{"Namespace":"AWS/ECS","MetricName":"CPUUtilization"},
				"Period":4320,"Stat":"Average"},"ReturnData":true}],"StartTime":1777370400,"EndTime":1777888800,"ScanBy":"TimestampAscending","MaxDatapoints":6000}`},
			wantNoData: true,
			wantText:   "CloudWatch has no points for those queries in account " + testAccount + ", Region us-east-1 between 2026-04-28T10:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{{Kind: source.EvidenceMetric, Title: "CloudWatch metrics in account " + testAccount + ", Region us-east-1",
				URL: cloudWatchConsole + "metricsV2:graph=~(metrics~(~(~'AWS*2fECS~'CPUUtilization~(stat~'Average~id~'m1)))" +
					"~view~'timeSeries~stacked~false~region~'us-east-1~start~'2026-04-28T10*3a00*3a00.000Z~end~'2026-05-04T10*3a00*3a00.000Z~period~4320)",
				At: testNow}},
		},
		{
			name:  "an expression CloudWatch cannot parse is a tool error",
			tool:  toolQueryMetrics,
			input: `{"queries":[{"id":"e1","expression":"m9 +"}]}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{{onCloudWatch, http.MethodPost, pathMetricData, []fakehttp.Response{
					cborRefused(t, http.StatusBadRequest, "ValidationError", "Error in expression 'e1': Syntax error")}}}
			},
			wantToolError: "AWS answered 400 ValidationError: Error in expression 'e1': Syntax error",
		},
		{
			name:  "list_alarms lists the alarms in ALARM, newest change first",
			tool:  toolListAlarms,
			input: `{}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{{onCloudWatch, http.MethodPost, pathAlarms, []fakehttp.Response{cborAnswer(t, 0, `{"MetricAlarms":[`+metricAlarm+`],
					"CompositeAlarms":[{"AlarmName":"checkout-down","StateValue":"ALARM","AlarmRule":"ALARM(checkout-5xx)","StateUpdatedTimestamp":{"$time":1777887600}}]}`)}}}
			},
			wantBodies: []string{`{"AlarmTypes":["MetricAlarm","CompositeAlarm"],"MaxRecords":100,"StateValue":"ALARM"}`},
			want: `{"account":"` + testAccount + `","region":"us-east-1","alarms":[
				{"name":"checkout-down","type":"composite","state":"ALARM","since":"2026-05-04T09:40:00Z","rule":"ALARM(checkout-5xx)",
				 "url":"` + cloudWatchConsole + `alarmsV2:alarm/checkout-down"},` + metricAlarmItem + `]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceResource, Title: "CloudWatch alarm checkout-down", URL: cloudWatchConsole + "alarmsV2:alarm/checkout-down", At: at(9, 40)},
				alarmLink,
			},
		},
		{
			name:  "list_alarms with no alarm in the state is no data",
			tool:  toolListAlarms,
			input: `{"state":"INSUFFICIENT_DATA","prefix":"checkout-"}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{{onCloudWatch, http.MethodPost, pathAlarms, []fakehttp.Response{cborAnswer(t, 0, `{"MetricAlarms":[]}`)}}}
			},
			wantBodies: []string{`{"AlarmTypes":["MetricAlarm","CompositeAlarm"],"MaxRecords":100,"StateValue":"INSUFFICIENT_DATA","AlarmNamePrefix":"checkout-"}`},
			wantNoData: true,
			wantText:   "No CloudWatch alarm is in state INSUFFICIENT_DATA in account " + testAccount + ", Region us-east-1.",
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "CloudWatch alarms in account " + testAccount + ", Region us-east-1",
				URL: cloudWatchConsole + "alarmsV2:"}},
		},
		{
			name:  "list_alarms for one alarm returns its state changes",
			tool:  toolListAlarms,
			input: `{"alarm":"checkout-5xx"}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{
					{onCloudWatch, http.MethodPost, pathAlarms, []fakehttp.Response{cborAnswer(t, 0, `{"MetricAlarms":[`+metricAlarm+`]}`)}},
					{onCloudWatch, http.MethodPost, pathAlarmHistory, []fakehttp.Response{cborAnswer(t, 0, `{"AlarmHistoryItems":[{"AlarmName":"checkout-5xx",
						"Timestamp":{"$time":1777887000},"HistoryItemType":"StateUpdate","HistorySummary":"Alarm updated from OK to ALARM",
						"HistoryData":"{\"oldState\":{\"stateValue\":\"OK\"},\"newState\":{\"stateValue\":\"ALARM\"}}"}],"NextToken":"h2"}`)}},
				}
			},
			wantBodies: []string{
				`{"AlarmNames":["checkout-5xx"],"AlarmTypes":["MetricAlarm","CompositeAlarm"]}`,
				`{"AlarmName":"checkout-5xx","AlarmTypes":["MetricAlarm","CompositeAlarm"],"HistoryItemType":"StateUpdate",
				  "StartDate":1777802400,"EndDate":1777888800,"ScanBy":"TimestampDescending","MaxRecords":30}`,
			},
			want: `{"account":"` + testAccount + `","region":"us-east-1","alarm":` + metricAlarmItem + `,
				"history":[{"time":"2026-05-04T09:30:00Z","summary":"Alarm updated from OK to ALARM","from":"OK","to":"ALARM"}]}`,
			wantEvidence:  []source.Evidence{alarmLink},
			wantTruncated: true,
		},
		{
			name:  "an alarm CloudWatch does not have is no data",
			tool:  toolListAlarms,
			input: `{"alarm":"gone"}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{{onCloudWatch, http.MethodPost, pathAlarms, []fakehttp.Response{cborAnswer(t, 0, `{"MetricAlarms":[],"CompositeAlarms":[]}`)}}}
			},
			wantNoData: true,
			wantText:   `No CloudWatch alarm is named "gone" in account ` + testAccount + ", Region us-east-1.",
		},
		{
			name:  "get_eks_cluster returns the cluster and its node groups",
			tool:  toolGetEKSCluster,
			input: `{"cluster":"prod"}`,
			routes: func(*testing.T) []fakeRoute {
				return []fakeRoute{
					{onEKS, http.MethodGet, "/clusters/prod", []fakehttp.Response{answer(`{"cluster":{"name":"prod","status":"UPDATING","version":"1.33",
						"platformVersion":"eks.5","createdAt":1777885200,"upgradePolicy":{"supportType":"STANDARD"},"certificateAuthority":{"data":"c2VjcmV0"},
						"health":{"issues":[{"code":"Ec2SubnetNotFound","message":"subnet-1 was deleted","resourceIds":["subnet-1"]}]}}}`)}},
					{onEKS, http.MethodGet, "/clusters/prod/node-groups", []fakehttp.Response{answer(`{"nodegroups":["general"]}`)}},
					{onEKS, http.MethodGet, "/clusters/prod/node-groups/general", []fakehttp.Response{answer(`{"nodegroup":{"nodegroupName":"general",
						"status":"DEGRADED","version":"1.32","releaseVersion":"1.32.3-20260401","capacityType":"ON_DEMAND","instanceTypes":["m6i.large"],
						"scalingConfig":{"minSize":2,"maxSize":6,"desiredSize":3},"modifiedAt":1777887000,
						"health":{"issues":[{"code":"AsgInstanceLaunchFailures","message":"Could not launch On-Demand Instances","resourceIds":["eks-general"]}]}}}`)}},
				}
			},
			wantQueries: []string{"", "maxResults=100", ""},
			want: `{"account":"` + testAccount + `","region":"us-east-1","name":"prod","status":"UPDATING","version":"1.33","platformVersion":"eks.5",
				"supportType":"STANDARD","created":"2026-05-04T09:00:00Z",
				"issues":[{"code":"Ec2SubnetNotFound","message":"subnet-1 was deleted","resources":["subnet-1"]}],
				"nodegroups":[{"name":"general","status":"DEGRADED","version":"1.32","releaseVersion":"1.32.3-20260401","capacityType":"ON_DEMAND",
				 "instanceTypes":["m6i.large"],"desired":3,"min":2,"max":6,"modified":"2026-05-04T09:30:00Z",
				 "issues":[{"code":"AsgInstanceLaunchFailures","message":"Could not launch On-Demand Instances","resources":["eks-general"]}]}],
				"url":"` + eksConsole + `/prod"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "EKS cluster prod", URL: eksConsole + "/prod"}},
		},
		{
			name:  "get_eks_cluster without a cluster lists the clusters",
			tool:  toolGetEKSCluster,
			input: `{}`,
			routes: func(*testing.T) []fakeRoute {
				return []fakeRoute{{onEKS, http.MethodGet, "/clusters", []fakehttp.Response{answer(`{"clusters":["prod","staging"],"nextToken":"n2"}`)}}}
			},
			wantQueries:   []string{"maxResults=100"},
			want:          `{"account":"` + testAccount + `","region":"us-east-1","clusters":["prod","staging"]}`,
			wantEvidence:  []source.Evidence{{Kind: source.EvidenceResource, Title: "EKS clusters in account " + testAccount + ", Region us-east-1", URL: eksConsole}},
			wantTruncated: true,
		},
		{
			name:  "a cluster EKS does not have is no data",
			tool:  toolGetEKSCluster,
			input: `{"cluster":"gone"}`,
			routes: func(*testing.T) []fakeRoute {
				return []fakeRoute{{onEKS, http.MethodGet, "/clusters/gone", []fakehttp.Response{
					refused(http.StatusNotFound, "ResourceNotFoundException", "No cluster found for name: gone.")}}}
			},
			wantNoData: true,
			wantText:   notFoundText,
		},
		{
			name:  "get_ecs_service returns the service, its deployments and its latest events",
			tool:  toolGetECSService,
			input: `{"cluster":"shop","service":"checkout","limit":1}`,
			routes: func(*testing.T) []fakeRoute {
				return []fakeRoute{{onECS, http.MethodPost, "/", []fakehttp.Response{answer(`{"services":[{"serviceName":"checkout","status":"ACTIVE",
					"desiredCount":3,"runningCount":1,"pendingCount":2,"launchType":"FARGATE","taskDefinition":"arn:aws:ecs:us-east-1:111122223333:task-definition/checkout:42",
					"deployments":[{"id":"ecs-svc/1","status":"PRIMARY","rolloutState":"IN_PROGRESS","rolloutStateReason":"ECS deployment ecs-svc/1 in progress.",
					 "taskDefinition":"arn:aws:ecs:us-east-1:111122223333:task-definition/checkout:42","desiredCount":3,"runningCount":1,"pendingCount":2,
					 "failedTasks":4,"createdAt":1777887000,"updatedAt":1777887600}],
					"events":[{"id":"v2","createdAt":1777887600,"message":"(service checkout) is unable to consistently start tasks successfully."},
					 {"id":"v1","createdAt":1777887000,"message":"(service checkout) has started 2 tasks."}]}],"failures":[]}`)}}}
			},
			wantBodies: []string{`{"cluster":"shop","services":["checkout"]}`},
			want: `{"account":"` + testAccount + `","region":"us-east-1","cluster":"shop","name":"checkout","status":"ACTIVE","desired":3,"running":1,
				"pending":2,"launchType":"FARGATE","taskDefinition":"arn:aws:ecs:us-east-1:111122223333:task-definition/checkout:42",
				"deployments":[{"id":"ecs-svc/1","status":"PRIMARY","rollout":"IN_PROGRESS","rolloutReason":"ECS deployment ecs-svc/1 in progress.",
				 "taskDefinition":"arn:aws:ecs:us-east-1:111122223333:task-definition/checkout:42","desired":3,"running":1,"pending":2,"failed":4,
				 "created":"2026-05-04T09:30:00Z","updated":"2026-05-04T09:40:00Z"}],
				"events":[{"time":"2026-05-04T09:40:00Z","message":"(service checkout) is unable to consistently start tasks successfully."}],
				"url":"` + ecsConsole + `/services/checkout/health?region=us-east-1"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "ECS service checkout in cluster shop",
				URL: ecsConsole + "/services/checkout/health?region=us-east-1"}},
			wantTruncated: true,
		},
		{
			name:  "a service ECS does not have is no data",
			tool:  toolGetECSService,
			input: `{"cluster":"shop","service":"gone"}`,
			routes: func(*testing.T) []fakeRoute {
				return []fakeRoute{{onECS, http.MethodPost, "/", []fakehttp.Response{answer(`{"services":[],
					"failures":[{"arn":"arn:aws:ecs:us-east-1:111122223333:service/shop/gone","reason":"MISSING"}]}`)}}}
			},
			wantNoData: true,
			wantText:   "ECS cluster shop in account " + testAccount + ", Region us-east-1 has no service gone.",
		},
		{
			name:  "get_ecs_service without a service lists the cluster's services",
			tool:  toolGetECSService,
			input: `{"cluster":"shop"}`,
			routes: func(*testing.T) []fakeRoute {
				return []fakeRoute{{onECS, http.MethodPost, "/", []fakehttp.Response{answer(
					`{"serviceArns":["arn:aws:ecs:us-east-1:111122223333:service/shop/checkout","arn:aws:ecs:us-east-1:111122223333:service/shop/cart"]}`)}}}
			},
			wantBodies:   []string{`{"cluster":"shop","maxResults":100}`},
			want:         `{"account":"` + testAccount + `","region":"us-east-1","cluster":"shop","services":["checkout","cart"]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "ECS cluster shop", URL: ecsConsole + "?region=us-east-1"}},
		},
		{
			name:  "list_health_events reads the events of the Region and global services with their description",
			tool:  toolListHealthEvents,
			input: `{}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{
					{onHealth, http.MethodPost, pathHealthEvents, []fakehttp.Response{cborAnswer(t, 0, `{"events":[{
						"arn":"arn:aws:health:us-east-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/abc","service":"EC2","eventTypeCode":"AWS_EC2_OPERATIONAL_ISSUE",
						"eventTypeCategory":"issue","region":"us-east-1","statusCode":"open","startTime":{"$time":1777884000},"lastUpdatedTime":{"$time":1777887000}}]}`)}},
					{onHealth, http.MethodPost, pathEventDetails, []fakehttp.Response{cborAnswer(t, 0, `{"successfulSet":[{
						"event":{"arn":"arn:aws:health:us-east-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/abc"},
						"eventDescription":{"latestDescription":"We are investigating increased error rates."}}],"failedSet":[]}`)}},
				}
			},
			wantBodies: []string{
				`{"filter":{"regions":["us-east-1","global"],"lastUpdatedTimes":[{"from":1777802400,"to":1777888800}]},"maxResults":20}`,
				`{"eventArns":["arn:aws:health:us-east-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/abc"]}`,
			},
			want: `{"account":"` + testAccount + `","region":"us-east-1","events":[{"service":"EC2","type":"AWS_EC2_OPERATIONAL_ISSUE","category":"issue",
				"region":"us-east-1","status":"open","started":"2026-05-04T08:40:00Z","updated":"2026-05-04T09:30:00Z",
				"description":"We are investigating increased error rates.","url":"` + healthEventLink + `"}]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceIncident, Title: "AWS Health event AWS_EC2_OPERATIONAL_ISSUE", URL: healthEventLink, At: at(9, 30)}},
		},
		{
			name:  "list_health_events without a support plan that has the Health API is a tool error that says so",
			tool:  toolListHealthEvents,
			input: `{"status":"open"}`,
			routes: func(t *testing.T) []fakeRoute {
				return []fakeRoute{{onHealth, http.MethodPost, pathHealthEvents, []fakehttp.Response{
					cborRefused(t, http.StatusBadRequest, "SubscriptionRequiredException", "The AWS Premium Support Subscription is required")}}}
			},
			wantToolError: "the AWS Health API needs a Business, Enterprise On-Ramp or Enterprise support plan, which account " + testAccount +
				" does not have; the AWS Health dashboard in the console works without one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, apis := newTestSource(t, nil)

			for _, r := range tt.routes(t) {
				r.api(apis).Handle(r.method, r.path, r.responses...)
			}

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			for i, req := range apis.requests() {
				require.Contains(t, req.Header.Get("Authorization"), "Credential="+testAccessKey+"/20", "request %d", i)

				if i < len(tt.wantQueries) {
					require.Equal(t, tt.wantQueries[i], req.Query.Encode(), "query of request %d", i)
				}

				if i >= len(tt.wantBodies) || tt.wantBodies[i] == "" {
					continue
				}

				body := string(req.Body)
				if req.Header.Get("Content-Type") == "application/cbor" {
					body = cborJSON(t, req.Body)
				}

				require.JSONEq(t, tt.wantBodies[i], body, "body of request %d", i)
			}

			switch {
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)
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

func TestSourceCallServicesInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{name: "an account the config does not list", tool: toolQueryMetrics,
			input: `{"account":"` + otherAccount + `","queries":[{"id":"m1","namespace":"AWS/ECS","metric":"CPUUtilization"}]}`,
			want:  `account "` + otherAccount + `" is not one Sidekick may read`},
		{name: "a Region the config does not list", tool: toolGetEKSCluster, input: `{"region":"ap-south-1"}`,
			want: `region "ap-south-1" is not one Sidekick may read`},
		{name: "no queries", tool: toolQueryMetrics, input: `{"queries":[]}`, want: "queries: give 1 to 10 queries"},
		{name: "a query ID that is not one", tool: toolQueryMetrics, input: `{"queries":[{"id":"M1","expression":"1"}]}`,
			want: `queries[0].id: "M1" must start with a lower-case letter`},
		{name: "a query ID used twice", tool: toolQueryMetrics,
			input: `{"queries":[{"id":"e1","expression":"1"},{"id":"e1","expression":"2"}]}`, want: `queries[1].id: "e1" is used twice`},
		{name: "a query with a metric and an expression", tool: toolQueryMetrics,
			input: `{"queries":[{"id":"m1","namespace":"AWS/ECS","metric":"CPUUtilization","expression":"m2"}]}`,
			want:  "queries[0]: give either namespace and metric, or expression"},
		{name: "a query with neither", tool: toolQueryMetrics, input: `{"queries":[{"id":"m1"}]}`,
			want: "queries[0]: give either namespace and metric, or expression"},
		{name: "a metric without a name", tool: toolQueryMetrics, input: `{"queries":[{"id":"m1","namespace":"AWS/ECS"}]}`,
			want: "queries[0].metric: required with a metric"},
		{name: "a statistic that is not one", tool: toolQueryMetrics,
			input: `{"queries":[{"id":"m1","namespace":"AWS/ECS","metric":"CPUUtilization","stat":"Mean"}]}`,
			want:  `queries[0].stat: "Mean" is not a statistic`},
		{name: "a period that is not a multiple of a minute", tool: toolQueryMetrics,
			input: `{"queries":[{"id":"e1","expression":"1"}],"period":90}`, want: "period: 90 is not a multiple of 60 seconds"},
		{name: "a metric range longer than seven days", tool: toolQueryMetrics,
			input: `{"queries":[{"id":"e1","expression":"1"}],"since":"2026-04-01T00:00:00Z"}`, want: "longer than the 168h0m0s"},
		{name: "an alarm and a prefix", tool: toolListAlarms, input: `{"alarm":"a","prefix":"b"}`, want: "give alarm or prefix, not both"},
		{name: "an alarm state that is not one", tool: toolListAlarms, input: `{"state":"FIRING"}`,
			want: `state: "FIRING" is not ALARM, OK, INSUFFICIENT_DATA or any`},
		{name: "an EKS cluster given by ARN", tool: toolGetEKSCluster,
			input: `{"cluster":"arn:aws:eks:us-east-1:` + otherAccount + `:cluster/prod"}`, want: "is not an EKS cluster name"},
		{name: "an ECS service without a cluster", tool: toolGetECSService, input: `{"service":"checkout"}`,
			want: `cluster: "" is not an ECS cluster name`},
		{name: "an ECS cluster given by ARN", tool: toolGetECSService,
			input: `{"cluster":"arn:aws:ecs:us-east-1:` + otherAccount + `:cluster/shop"}`, want: "is not an ECS cluster name"},
		{name: "an ECS service name that climbs out", tool: toolGetECSService, input: `{"cluster":"shop","service":"../x"}`,
			want: `service: "../x" is not an ECS service name`},
		{name: "a health status that is not one", tool: toolListHealthEvents, input: `{"status":"resolved"}`,
			want: `status: "resolved" is not open, upcoming, closed or any`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, apis := newTestSource(t, nil)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, apis.requests(), "the call never reaches AWS")
		})
	}
}

func TestPeriodOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		given int
		span  time.Duration
		want  int
	}{
		{name: "an hour takes the floor", span: time.Hour, want: 60},
		{name: "a day needs 12 minutes for 120 points", span: 24 * time.Hour, want: 720},
		{name: "a week needs 84 minutes", span: 7 * 24 * time.Hour, want: 5040},
		{name: "a given period above the budget stays", given: 3600, span: time.Hour, want: 3600},
		{name: "a given period below the budget is raised", given: 60, span: 24 * time.Hour, want: 720},
		{name: "an odd range is rounded up to a whole minute", span: 3*time.Hour + time.Second, want: 120},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := periodOf(tt.given, window{start: testNow.Add(-tt.span), end: testNow})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHealthRegion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		region string
		want   string
	}{
		{region: "eu-west-1", want: "us-east-1"},
		{region: "cn-north-1", want: "cn-northwest-1"},
		{region: "us-gov-east-1", want: "us-gov-west-1"},
	}

	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, healthRegion(tt.region))
		})
	}
}
