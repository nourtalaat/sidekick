package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrorsError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		errs Errors
		want string
	}{
		{name: "empty", errs: Errors{}, want: ""},
		{name: "one error with a path", errs: Errors{{Path: "server.listen", Message: "required"}}, want: "server.listen: required"},
		{name: "error without a path", errs: Errors{{Message: "the file is empty"}}, want: "the file is empty"},
		{
			name: "one error per line",
			errs: Errors{{Path: "a", Message: "first"}, {Path: "b", Message: "second"}},
			want: "a: first\nb: second",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.errs.Error())
		})
	}
}

func TestErrorListErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items Errors
		want  error
	}{
		{name: "no errors", items: nil, want: nil},
		{
			name: "sorted by path with numeric indexes",
			items: Errors{
				{Path: "routes[10].name", Message: "x"},
				{Path: "routes[2].name", Message: "x"},
				{Path: "model", Message: "x"},
				{Path: "routes[2]", Message: "x"},
			},
			want: Errors{
				{Path: "model", Message: "x"},
				{Path: "routes[2]", Message: "x"},
				{Path: "routes[2].name", Message: "x"},
				{Path: "routes[10].name", Message: "x"},
			},
		},
		{
			name:  "same path sorted by message",
			items: Errors{{Path: "a", Message: "b"}, {Path: "a", Message: "a"}},
			want:  Errors{{Path: "a", Message: "a"}, {Path: "a", Message: "b"}},
		},
		{
			name:  "exact duplicates are dropped",
			items: Errors{{Path: "a", Message: "b"}, {Path: "a", Message: "b"}},
			want:  Errors{{Path: "a", Message: "b"}},
		},
		{
			name:  "leading zeros compare by value",
			items: Errors{{Path: "x[010]", Message: "m"}, {Path: "x[9]", Message: "m"}},
			want:  Errors{{Path: "x[9]", Message: "m"}, {Path: "x[010]", Message: "m"}},
		},
		{
			name:  "same number of digits compares the digits",
			items: Errors{{Path: "x[3]", Message: "m"}, {Path: "x[1]", Message: "m"}},
			want:  Errors{{Path: "x[1]", Message: "m"}, {Path: "x[3]", Message: "m"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var l errorList

			l.addAll(tt.items)
			require.Equal(t, tt.want, l.err())
		})
	}
}

func TestErrorListAdd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		format string
		args   []any
		want   Error
	}{
		{name: "plain message", path: "server", format: "required", want: Error{Path: "server", Message: "required"}},
		{name: "formatted message", path: "routes[0]", format: "route %q", args: []any{"x"}, want: Error{Path: "routes[0]", Message: `route "x"`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var l errorList

			l.add(tt.path, tt.format, tt.args...)
			require.Equal(t, Errors{tt.want}, l.err())
		})
	}
}
