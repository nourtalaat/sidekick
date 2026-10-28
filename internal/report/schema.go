package report

import "encoding/json"

// SubmitVerdictTool is the name of the tool that ends a run.
const SubmitVerdictTool = "submit_verdict"

// SubmitVerdictSchema is the input schema of submit_verdict. Findings cite
// evidence by the ID of the tool call that produced it; Sidekick resolves the
// IDs to evidence links, so the model cannot invent URLs.
var SubmitVerdictSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["verdict", "confidence", "reason", "summary", "findings"],
  "properties": {
    "verdict": {"enum": ["true_positive", "false_positive", "inconclusive", "duplicate", "known_issue", "flapping"]},
    "confidence": {"enum": ["low", "medium", "high"]},
    "reason": {"type": "string", "maxLength": 1200},
    "summary": {"type": "string", "maxLength": 600},
    "cause": {"type": "string", "maxLength": 1200},
    "impact": {"type": "string", "maxLength": 800},
    "findings": {
      "type": "array", "maxItems": 15,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["claim", "evidenceToolCallIds"],
        "properties": {
          "claim": {"type": "string", "maxLength": 500},
          "evidenceToolCallIds": {"type": "array", "items": {"type": "string"}}
        }
      }
    },
    "fixes": {
      "type": "array", "maxItems": 8,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["title", "detail"],
        "properties": {"title": {"type": "string"}, "detail": {"type": "string"}}
      }
    },
    "falsePositive": {
      "type": "object", "additionalProperties": false,
      "required": ["whyFired", "prevention"],
      "properties": {
        "whyFired": {"type": "string"},
        "prevention": {
          "type": "array",
          "items": {
            "type": "object", "additionalProperties": false,
            "required": ["kind", "detail"],
            "properties": {
              "kind": {"enum": ["threshold", "duration", "filter", "exclusion", "delete_monitor", "other"]},
              "detail": {"type": "string"}
            }
          }
        }
      }
    }
  }
}`)

// AnswerFollowUpTool is the name of the tool that answers a follow-up
// question without changing the verdict. Only follow-up runs offer it.
const AnswerFollowUpTool = "answer_follow_up"

// AnswerFollowUpSchema is the input schema of answer_follow_up. Like a
// finding, the answer cites the tool calls that support it.
var AnswerFollowUpSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["answer", "evidenceToolCallIds"],
  "properties": {
    "answer": {"type": "string", "maxLength": 2000},
    "evidenceToolCallIds": {"type": "array", "items": {"type": "string"}}
  }
}`)
