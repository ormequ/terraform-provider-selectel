package selectel

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMKSV2BaseURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "v2 path", endpoint: "https://ru-7.mks.selcloud.ru/v2", want: "https://ru-7.mks.selcloud.ru"},
		{name: "v2 path with a trailing slash", endpoint: "https://ru-7.mks.selcloud.ru/v2/", want: "https://ru-7.mks.selcloud.ru"},
		{name: "host with port", endpoint: "http://127.0.0.1:8080/v2", want: "http://127.0.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mksV2BaseURL(tt.endpoint)
			if got != tt.want {
				t.Errorf("mksV2BaseURL(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestMKSV2ClientUnknownPool(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	p := &mksV2Provided{config: fake.config("test", func(string) string { return "" })}

	_, _, diags := p.client(t.Context(), types.StringValue("attribute-project"), "ru-0")
	if !diags.HasError() {
		t.Fatal("client accepted a pool the catalog has no endpoint for")
	}
	if !strings.Contains(diags[0].Detail(), "region value must contain one of the values") {
		t.Errorf("unexpected error: %s", diags[0].Detail())
	}
}
