package prometheus

import (
	"net/http"
	"testing"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Paths of the alert APIs.
const (
	alertsPath   = "/api/v1/alerts"
	rulesPath    = "/api/v1/rules"
	amAlertsPath = "/alertmanager/api/v2/alerts"
	silencesPath = "/alertmanager/api/v2/silences"
)

// withAlertmanager is a config whose Alertmanager is on the fake server.
var withAlertmanager = Config{AlertmanagerURL: "alertmanager"}

func TestListAlerts(t *testing.T) {
	t.Parallel()

	promAlerts := success(`{"alerts":[
		{"labels":{"alertname":"HighLatency","service":"cart"},"annotations":{"summary":"slow"},"state":"pending",
		 "activeAt":"2026-05-04T09:55:00Z","value":"1.5e+00"},
		{"labels":{"alertname":"CheckoutErrors","service":"checkout","severity":"critical"},"annotations":{"summary":"5xx above 5%"},
		 "state":"firing","activeAt":"2026-05-04T09:40:00Z","value":"0.07"},
		{"labels":{"alertname":"Watchdog"},"state":"firing","activeAt":"2026-05-04T09:50:00Z","value":"1"}]}`)
	promLink := source.Evidence{Kind: source.EvidenceResource, Title: "Alerts in Prometheus", URL: testWebURL + "/alerts"}
	amLink := source.Evidence{Kind: source.EvidenceResource, Title: "Alerts in Alertmanager", URL: "{api}/alertmanager/#/alerts"}

	runCalls(t, []callCase{
		{
			name:   "Prometheus alerts come firing first, then newest first",
			tool:   toolListAlerts,
			input:  `{}`,
			routes: []route{{http.MethodGet, alertsPath, []fakehttp.Response{promAlerts}}},
			want: `{"from":"prometheus","total":3,"alerts":[
				{"name":"Watchdog","state":"firing","since":"2026-05-04T09:50:00Z","value":1},
				{"name":"CheckoutErrors","state":"firing","since":"2026-05-04T09:40:00Z","value":0.07,
				 "labels":{"service":"checkout","severity":"critical"},"annotations":{"summary":"5xx above 5%"}},
				{"name":"HighLatency","state":"pending","since":"2026-05-04T09:55:00Z","value":1.5,
				 "labels":{"service":"cart"},"annotations":{"summary":"slow"}}]}`,
			wantEvidence: []source.Evidence{promLink},
		},
		{
			name:         "an alert name keeps only its alerts",
			tool:         toolListAlerts,
			input:        `{"alertname":"Watchdog"}`,
			routes:       []route{{http.MethodGet, alertsPath, []fakehttp.Response{promAlerts}}},
			want:         `{"from":"prometheus","total":1,"alerts":[{"name":"Watchdog","state":"firing","since":"2026-05-04T09:50:00Z","value":1}]}`,
			wantEvidence: []source.Evidence{promLink},
		},
		{
			name:   "a limit keeps fewer alerts and says so, and with a Grafana the evidence is its alert list",
			cfg:    Config{Links: Links{GrafanaURL: "https://grafana.example.com/", GrafanaDatasource: "prom-main"}},
			tool:   toolListAlerts,
			input:  `{"limit":1}`,
			routes: []route{{http.MethodGet, alertsPath, []fakehttp.Response{promAlerts}}},
			want:   `{"from":"prometheus","total":3,"alerts":[{"name":"Watchdog","state":"firing","since":"2026-05-04T09:50:00Z","value":1}]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "Alert rules in Grafana",
				URL: "https://grafana.example.com/alerting/list"}},
			wantTruncated: true,
		},
		{
			name:         "no alerts is no data",
			tool:         toolListAlerts,
			input:        `{"alertname":"CheckoutErrors"}`,
			routes:       []route{{http.MethodGet, alertsPath, []fakehttp.Response{success(`{"alerts":[]}`)}}},
			wantNoData:   true,
			wantText:     `No alert named "CheckoutErrors" is firing or pending in Prometheus.`,
			wantEvidence: []source.Evidence{promLink},
		},
		{
			name:  "Alertmanager alerts come with the silences that mute them",
			cfg:   withAlertmanager,
			tool:  toolListAlerts,
			input: `{}`,
			routes: []route{
				{http.MethodGet, amAlertsPath, []fakehttp.Response{body(`[
					{"labels":{"alertname":"DiskFull","instance":"db-1"},"startsAt":"2026-05-04T08:00:00Z",
					 "status":{"state":"suppressed","silencedBy":["s-1"],"inhibitedBy":[]}},
					{"labels":{"alertname":"CheckoutErrors","service":"checkout"},"annotations":{"summary":"5xx"},
					 "startsAt":"2026-05-04T09:40:00Z","status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}]`)}},
				{http.MethodGet, silencesPath, []fakehttp.Response{body(`[
					{"id":"s-1","matchers":[{"name":"alertname","value":"DiskFull","isRegex":false,"isEqual":true},
					 {"name":"instance","value":"db-.*","isRegex":true}],
					 "startsAt":"2026-05-04T07:55:00Z","endsAt":"2026-05-04T12:00:00Z","createdBy":"ana","comment":"resizing the disk",
					 "status":{"state":"active"}},
					{"id":"s-2","matchers":[{"name":"env","value":"dev","isRegex":false,"isEqual":false}],
					 "startsAt":"2026-05-01T00:00:00Z","endsAt":"2026-05-08T00:00:00Z","createdBy":"bo","status":{"state":"active"}}]`)}},
			},
			wantQueries: []string{"active=true&inhibited=true&silenced=true&unprocessed=true", ""},
			want: `{"from":"alertmanager","total":2,"alerts":[
				{"name":"CheckoutErrors","state":"active","since":"2026-05-04T09:40:00Z","labels":{"service":"checkout"},"annotations":{"summary":"5xx"}},
				{"name":"DiskFull","state":"suppressed","since":"2026-05-04T08:00:00Z","labels":{"instance":"db-1"},"silencedBy":["s-1"]}],
				"silences":[{"id":"s-1","matchers":["alertname=\"DiskFull\"","instance=~\"db-.*\""],
				 "startsAt":"2026-05-04T07:55:00Z","endsAt":"2026-05-04T12:00:00Z","createdBy":"ana","comment":"resizing the disk"}]}`,
			wantEvidence: []source.Evidence{amLink},
		},
		{
			name:         "Alertmanager with no alerts is no data, and silences are not read",
			cfg:          withAlertmanager,
			tool:         toolListAlerts,
			input:        `{}`,
			routes:       []route{{http.MethodGet, amAlertsPath, []fakehttp.Response{body(`[]`)}}},
			wantNoData:   true,
			wantText:     "Alertmanager has no alerts.",
			wantEvidence: []source.Evidence{amLink},
		},
		{
			name:  "an Alertmanager refusal is a tool error with its message",
			cfg:   withAlertmanager,
			tool:  toolListAlerts,
			input: `{}`,
			routes: []route{{http.MethodGet, amAlertsPath, []fakehttp.Response{{Status: http.StatusBadRequest, Headers: jsonHeaders,
				Body: []byte(`"bad matcher format"`)}}}},
			wantToolError: "Alertmanager answered 400: bad matcher format",
		},
	})
}

func TestGetRules(t *testing.T) {
	t.Parallel()

	const expr = `sum(rate(http_requests_total{code=~"5.."}[5m])) / sum(rate(http_requests_total[5m])) > 0.05`

	runCalls(t, []callCase{
		{
			name:  "the alerting rules with the name, from every group",
			tool:  toolGetRules,
			input: `{"alertname":"CheckoutErrors"}`,
			routes: []route{{http.MethodGet, rulesPath, []fakehttp.Response{success(`{"groups":[
				{"name":"checkout","file":"/etc/prometheus/rules/checkout.yaml","rules":[
					{"type":"alerting","name":"CheckoutErrors","query":` + mustJSON(t, expr) + `,"duration":300,"keepFiringFor":0,
					 "labels":{"severity":"critical"},"annotations":{"summary":"5xx above 5%"},"health":"ok","lastError":"",
					 "lastEvaluation":"2026-05-04T09:59:30Z","state":"firing","alerts":[{"labels":{"alertname":"CheckoutErrors"}}]},
					{"type":"recording","name":"CheckoutErrors","query":"sum(up)","health":"ok"},
					{"type":"alerting","name":"CartErrors","query":"up == 0","health":"ok","state":"inactive","alerts":[]}]},
				{"name":"other","file":"other.yaml","rules":[
					{"type":"alerting","name":"CheckoutErrors","query":"up == 0","duration":90.5,"health":"err",
					 "lastError":"vector contains metrics with the same labelset","state":"inactive","alerts":[]}]}]}`)}}},
			wantQueries: []string{"rule_name%5B%5D=CheckoutErrors&type=alert"},
			want: `{"rules":[
				{"group":"checkout","file":"/etc/prometheus/rules/checkout.yaml","name":"CheckoutErrors","expression":` + mustJSON(t, expr) + `,
				 "for":"5m0s","labels":{"severity":"critical"},"annotations":{"summary":"5xx above 5%"},"health":"ok",
				 "lastEvaluation":"2026-05-04T09:59:30Z","state":"firing","activeAlerts":1},
				{"group":"other","file":"other.yaml","name":"CheckoutErrors","expression":"up == 0","for":"1m30.5s","health":"err",
				 "lastError":"vector contains metrics with the same labelset","state":"inactive","activeAlerts":0}]}`,
			wantEvidence: []source.Evidence{graphLink("Alert rule CheckoutErrors", expr, "1h", testNow)},
		},
		{
			name:       "no rule with the name is no data",
			tool:       toolGetRules,
			input:      `{"alertname":"Nope"}`,
			routes:     []route{{http.MethodGet, rulesPath, []fakehttp.Response{success(`{"groups":[]}`)}}},
			wantNoData: true,
			wantText:   `No alerting rule is named "Nope".`,
		},
		{name: "an alert name is required", tool: toolGetRules, input: `{"alertname":""}`, wantToolError: "alertname: required"},
	})
}
