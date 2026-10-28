package promql

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQueryDataSeries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		answer  string
		want    []Series
		wantErr string
	}{
		{
			name: "a matrix with whole and fractional times",
			answer: `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"job":"api"},"values":[[1777885200,"0.5"],[1777885230.5,"NaN"],[1777885240.123,"1"]]}]}}`,
			want: []Series{{Metric: map[string]string{"job": "api"}, Values: []Sample{
				{T: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), V: "0.5"},
				{T: time.Date(2026, 5, 4, 9, 0, 30, 500_000_000, time.UTC), V: "NaN"},
				{T: time.Date(2026, 5, 4, 9, 0, 40, 123_000_000, time.UTC), V: "1"},
			}}},
		},
		{
			name:   "a vector",
			answer: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1777885200,"+Inf"]}]}}`,
			want:   []Series{{Metric: map[string]string{}, Value: &Sample{T: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), V: "+Inf"}}},
		},
		{
			name:   "no result",
			answer: `{"status":"success","data":{"resultType":"matrix","result":null}}`,
		},
		{
			name:    "a scalar holds no series",
			answer:  `{"status":"success","data":{"resultType":"scalar","result":[1777885200,"1"]}}`,
			wantErr: "holds no series",
		},
		{
			name:    "a sample that is not an array",
			answer:  `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[{"t":1}]}]}}`,
			wantErr: "decode a sample",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var answer Answer[QueryData]
			require.NoError(t, json.Unmarshal([]byte(tt.answer), &answer))
			require.Equal(t, StatusSuccess, answer.Status)

			got, err := answer.Data.Series()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestQueryDataSample(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		answer  string
		want    Sample
		wantErr string
	}{
		{
			name:   "a scalar",
			answer: `{"resultType":"scalar","result":[1777885200,"42"]}`,
			want:   Sample{T: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), V: "42"},
		},
		{
			name:   "a string",
			answer: `{"resultType":"string","result":[1777885200,"hello"]}`,
			want:   Sample{T: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), V: "hello"},
		},
		{
			name:    "a vector is not one sample",
			answer:  `{"resultType":"vector","result":[]}`,
			wantErr: "not a scalar or a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var data QueryData
			require.NoError(t, json.Unmarshal([]byte(tt.answer), &data))

			got, err := data.Sample()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
