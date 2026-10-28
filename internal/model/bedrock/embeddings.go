package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"golang.org/x/sync/errgroup"

	"github.com/nourtalaat/sidekick/internal/model"
)

// defaultEmbedTimeout bounds one embedding call when the timeout setting
// is zero. Embedding a batch is far quicker than a model turn.
const defaultEmbedTimeout = time.Minute

// titanConcurrency is how many Titan requests run at once. Titan takes one
// text per request, so a batch is many requests.
const titanConcurrency = 8

// embedContentType is the content type of every embedding request and answer.
const embedContentType = "application/json"

// inputTokenCountHeader is the response header in which Bedrock reports the
// input tokens of an InvokeModel call. The InvokeModel reference does not
// list it, so a missing header only means the usage is unknown.
const inputTokenCountHeader = "X-Amzn-Bedrock-Input-Token-Count"

// cohereDocumentInput is the input type Cohere gets for every text. The
// Embedder interface does not tell a stored text from a search query, so
// both are embedded the same way.
const cohereDocumentInput = "search_document"

// Errors of embedding calls.
var (
	errNotEmbedding      = errors.New("the connector is not set up for embeddings: use an embedding model and set dimensions")
	errEmbeddingOnly     = errors.New("the connector is built for an embedding model and cannot generate")
	errWrongVectorLength = errors.New("the model returned a vector of the wrong length")
	errBadAnswer         = errors.New("the model did not return one vector for each text")
)

// Embed returns one vector for each text, in order. Provider failures are
// returned as a *model.Error. The vectors are as long as the configured
// dimensions, or the call fails with an invalid_request error.
func (c *Connector) Embed(ctx context.Context, texts []string) (model.EmbedResult, error) {
	if c.embedding == nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, errNotEmbedding)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.cfg.Timeout, defaultEmbedTimeout))
	defer cancel()

	if _, err := c.credentials.Retrieve(ctx); err != nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: get AWS credentials: %w", c.name, err)}
	}

	var (
		res model.EmbedResult
		err error
	)

	switch c.embedding.family {
	case familyTitan:
		res, err = c.embedTitan(ctx, texts)
	default:
		res, err = c.embedCohere(ctx, texts)
	}

	if err != nil {
		return model.EmbedResult{}, fmt.Errorf("connector %s: %w", c.name, err)
	}

	return res, nil
}

// Dimensions returns the length of the vectors Embed returns, or zero for a
// connector that is not set up for embeddings.
func (c *Connector) Dimensions() int {
	return c.dimensions
}

// embedTitan embeds each text with its own request, a few at a time.
func (c *Connector) embedTitan(ctx context.Context, texts []string) (model.EmbedResult, error) {
	vectors := make([][]float32, len(texts))
	tokens := make([]int64, len(texts))
	requestIDs := make([]string, len(texts))

	var group errgroup.Group

	group.SetLimit(titanConcurrency)

	for i, text := range texts {
		group.Go(func() error {
			body, err := json.Marshal(titanRequest{InputText: text, Dimensions: c.dimensions, Normalize: true})
			if err != nil {
				return &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("encode the Titan request: %w", err)}
			}

			out, err := c.invoke(ctx, body)
			if err != nil {
				return fmt.Errorf("text %d of %d: %w", i+1, len(texts), err)
			}

			var ans titanResponse
			if jerr := json.Unmarshal(out.Body, &ans); jerr != nil {
				return &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("text %d of %d: decode the Titan answer: %w", i+1, len(texts), jerr)}
			}

			v, err := c.vector(ans.Embedding)
			if err != nil {
				return fmt.Errorf("text %d of %d: %w", i+1, len(texts), err)
			}

			vectors[i], tokens[i], requestIDs[i] = v, ans.InputTextTokenCount, requestIDOf(out)

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return model.EmbedResult{}, fmt.Errorf("embed with Titan: %w", err)
	}

	res := model.EmbedResult{Vectors: vectors}
	for i := range tokens {
		res.Usage.InputTokens += tokens[i]
	}

	if len(requestIDs) > 0 {
		res.RequestID = requestIDs[0]
	}

	return res, nil
}

// embedCohere embeds all texts in one request.
func (c *Connector) embedCohere(ctx context.Context, texts []string) (model.EmbedResult, error) {
	body, err := json.Marshal(cohereRequest{
		Texts:          texts,
		InputType:      cohereDocumentInput,
		EmbeddingTypes: []string{cohereFloat},
		OutputDim:      c.dimensions,
		Truncate:       cohereTruncateRight,
	})
	if err != nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("encode the Cohere request: %w", err)}
	}

	out, err := c.invoke(ctx, body)
	if err != nil {
		return model.EmbedResult{}, fmt.Errorf("embed with Cohere: %w", err)
	}

	rows, err := cohereVectors(out.Body)
	if err != nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrInvalid, Err: err}
	}

	if len(rows) != len(texts) {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("%w: sent %d texts, got %d vectors", errBadAnswer, len(texts), len(rows))}
	}

	res := model.EmbedResult{Vectors: make([][]float32, len(rows)), RequestID: requestIDOf(out), Usage: model.Usage{InputTokens: inputTokensOf(out)}}

	for i, row := range rows {
		if res.Vectors[i], err = c.vector(row); err != nil {
			return model.EmbedResult{}, fmt.Errorf("embed with Cohere: text %d of %d: %w", i+1, len(rows), err)
		}
	}

	return res, nil
}

