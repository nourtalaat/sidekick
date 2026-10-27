package backstage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    ref
		wantErr string
	}{
		{name: "a bare name", input: "checkout-api", want: ref{kindComponent, "default", "checkout-api"}},
		{name: "a name with spaces around it", input: "  checkout-api\n", want: ref{kindComponent, "default", "checkout-api"}},
		{name: "a namespace and a name", input: "shop/checkout-api", want: ref{kindComponent, "shop", "checkout-api"}},
		{name: "a full ref in mixed case", input: "Resource:Default/Orders_DB", want: ref{kindResource, "default", "Orders_DB"}},
		{name: "a kind the tool does not read", input: "group:payments", wantErr: "the kind must be one of component, resource, api"},
		{name: "a name with a slash too many", input: "a/b/c", wantErr: "is not a catalog name or entity ref"},
		{name: "a name with a query", input: "checkout?x=1", wantErr: "is not a catalog name or entity ref"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseRef(tt.input, kindComponent, defaultNamespace, serviceKinds)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTargetName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		targetRef string
		wantKind  string
		wantName  string
		wantOK    bool
	}{
		{name: "a full ref", targetRef: "Component:default/checkout-api", wantKind: kindComponent, wantName: "checkout-api", wantOK: true},
		{name: "no kind", targetRef: "default/checkout-api"},
		{name: "no namespace", targetRef: "component:checkout-api"},
		{name: "a name that is not valid", targetRef: "component:default/a b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			kind, name, ok := targetName(tt.targetRef)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantKind, kind)
			require.Equal(t, tt.wantName, name)
		})
	}
}
