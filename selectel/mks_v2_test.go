package selectel

import "testing"

func TestMKSV2BaseURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "v1 path", endpoint: "https://ru-7.mks.selcloud.ru/v1", want: "https://ru-7.mks.selcloud.ru"},
		{name: "v1 path with a trailing slash", endpoint: "https://ru-7.mks.selcloud.ru/v1/", want: "https://ru-7.mks.selcloud.ru"},
		{name: "prefixed path", endpoint: "https://api.example.com/mks/v1", want: "https://api.example.com"},
		{name: "host with port", endpoint: "http://127.0.0.1:8080/v1", want: "http://127.0.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mksV2BaseURL(tt.endpoint)
			if err != nil {
				t.Fatalf("mksV2BaseURL(%q) error: %s", tt.endpoint, err)
			}
			if got != tt.want {
				t.Errorf("mksV2BaseURL(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}

	_, err := mksV2BaseURL("https://ru-7.mks.selcloud.ru:bad-port/v1")
	if err == nil {
		t.Error("mksV2BaseURL accepted an endpoint with an invalid port")
	}
}
