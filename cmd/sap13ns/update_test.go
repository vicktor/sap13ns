package main

import (
	"slices"
	"testing"
)

func TestBackupUnits(t *testing.T) {
	list := "sap13ns-backup.service,sap13ns-network-backup.service"
	for _, tc := range []struct {
		name      string
		installed []string
		want      []string
	}{
		{"single site", []string{"sap13ns-backup.service"}, []string{"sap13ns-backup.service"}},
		{"network", []string{"sap13ns-network-backup.service"}, []string{"sap13ns-network-backup.service"}},
		// A server that became a network keeps the single-site unit, which
		// refuses to run there: only the network's runs.
		{"network with the old unit", []string{"sap13ns-backup.service", "sap13ns-network-backup.service"}, []string{"sap13ns-network-backup.service"}},
		{"none", nil, nil},
	} {
		got := backupUnits(list, func(u string) bool { return slices.Contains(tc.installed, u) })
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
