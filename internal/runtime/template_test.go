package runtime

import "testing"

func TestRender(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		inputs  map[string]any
		want    string
		wantErr bool
	}{
		{
			name:   "single string substitution",
			tmpl:   "Hello, {{ name }}!",
			inputs: map[string]any{"name": "Alice"},
			want:   "Hello, Alice!",
		},
		{
			name:   "multiple substitutions",
			tmpl:   "{{ greeting }}, {{ name }}!",
			inputs: map[string]any{"greeting": "Hi", "name": "Bob"},
			want:   "Hi, Bob!",
		},
		{
			name:   "whitespace inside braces",
			tmpl:   "{{  name  }}",
			inputs: map[string]any{"name": "Charlie"},
			want:   "Charlie",
		},
		{
			name:   "integer value",
			tmpl:   "qty: {{ qty }}",
			inputs: map[string]any{"qty": 42},
			want:   "qty: 42",
		},
		{
			name:   "float64 value",
			tmpl:   "price: {{ price }}",
			inputs: map[string]any{"price": float64(9.99)},
			want:   "price: 9.99",
		},
		{
			name:   "bool value",
			tmpl:   "active: {{ active }}",
			inputs: map[string]any{"active": true},
			want:   "active: true",
		},
		{
			name:   "nil value",
			tmpl:   "val: {{ val }}",
			inputs: map[string]any{"val": nil},
			want:   "val: ",
		},
		{
			name:   "object value marshalled as JSON",
			tmpl:   "data: {{ obj }}",
			inputs: map[string]any{"obj": map[string]any{"k": "v"}},
			want:   `data: {"k":"v"}`,
		},
		{
			name:   "no placeholders",
			tmpl:   "plain text",
			inputs: map[string]any{},
			want:   "plain text",
		},
		{
			name:    "missing input key",
			tmpl:    "Hello, {{ missing }}!",
			inputs:  map[string]any{},
			wantErr: true,
		},
		{
			name:   "extra inputs are ignored",
			tmpl:   "{{ a }}",
			inputs: map[string]any{"a": "x", "b": "y"},
			want:   "x",
		},
		{
			name:   "repeated placeholder",
			tmpl:   "{{ word }} {{ word }}",
			inputs: map[string]any{"word": "echo"},
			want:   "echo echo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.tmpl, tt.inputs)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Render(%q): expected error, got %q", tt.tmpl, got)
				}
				return
			}
			if err != nil {
				t.Errorf("Render(%q): unexpected error: %v", tt.tmpl, err)
				return
			}
			if got != tt.want {
				t.Errorf("Render(%q):\n got  %q\n want %q", tt.tmpl, got, tt.want)
			}
		})
	}
}

// Regression: whole numbers ending in zero must render verbatim. The old
// TrimRight(Sprintf("%g")) implementation stripped trailing zeros of WHOLE
// numbers too — {{ batch_size }} of 1000 rendered as "1", corrupting every
// round numeric quantity in prompts (observed as should-costs quoting qty 1).
func TestRenderRoundNumbers(t *testing.T) {
	cases := map[float64]string{
		1000:    "1000",
		100:     "100",
		10:      "10",
		2000:    "2000",
		10000:   "10000",
		1500:    "1500",
		1.5:     "1.5",
		0.1:     "0.1",
		1000000: "1000000",
	}
	for in, want := range cases {
		got, err := Render("{{ v }}", map[string]any{"v": in})
		if err != nil {
			t.Fatalf("Render(%v): %v", in, err)
		}
		if got != want {
			t.Errorf("Render(%v) = %q, want %q", in, got, want)
		}
	}
}
