// Package signoz is the action signoz.alert_rule: it changes a SigNoz alert
// rule in place through the SigNoz API, or deletes it, after an approver
// approved the change, and it can restore the fields it changed. It is
// written like the datadog.monitor action: the parameters say which fields
// to set, Snapshot reads the rule and computes the new one, Execute writes
// what the approver read, and Undo puts back only the fields the action
// changed. docs/plugins/actions.md explains the steps.
//
// What the action relies on from SigNoz, as its source code at the release
// tags v0.134.0 and v0.148.1 shows (github.com/SigNoz/signoz, paths below
// are in that repository):
//
//   - GET, PUT and DELETE /api/v2/rules/{id} (pkg/apiserver/signozapiserver/
//     ruler.go). The ID is a UUID. A read answers {"status":"success",
//     "data":{...}}; PUT and DELETE answer 204. An error answers
//     {"status":"error","error":{"message":...,"errors":[{"message":...}]}}
//     (pkg/http/render/render.go, pkg/errors/http.go).
//   - The API key goes in the header SIGNOZ-API-KEY unless the operator
//     renamed it (pkg/identn/config.go). Writing needs the role signoz-admin
//     or signoz-editor (pkg/http/middleware/authz.go, EditAccess).
//   - PUT replaces the whole rule with the body (pkg/query-service/rules/
//     manager.go, EditRule), and a missing disabled means false, so the
//     action always sends the whole rule. It does not use PATCH, which
//     merges maps and cannot remove a label.
//   - The read model (pkg/types/ruletypes/api_params.go, Rule) has the read-
//     only keys id, state, createdAt, createdBy, updatedAt and updatedBy. They
//     are left out of the definition. updatedAt is set on every write
//     (EditRule) and only its equality is used.
//   - SigNoz has no call that checks a rule without side effects. POST
//     /api/v2/rules/test sends a real notification to the rule's channels
//     and runs its query (manager.go, TestNotification), so the action never
//     calls it. SigNoz validates a rule only when it writes it, before
//     anything is stored, and a rejected write leaves the rule as it was
//     (pkg/ruler/rulestore/sqlrulestore/rule.go, EditRule runs in one
//     transaction). Snapshot therefore checks the common mistakes itself
//     (fields.go) and leaves template, query and channel checks to SigNoz.
//   - The body is stored as sent, and every read parses it into typed values
//     and writes it out again. So SigNoz drops keys it does not know, lower-
//     cases enum text such as op and matchType, drops empty optional values,
//     and shows the defaults of an optional value (pkg/types/ruletypes/
//     roundtrip_test.go shows the shape). A rule without schemaVersion (v1)
//     is read without thresholds, evaluation and notificationSettings, and
//     its top-level evalWindow and frequency; a v2alpha1 rule has them in
//     condition.thresholds, evaluation and notificationSettings, and keeps
//     but ignores the v1 fields. normalise.go compares values the way SigNoz
//     reads them back, and fields.go refuses a change SigNoz would drop or
//     ignore.
//   - A read shows a rule as disabled, with the state "disabled", when the
//     rule is not loaded in the rule manager, whatever the stored value is
//     (manager.go, GetRule, ListRuleStates and ruletypes.
//     NewListableRulesFromStorableRules). No read tells a rule a person
//     disabled from one that has not loaded, so a change to a rule that reads
//     as disabled is refused unless the change itself sets disabled.
//   - GET /api/v1/version answers {"version":"v0.148.1",...} without a key
//     (pkg/query-service/app/http_handler.go). The action supports v0.134.0
//     and later: from that tag to v0.148.1 the files above differ only by
//     new routes and fields, by changes that leave the JSON as it was (the
//     alert name became a DisplayName), and by the check of channel names
//     (display name instead of name, from v0.141.0). Tags before v0.134.0
//     read an empty groupBy, usePolicy and alertStates another way, and tags
//     before v0.133.0 have no version default and a renotify that is not
//     optional.
//   - SigNoz has no marker for a rule that is managed elsewhere, such as by
//     Terraform, so the action cannot refuse such a rule.
package signoz
