package argocd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAppRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    AppRef
		wantErr bool
	}{
		{name: "project and name", in: "shop/checkout", want: AppRef{Project: "shop", Name: "checkout"}},
		{name: "spaces around are dropped", in: " shop/checkout\n", want: AppRef{Project: "shop", Name: "checkout"}},
		{name: "dots and dashes", in: "my-shop/checkout.v2-eu", want: AppRef{Project: "my-shop", Name: "checkout.v2-eu"}},
		{name: "a one letter name", in: "a/b", want: AppRef{Project: "a", Name: "b"}},
		{name: "the longest name", in: "shop/" + strings.Repeat("a", 253), want: AppRef{Project: "shop", Name: strings.Repeat("a", 253)}},
		{name: "a name that is too long", in: "shop/" + strings.Repeat("a", 254), wantErr: true},
		{name: "a bare name", in: "checkout", wantErr: true},
		{name: "a namespace in the middle", in: "shop/team-a/checkout", wantErr: true},
		{name: "an empty project", in: "/checkout", wantErr: true},
		{name: "an empty name", in: "shop/", wantErr: true},
		{name: "upper case", in: "Shop/checkout", wantErr: true},
		{name: "a wildcard", in: "shop/*", wantErr: true},
		{name: "a query", in: "shop/checkout?x=1", wantErr: true},
		{name: "an escape", in: "shop/check%2Fout", wantErr: true},
		{name: "a name that starts with a dot", in: "shop/.hidden", wantErr: true},
		{name: "nothing", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseAppRef(tt.in)
			if tt.wantErr {
				require.ErrorContains(t, err, "project/name")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want.Project+"/"+tt.want.Name, got.String())
		})
	}
}

func TestAllowlistCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		list    Allowlist
		wantErr []string
	}{
		{name: "one glob", list: Allowlist{"shop/*"}},
		{name: "several globs", list: Allowlist{"shop/*", "platform/ingress-?", "*/[a-c]*", "shop-^x/checkout.v2"}},
		{name: "empty", wantErr: []string{"applications: list at least one"}},
		{name: "a bare name", list: Allowlist{"checkout"}, wantErr: []string{`applications[0]: "checkout" must have the form project/name`}},
		{name: "a namespace", list: Allowlist{"shop/ns/app"}, wantErr: []string{`applications[0]: "shop/ns/app"`}},
		{name: "upper case", list: Allowlist{"Shop/*"}, wantErr: []string{`applications[0]: "Shop/*"`}},
		{name: "a space", list: Allowlist{"shop/a b"}, wantErr: []string{`applications[0]: "shop/a b"`}},
		{name: "an open class", list: Allowlist{"shop/[a"}, wantErr: []string{`applications[0]: invalid glob "shop/[a"`}},
		{name: "twice", list: Allowlist{"shop/*", "shop/*"}, wantErr: []string{`applications[1]: "shop/*" is listed twice`}},
		{name: "two bad entries", list: Allowlist{"a", "b"}, wantErr: []string{"applications[0]", "applications[1]"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.list.Check("applications")
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestAllowlistAllows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		list Allowlist
		ref  AppRef
		want bool
	}{
		{name: "every application of a project", list: Allowlist{"shop/*"}, ref: AppRef{Project: "shop", Name: "checkout"}, want: true},
		{name: "another project", list: Allowlist{"shop/*"}, ref: AppRef{Project: "platform", Name: "checkout"}},
		{name: "an exact name", list: Allowlist{"shop/checkout"}, ref: AppRef{Project: "shop", Name: "checkout"}, want: true},
		{name: "an exact name that differs", list: Allowlist{"shop/checkout"}, ref: AppRef{Project: "shop", Name: "checkout-eu"}},
		{name: "a name prefix", list: Allowlist{"shop/checkout-*"}, ref: AppRef{Project: "shop", Name: "checkout-eu"}, want: true},
		{name: "every project", list: Allowlist{"*/ingress"}, ref: AppRef{Project: "platform", Name: "ingress"}, want: true},
		{name: "the second glob", list: Allowlist{"a/*", "b/*"}, ref: AppRef{Project: "b", Name: "x"}, want: true},
		{name: "a star does not cross the project boundary", list: Allowlist{"*"}, ref: AppRef{Project: "shop", Name: "checkout"}},
		{name: "no globs", ref: AppRef{Project: "shop", Name: "checkout"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.list.Allows(tt.ref))
		})
	}
}

func TestAllowlistProjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		list Allowlist
		want []string
	}{
		{name: "one project", list: Allowlist{"shop/*"}, want: []string{"shop"}},
		{name: "sorted and without repeats", list: Allowlist{"shop/a", "platform/*", "shop/b"}, want: []string{"platform", "shop"}},
		{name: "a wildcard project", list: Allowlist{"shop/*", "*/ingress"}},
		{name: "a project with a class", list: Allowlist{"shop-[ab]/*"}},
		{name: "a project with a question mark", list: Allowlist{"shop-?/*"}},
		{name: "nothing", list: Allowlist{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.list.Projects())
		})
	}
}

func TestAllowlistString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		list Allowlist
		want string
	}{
		{name: "one glob", list: Allowlist{"shop/*"}, want: "shop/*"},
		{name: "two globs", list: Allowlist{"shop/*", "platform/*"}, want: "shop/*, platform/*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.list.String())
		})
	}
}