// invoke sends body to the embedding model with InvokeModel. It returns a
// *model.Error for a failed call. The raw response is kept in the result
// metadata, for the token count header.
func (c *Connector) invoke(ctx context.Context, body []byte) (*bedrockruntime.InvokeModelOutput, error) {
	out, err := c.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(c.cfg.Model),
		Body:        body,
		ContentType: aws.String(embedContentType),
		Accept:      aws.String(embedContentType),
	}, func(o *bedrockruntime.Options) {
		o.APIOptions = append(o.APIOptions, awsmiddleware.AddRawResponseToMetadata)
	})
	if err != nil {
		return nil, classify(fmt.Errorf("invoke model: %w", err))
	}

	return out, nil
}

// vector converts a vector from the model, checking its length.
func (c *Connector) vector(row []float64) ([]float32, error) {
	if len(row) != c.dimensions {
		return nil, &model.Error{
			Kind: model.ErrInvalid,
			Err:  fmt.Errorf("%w: expected %d numbers, got %d; set dimensions to what model %s returns", errWrongVectorLength, c.dimensions, len(row), c.cfg.Model),
		}
	}

	v := make([]float32, len(row))
	for i, f := range row {
		v[i] = float32(f)
	}

	return v, nil
}

// titanRequest is the InvokeModel body of Titan Text Embeddings V2.
type titanRequest struct {
	InputText  string `json:"inputText"`
	Dimensions int    `json:"dimensions"`
	Normalize  bool   `json:"normalize"`
}

// titanResponse is the InvokeModel answer of Titan Text Embeddings V2.
type titanResponse struct {
	Embedding           []float64 `json:"embedding"`
	InputTextTokenCount int64     `json:"inputTextTokenCount"`
}

// Cohere request values.
const (
	cohereFloat         = "float"
	cohereTruncateRight = "RIGHT"
)

// cohereRequest is the InvokeModel body of Cohere Embed v4 for text.
type cohereRequest struct {
	Texts          []string `json:"texts"`
	InputType      string   `json:"input_type"`
	EmbeddingTypes []string `json:"embedding_types"`
	OutputDim      int      `json:"output_dimension"`
	Truncate       string   `json:"truncate"`
}

// cohereVectors reads the vectors of a Cohere Embed v4 answer. The
// documentation shows the embeddings as a list when one type was asked for
// and as an object by type when several were, so both shapes are read.
func cohereVectors(body []byte) ([][]float64, error) {
	var ans struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}

	if err := json.Unmarshal(body, &ans); err != nil {
		return nil, fmt.Errorf("decode the Cohere answer: %w", err)
	}

	trimmed := bytes.TrimSpace(ans.Embeddings)

	if len(trimmed) > 0 && trimmed[0] == '{' {
		var byType map[string][][]float64
		if err := json.Unmarshal(trimmed, &byType); err != nil {
			return nil, fmt.Errorf("decode the Cohere embeddings: %w", err)
		}

		rows, ok := byType[cohereFloat]
		if !ok {
			return nil, fmt.Errorf("%w: the answer has no float embeddings", errBadAnswer)
		}

		return rows, nil
	}

	var rows [][]float64
	if err := json.Unmarshal(trimmed, &rows); err != nil {
		return nil, fmt.Errorf("decode the Cohere embeddings: %w", err)
	}

	return rows, nil
}

// requestIDOf returns the request ID of a call, or "".
func requestIDOf(out *bedrockruntime.InvokeModelOutput) string {
	id, _ := awsmiddleware.GetRequestIDMetadata(out.ResultMetadata)

	return id
}

// inputTokensOf returns the input tokens Bedrock reported in the response
// headers of a call, or zero when it reported none.
func inputTokensOf(out *bedrockruntime.InvokeModelOutput) int64 {
	raw, ok := awsmiddleware.GetRawResponse(out.ResultMetadata).(*smithyhttp.Response)
	if !ok || raw == nil || raw.Response == nil {
		return 0
	}

	return headerInt(raw.Response, inputTokenCountHeader)
}

// headerInt returns the number in header h of resp, or zero.
func headerInt(resp *http.Response, h string) int64 {
	n, err := strconv.ParseInt(resp.Header.Get(h), 10, 64)
	if err != nil || n < 0 {
		return 0
	}

	return n
}
