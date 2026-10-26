package registry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type widget struct {
	name string
	url  string
}

type widgetConfig struct {
	URL string `yaml:"url"`
}

func (c *widgetConfig) Validate() error {
	if c.URL == "" {
		return errors.New("url: required")
	}

	return nil
}

type plainConfig struct {
	Size int `yaml:"size"`
}

func TestRegistryRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		first     string
		second    string
		wantPanic string
	}{
		{name: "different type names", first: "http", second: "grpc"},
		{name: "same type name twice", first: "http", second: "http", wantPanic: `registry: widget type "http" registered twice`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := New[widget]("widget")
			r.Register(tt.first, constantPlugin(widget{name: tt.first}))

			register := func() { r.Register(tt.second, constantPlugin(widget{name: tt.second})) }
			if tt.wantPanic != "" {
				require.PanicsWithValue(t, tt.wantPanic, register)

				return
			}

			require.NotPanics(t, register)
			require.Equal(t, []string{tt.second, tt.first}, r.Types())
		})
	}
}

func TestRegistryBuild(t *testing.T) {
	t.Parallel()

	errFactory := errors.New("dial failed")

	tests := []struct {
		name         string
		typeName     string
		factory      Factory[widget]
		want         widget
		wantErr      error
		wantErrMatch string
	}{
		{
			name:     "registered type",
			typeName: "http",
			factory: func(_ context.Context, name string, _ Decoder, _ Deps) (widget, error) {
				return widget{name: name}, nil
			},
			want: widget{name: "primary"},
		},
		{
			name:         "unknown type",
			typeName:     "smtp",
			wantErr:      ErrUnknownType,
			wantErrMatch: `widget "primary": type "smtp": unknown type`,
		},
		{
			name:     "factory fails",
			typeName: "http",
			factory: func(context.Context, string, Decoder, Deps) (widget, error) {
				return widget{name: "half built"}, errFactory
			},
			wantErr:      errFactory,
			wantErrMatch: `build widget "primary": dial failed`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := New[widget]("widget")
			if tt.factory != nil {
				r.Register("http", Plugin[widget]{Build: tt.factory})
			}

			got, err := r.Build(t.Context(), tt.typeName, "primary", NewMockDecoder(gomock.NewController(t)), Deps{})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.EqualError(t, err, tt.wantErrMatch)
				require.Zero(t, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRegistryBuildPassesInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		deps Deps
	}{
		{name: "empty deps", deps: Deps{}},
		{name: "every dep set", deps: Deps{HTTP: &http.Client{}, Logger: slog.Default()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			dec := NewMockDecoder(ctrl)
			deps := tt.deps
			deps.Secrets = NewMockSecrets(ctrl)

			type ctxKey struct{}

			ctx := context.WithValue(t.Context(), ctxKey{}, "marker")

			r := New[widget]("widget")
			r.Register("http", Plugin[widget]{Build: func(gotCtx context.Context, name string, gotDec Decoder, gotDeps Deps) (widget, error) {
				require.Equal(t, "marker", gotCtx.Value(ctxKey{}))
				require.Equal(t, "primary", name)
				require.Same(t, dec, gotDec)
				require.Equal(t, deps, gotDeps)

				return widget{name: name}, nil
			}})

			got, err := r.Build(ctx, "http", "primary", dec, deps)
			require.NoError(t, err)
			require.Equal(t, widget{name: "primary"}, got)
		})
	}
}

func TestRegistryTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		names []string
		want  []string
	}{
		{name: "empty registry", names: nil, want: []string{}},
		{name: "sorted by name", names: []string{"slack", "jira", "webhook"}, want: []string{"jira", "slack", "webhook"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := New[widget]("widget")
			for _, n := range tt.names {
				r.Register(n, constantPlugin(widget{name: n}))
			}

			require.Equal(t, tt.want, r.Types())
		})
	}
}

func TestPluginRegistries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind  string
		build func(ctx context.Context) error
	}{
		{kind: "model", build: func(ctx context.Context) error { _, err := Models.Build(ctx, "none", "x", nil, Deps{}); return err }},
		{kind: "source", build: func(ctx context.Context) error { _, err := Sources.Build(ctx, "none", "x", nil, Deps{}); return err }},
		{kind: "intake", build: func(ctx context.Context) error { _, err := Intakes.Build(ctx, "none", "x", nil, Deps{}); return err }},
		{kind: "output", build: func(ctx context.Context) error { _, err := Outputs.Build(ctx, "none", "x", nil, Deps{}); return err }},
		{kind: "action", build: func(ctx context.Context) error { _, err := Actions.Build(ctx, "none", "x", nil, Deps{}); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()

			err := tt.build(t.Context())
			require.ErrorIs(t, err, ErrUnknownType)
			require.EqualError(t, err, tt.kind+` "x": type "none": unknown type`)
		})
	}
}

