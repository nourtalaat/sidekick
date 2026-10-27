package validate

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
	"go.yaml.in/yaml/v3"
)

//go:embed grafana_alert_file.schema.json
var grafanaSchema []byte

// grafanaExpressionUIDs are the datasource UIDs of Grafana's own
// expressions (math, reduce, threshold, and so on), which are not queries.
var grafanaExpressionUIDs = map[string]bool{"__expr__": true, "-100": true}

// grafanaVariable matches Grafana's built-in interval and range variables,
// which a query may use in place of a duration or a number.
var grafanaVariable = regexp.MustCompile(`\$\{?__(rate_interval|interval|range)(_ms|_s)?\}?`)

// grafanaVariableValues stand in for Grafana's variables while a query is
// parsed: a duration, or a number for the _ms and _s forms.
var grafanaVariableValues = map[string]string{"": "5m", "_ms": "300000", "_s": "300"}

// grafanaChecker checks Grafana alerting file provisioning files against
// Sidekick's schema of them, then parses each Prometheus query.
type grafanaChecker struct {
	schema *schemaLevel
	promql parser.Parser
}

// grafanaFile is the part of a provisioning file whose queries are parsed.
type grafanaFile struct {
	Groups []struct {
		Name  string `json:"name"`
		Rules []struct {
			Title string `json:"title"`
			Data  []struct {
				RefID         string `json:"refId"`
				DatasourceUID string `json:"datasourceUid"`
				Model         struct {
					Expr       *string `json:"expr"`
					Datasource *struct {
						Type string `json:"type"`
					} `json:"datasource"`
				} `json:"model"`
			} `json:"data"`
		} `json:"rules"`
	} `json:"groups"`
}

// newGrafanaChecker loads the embedded schema. The parser accepts every
// optional PromQL feature, since the Prometheus that runs the query
// decides which ones it allows.
func newGrafanaChecker() (grafanaChecker, error) {
	top, err := loadSchemaLevels(grafanaSchema, []schemaLevelSpec{
		{children: "groups"},
		{def: "group", children: "rules"},
		{def: "rule", children: "data"},
		{def: "query"},
	})
	if err != nil {
		return grafanaChecker{}, fmt.Errorf("load the schema: %w", err)
	}

	return grafanaChecker{
		schema: top,
		promql: parser.NewParser(parser.Options{
			EnableExperimentalFunctions:  true,
			ExperimentalDurationExpr:     true,
			EnableExtendedRangeSelectors: true,
			EnableBinopFillModifiers:     true,
		}),
	}, nil
}

func (c grafanaChecker) check(_ context.Context, _ *run, req Request) (Result, error) {
	var res Result

	for _, f := range req.Files {
		file := c.checkFile(f)
		res.Errors = append(res.Errors, file.Errors...)
		res.NotChecked = append(res.NotChecked, file.NotChecked...)
	}

	return res, nil
}

// checkFile checks one file: YAML or JSON, then the schema, then the
// queries.
func (c grafanaChecker) checkFile(f File) Result {
	var doc any
	if err := yaml.Unmarshal([]byte(f.Content), &doc); err != nil {
		return Result{Errors: []Problem{{Path: f.Path, Message: "not valid YAML or JSON: " + err.Error()}}}
	}

	// A JSON round trip gives the schema the plain types it expects, such
	// as float64 numbers.
	raw, err := json.Marshal(doc)
	if err != nil {
		return Result{Errors: []Problem{{Path: f.Path, Message: "not a JSON-compatible document: " + err.Error()}}}
	}

	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return Result{Errors: []Problem{{Path: f.Path, Message: "not a JSON-compatible document: " + err.Error()}}}
	}

	if problems := c.schema.problems(f.Path, fileWhere, instance); len(problems) > 0 {
		return Result{Errors: problems}
	}

	var parsed grafanaFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{Errors: []Problem{{Path: f.Path, Message: "read the alert rules: " + err.Error()}}}
	}

	return c.checkQueries(f.Path, parsed)
}

// checkQueries parses the expr of every Prometheus query. A query for
// another kind of datasource is listed as not checked.
func (c grafanaChecker) checkQueries(path string, file grafanaFile) Result {
	var res Result

	for gi, g := range file.Groups {
		for ri, r := range g.Rules {
			for di, d := range r.Data {
				if grafanaExpressionUIDs[d.DatasourceUID] {
					continue
				}

				where := fmt.Sprintf("groups[%d].rules[%d] (%q) data[%d] (refId %s)", gi, ri, r.Title, di, d.RefID)
				kind := ""

				if d.Model.Datasource != nil {
					kind = d.Model.Datasource.Type
				}

				switch {
				case kind != "" && !isPrometheusType(kind):
					res.NotChecked = append(res.NotChecked, Problem{Path: path, Message: where + ": a " + kind + " query, not PromQL"})
				case d.Model.Expr == nil && kind == "":
					res.NotChecked = append(res.NotChecked, Problem{Path: path, Message: where + ": no expr, so not a PromQL query"})
				case d.Model.Expr == nil || strings.TrimSpace(*d.Model.Expr) == "":
					res.Errors = append(res.Errors, Problem{Path: path, Message: where + ": the Prometheus query has no expr"})
				default:
					if err := c.parse(*d.Model.Expr); err != nil {
						res.Errors = append(res.Errors, Problem{Path: path, Message: where + ": " + err.Error()})
					}
				}
			}
		}
	}

	return res
}

// parse parses one PromQL expression, with Grafana's variables replaced.
func (c grafanaChecker) parse(expr string) error {
	expr = grafanaVariable.ReplaceAllStringFunc(expr, func(v string) string {
		return grafanaVariableValues[grafanaVariable.FindStringSubmatch(v)[2]]
	})

	if _, err := c.promql.ParseExpr(expr); err != nil {
		return fmt.Errorf("PromQL %w", err)
	}

	return nil
}

// isPrometheusType reports whether a Grafana datasource type speaks PromQL:
// Prometheus itself, or a managed Prometheus such as
// grafana-amazonprometheus-datasource.
func isPrometheusType(kind string) bool {
	return kind == "prometheus" || strings.HasSuffix(kind, "prometheus-datasource")
}
