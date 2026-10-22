package doctor

import (
	"reflect"
	"testing"
)

func TestLsofListenerPIDs(t *testing.T) {
	output := "p101\nn*:8837\np102\nn127.0.0.1:8837\np103\nn[::1]:8837\np104\nn127.0.0.2:8837\np105\nn*:8838\np106\nndifferent.example:8837\npbad\nn*:8837\n"
	for _, tc := range []struct {
		bind string
		want []string
	}{
		{"0.0.0.0:8837", []string{"101"}},
		{"[::]:8837", []string{"101"}},
		{"127.0.0.1:8837", []string{"101", "102"}},
		{"[::1]:8837", []string{"101", "103"}},
		{"[0:0:0:0:0:0:0:1]:8837", []string{"101", "103"}},
		{"127.0.0.1:8838", []string{"105"}},
		{"127.0.0.1:8839", nil},
		{"localhost:8837", []string{"101"}},
		{"invalid", nil},
	} {
		t.Run(tc.bind, func(t *testing.T) {
			if got := lsofListenerPIDs(output, tc.bind); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("owners = %v, want %v", got, tc.want)
			}
		})
	}
}