func TestTypedValidatedConfig(t *testing.T) {
	t.Parallel()

	errDecode := errors.New("line 3: cannot unmarshal")
	errBuild := errors.New("bad URL scheme")

	tests := []struct {
		name         string
		setupMocks   func(dec *MockDecoder)
		buildErr     error
		want         widget
		wantErr      error
		wantErrMatch string
	}{
		{
			name: "decoded and valid config reaches the constructor",
			setupMocks: func(dec *MockDecoder) {
				dec.EXPECT().Decode(gomock.AssignableToTypeOf(&widgetConfig{})).DoAndReturn(func(target any) error {
					target.(*widgetConfig).URL = "https://example.com"

					return nil
				})
			},
			want: widget{name: "primary", url: "https://example.com"},
		},
		{
			name: "decode failure",
			setupMocks: func(dec *MockDecoder) {
				dec.EXPECT().Decode(gomock.Any()).Return(errDecode)
			},
			wantErr:      errDecode,
			wantErrMatch: "decode config: line 3: cannot unmarshal",
		},
		{
			name: "Validate runs after decoding",
			setupMocks: func(dec *MockDecoder) {
				dec.EXPECT().Decode(gomock.Any()).Return(nil)
			},
			wantErrMatch: "validate config: url: required",
		},
		{
			name: "constructor failure",
			setupMocks: func(dec *MockDecoder) {
				dec.EXPECT().Decode(gomock.Any()).DoAndReturn(func(target any) error {
					target.(*widgetConfig).URL = "ftp://example.com"

					return nil
				})
			},
			buildErr:     errBuild,
			wantErr:      errBuild,
			wantErrMatch: "construct: bad URL scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dec := NewMockDecoder(gomock.NewController(t))
			tt.setupMocks(dec)

			factory := Typed(func(_ context.Context, name string, cfg widgetConfig, _ Deps) (widget, error) {
				if tt.buildErr != nil {
					return widget{name: "half built"}, tt.buildErr
				}

				return widget{name: name, url: cfg.URL}, nil
			})

			got, err := factory.Build(t.Context(), "primary", dec, Deps{})
			if tt.wantErrMatch != "" {
				require.EqualError(t, err, tt.wantErrMatch)
				require.Zero(t, got)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTypedConfigWithoutValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		size int
	}{
		{name: "zero value is passed through", size: 0},
		{name: "decoded value is passed through", size: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dec := NewMockDecoder(gomock.NewController(t))
			dec.EXPECT().Decode(gomock.AssignableToTypeOf(&plainConfig{})).DoAndReturn(func(target any) error {
				target.(*plainConfig).Size = tt.size

				return nil
			})

			factory := Typed(func(_ context.Context, _ string, cfg plainConfig, _ Deps) (int, error) {
				return cfg.Size, nil
			})

			got, err := factory.Build(t.Context(), "primary", dec, Deps{})
			require.NoError(t, err)
			require.Equal(t, tt.size, got)
		})
	}
}

func TestRegistryRegisterWithoutBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		plugin Plugin[widget]
	}{
		{name: "empty plugin", plugin: Plugin[widget]{}},
		{name: "check only", plugin: Plugin[widget]{Check: func(context.Context, string, Decoder) (any, error) { return nil, nil }}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := New[widget]("widget")
			require.PanicsWithValue(t, `registry: widget type "http" has no Build function`, func() { r.Register("http", tt.plugin) })
		})
	}
}

func TestRegistryCheck(t *testing.T) {
	t.Parallel()

	errCheck := errors.New("url: required")

	tests := []struct {
		name         string
		typeName     string
		check        Checker
		want         any
		wantErr      error
		wantErrMatch string
	}{
		{
			name:     "check returns the decoded config",
			typeName: "http",
			check: func(_ context.Context, name string, _ Decoder) (any, error) {
				return &widgetConfig{URL: name}, nil
			},
			want: &widgetConfig{URL: "primary"},
		},
		{
			name:     "plugin without a check accepts the section",
			typeName: "http",
		},
		{
			name:         "unknown type",
			typeName:     "smtp",
			wantErr:      ErrUnknownType,
			wantErrMatch: `widget "primary": type "smtp": unknown type`,
		},
		{
			name:     "check fails",
			typeName: "http",
			check: func(context.Context, string, Decoder) (any, error) {
				return nil, errCheck
			},
			wantErr:      errCheck,
			wantErrMatch: `check widget "primary": url: required`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := New[widget]("widget")
			r.Register("http", Plugin[widget]{
				Check: tt.check,
				Build: func(context.Context, string, Decoder, Deps) (widget, error) {
					t.Fatal("Check must not build the instance")

					return widget{}, nil
				},
			})

			got, err := r.Check(t.Context(), tt.typeName, "primary", NewMockDecoder(gomock.NewController(t)))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.EqualError(t, err, tt.wantErrMatch)
				require.Nil(t, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTypedCheck(t *testing.T) {
	t.Parallel()

	errDecode := errors.New("line 3: cannot unmarshal")

	tests := []struct {
		name         string
		setupMocks   func(dec *MockDecoder)
		want         any
		wantErr      error
		wantErrMatch string
	}{
		{
			name: "returns the decoded and valid config",
			setupMocks: func(dec *MockDecoder) {
				dec.EXPECT().Decode(gomock.AssignableToTypeOf(&widgetConfig{})).DoAndReturn(func(target any) error {
					target.(*widgetConfig).URL = "https://example.com"

					return nil
				})
			},
			want: &widgetConfig{URL: "https://example.com"},
		},
		{
			name: "decode failure",
			setupMocks: func(dec *MockDecoder) {
				dec.EXPECT().Decode(gomock.Any()).Return(errDecode)
			},
			wantErr:      errDecode,
			wantErrMatch: "decode config: line 3: cannot unmarshal",
		},
		{
			name: "Validate runs after decoding",
			setupMocks: func(dec *MockDecoder) {
				dec.EXPECT().Decode(gomock.Any()).Return(nil)
			},
			wantErrMatch: "validate config: url: required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dec := NewMockDecoder(gomock.NewController(t))
			tt.setupMocks(dec)

			plugin := Typed(func(context.Context, string, widgetConfig, Deps) (widget, error) {
				t.Fatal("Check must not call the constructor")

				return widget{}, nil
			})

			got, err := plugin.Check(t.Context(), "primary", dec)
			if tt.wantErrMatch != "" {
				require.EqualError(t, err, tt.wantErrMatch)
				require.Nil(t, got)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func constantPlugin(w widget) Plugin[widget] {
	return Plugin[widget]{Build: func(context.Context, string, Decoder, Deps) (widget, error) {
		return w, nil
	}}
}
