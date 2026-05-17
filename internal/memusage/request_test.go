package memusage

import (
	"strings"
	"testing"
)

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name     string
		req      *Request
		opts     Options
		wantCode string
	}{
		{
			name:     "nil request",
			req:      nil,
			wantCode: "invalid_request",
		},
		{
			name:     "nil labels map",
			req:      &Request{Labels: nil},
			wantCode: "empty_labels",
		},
		{
			name:     "empty labels map",
			req:      &Request{Labels: map[string]string{}},
			wantCode: "empty_labels",
		},
		{
			name:     "empty key",
			req:      &Request{Labels: map[string]string{"": "v"}},
			wantCode: "empty_label_key",
		},
		{
			name:     "too many labels",
			req:      &Request{Labels: manyLabels(DefaultMaxLabels + 1)},
			wantCode: "too_many_labels",
		},
		{
			name:     "label key too long",
			req:      &Request{Labels: map[string]string{strings.Repeat("k", DefaultMaxLabelKeyBytes+1): "v"}},
			wantCode: "label_key_too_long",
		},
		{
			name:     "label value too long",
			req:      &Request{Labels: map[string]string{"k": strings.Repeat("v", DefaultMaxLabelValueBytes+1)}},
			wantCode: "label_value_too_long",
		},
		{
			name: "valid request",
			req:  &Request{Labels: map[string]string{"job": "42"}},
		},
		{
			name: "empty value is allowed",
			req:  &Request{Labels: map[string]string{"job": ""}},
		},
		{
			name:     "custom max labels respected",
			req:      &Request{Labels: map[string]string{"a": "1", "b": "2", "c": "3"}},
			opts:     Options{MaxLabels: 2},
			wantCode: "too_many_labels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRequest(tt.req, tt.opts)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want code=%q, got nil error", tt.wantCode)
			}
			if err.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q (msg=%q)", err.Code, tt.wantCode, err.Msg)
			}
		})
	}
}

func TestValidationError_Error(t *testing.T) {
	e := &ValidationError{Code: "test_code", Msg: "test message"}
	if got := e.Error(); got != "test message" {
		t.Fatalf("Error() = %q, want %q", got, "test message")
	}
}

func TestOptions_EffectiveMaxLabelKeyBytes_Custom(t *testing.T) {
	o := Options{MaxLabelKeyBytes: 512}
	if got := o.effectiveMaxLabelKeyBytes(); got != 512 {
		t.Fatalf("effectiveMaxLabelKeyBytes = %d, want 512", got)
	}
}

func TestOptions_EffectiveMaxLabelValueBytes_Custom(t *testing.T) {
	o := Options{MaxLabelValueBytes: 2000}
	if got := o.effectiveMaxLabelValueBytes(); got != 2000 {
		t.Fatalf("effectiveMaxLabelValueBytes = %d, want 2000", got)
	}
}

func manyLabels(n int) map[string]string {
	out := make(map[string]string, n)
	for i := 0; i < n; i++ {
		out["k"+itoa(i)] = "v"
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
