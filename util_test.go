package netman

import (
	"fmt"
	"os"
	"testing"
)

func TestGetNextEthName(t *testing.T) {
	tests := []struct {
		linkNames []string
		expected  string
	}{
		{[]string{}, "eth0"},
		{[]string{"lo"}, "eth0"},
		{[]string{"lo", "eth0"}, "eth1"},
		{[]string{"lo", "eth0", "eth1"}, "eth2"},
		{[]string{"lo", "eth0", "eth2"}, "eth1"},
		{[]string{"lo", "eth1", "eth2"}, "eth0"},
		{[]string{"lo", "eth0", "eth1", "eth2"}, "eth3"},
		{[]string{"lo", "eth0", "eth1", "eth3"}, "eth2"},
	}

	for _, test := range tests {
		// copy the variable to avoid closure capture issue
		currentTest := test
		t.Run(currentTest.expected, func(t *testing.T) {
			result := GetNextEthName(currentTest.linkNames)
			if result != currentTest.expected {
				t.Errorf("GetNextEthName(%v) = %s; expected %s", currentTest.linkNames, result, currentTest.expected)
			}
		})
	}
}

func TestTranslateContainerNSPath(t *testing.T) {
	wantPath := fmt.Sprintf("/proc/%d/ns/net", os.Getpid())
	gotPath, err := TranslateContainerNSPath(os.Getpid(), wantPath)
	if err != nil {
		t.Errorf("Failed to translate %s: %v", wantPath, err)
	} else if gotPath != wantPath {
		t.Errorf("TranslateContainerNSPath(%s) = %s; want %s", wantPath, gotPath, wantPath)
	}
}
