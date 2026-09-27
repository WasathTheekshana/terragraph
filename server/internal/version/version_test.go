package version

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in     string
		want   Version
		wantOK bool
	}{
		{"v5.1.0", Version{5, 1, 0, ""}, true},
		{"4.11.0", Version{4, 11, 0, ""}, true},
		{"v2.0.0-rc.1", Version{2, 0, 0, "rc.1"}, true},
		{"1.2.3+build.7", Version{1, 2, 3, ""}, true},
		{"~> 4.0", Version{}, false},
		{"4.1", Version{}, false},
		{"v1", Version{}, false},
		{"main", Version{}, false},
		{"b588428cf7026e378c6438fc9b8a6e7d960c040b", Version{}, false},
		{"", Version{}, false},
	}
	for _, tt := range tests {
		got, ok := Parse(tt.in)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}
