package bedrock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Fake AWS credentials the tests sign with. No test calls AWS.
const (
	testAccessKey = "AKIATESTNOTREAL"
	testSecretKey = "test-secret-not-real"
)

// testRegion is the Region every test connector calls.
const testRegion = "us-east-1"

// The two operations, as the last segment of their path.
const (
	opConverse = "converse"
	opStream   = "converse-stream"
)

// Response headers of recorded answers.
const (
	headerContentType = "Content-Type"
	headerErrorType   = "X-Amzn-Errortype"
	headerRequestID   = "X-Amzn-Requestid"
	contentTypeJSON   = "application/json"
	contentTypeEvents = "application/vnd.amazon.eventstream"
)

// recorded is one recorded API answer, served from a file in fixture/. A
// file ending in .jsonl holds one ConverseStream message per line and is
// sent as an AWS event stream; any other file is sent as it is.
type recorded struct {
	op      string
	status  int
	file    string
	headers map[string]string
}

// streamMessage is one line of a .jsonl fixture: an event or an exception
// of a ConverseStream answer, by its member name, with its JSON payload.
type streamMessage struct {
	Event     string          `json:"event"`
	Exception string          `json:"exception"`
	Payload   json.RawMessage `json:"payload"`
}

// serve starts a fake Bedrock runtime of the test's own that answers calls
// for modelID with the responses, in order. The test fails if a response
// is left unused.
func serve(t *testing.T, modelID string, responses ...recorded) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)

	for _, r := range responses {
		headers := map[string]string{headerContentType: contentTypeJSON}
		resp := fakehttp.Response{Status: r.status}

		if filepath.Ext(r.file) == ".jsonl" {
			headers[headerContentType] = contentTypeEvents
			resp.Body = encodeStream(t, filepath.Join("fixture", r.file))
		} else {
			resp.BodyFile = filepath.Join("fixture", r.file)
		}

		maps.Copy(headers, r.headers)
		resp.Headers = headers

		api.Handle(http.MethodPost, "/model/"+modelID+"/"+r.op, resp)
	}

	api.ExpectAllCalled()

	return api
}

// encodeStream reads a .jsonl fixture and encodes it as an AWS event
// stream, the wire format of ConverseStream.
func encodeStream(t *testing.T, path string) []byte {
	t.Helper()

	f, err := os.Open(path) //nolint:gosec // A fixture path chosen by the test.
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, f.Close()) })

	var out bytes.Buffer

	enc := eventstream.NewEncoder()
	lines := bufio.NewScanner(f)

	for lines.Scan() {
		if len(bytes.TrimSpace(lines.Bytes())) == 0 {
			continue
		}

		var m streamMessage
		require.NoError(t, json.Unmarshal(lines.Bytes(), &m), "fixture %s", path)

		var headers eventstream.Headers

		headers.Set(":content-type", eventstream.StringValue(contentTypeJSON))

		if m.Exception != "" {
			headers.Set(":message-type", eventstream.StringValue("exception"))
			headers.Set(":exception-type", eventstream.StringValue(m.Exception))
		} else {
			headers.Set(":message-type", eventstream.StringValue("event"))
			headers.Set(":event-type", eventstream.StringValue(m.Event))
		}

		require.NoError(t, enc.Encode(&out, eventstream.Message{Headers: headers, Payload: m.Payload}))
	}

	require.NoError(t, lines.Err())

	return out.Bytes()
}

// newTestConnector builds a connector for modelID that sends its requests
// to api, signed with fake credentials. edit, when set, changes the config
// first.
func newTestConnector(t *testing.T, api *fakehttp.Server, modelID string, edit func(*Config)) *Connector {
	t.Helper()

	cfg := Config{Settings: model.Settings{Model: modelID}, Region: testRegion, Endpoint: api.URL()}
	if edit != nil {
		edit(&cfg)
	}

	c, err := build(t.Context(), "claude", cfg, registry.Deps{HTTP: api.Client()}, fakeAWSConfig)
	require.NoError(t, err)

	return c
}

// fakeAWSConfig is an AWS config loader that applies the options it is
// given to an empty config with fake credentials. It reads no environment
// variables and no files, so a developer's own AWS setup never matters.
func fakeAWSConfig(_ context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
	var o config.LoadOptions
	for _, fn := range optFns {
		if err := fn(&o); err != nil {
			return aws.Config{}, err
		}
	}

	return aws.Config{
		Region:      o.Region,
		HTTPClient:  o.HTTPClient,
		Credentials: credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
	}, nil
}

// requestBody returns the JSON body of request i that api received, decoded.
func requestBody(t *testing.T, api *fakehttp.Server, i int) map[string]json.RawMessage {
	t.Helper()

	sent := api.Requests()
	require.Greater(t, len(sent), i, "request %d was not sent", i)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sent[i].Body, &m))

	return m
}

// userText is a request with one user message.
func userText(text string) model.Request {
	return model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: text}}}}}
}

// mustBase64 decodes standard base64, the encoding of blobs in Bedrock's
// JSON.
func mustBase64(t *testing.T, s string) []byte {
	t.Helper()

	b, err := base64.StdEncoding.DecodeString(s)
	require.NoError(t, err)

	return b
}
