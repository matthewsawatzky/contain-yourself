// Package migrations embeds the immutable, ordered SQLite migrations.
package migrations

import (
	"embed"
	"fmt"
)

//go:embed *.sql
var files embed.FS

var ordered = []string{
	"001_initial.sql",
	"002_apps.sql",
	"003_access_grants.sql",
	"004_vpn_profiles.sql",
	"005_multiuser_wireguard_profiles.sql",
	"006_accent_theme.sql",
	"007_egress_mode.sql",
	"008_user_egress_grants.sql",
}

// All returns migrations in version order.
func All() ([]string, error) {
	result := make([]string, 0, len(ordered))
	for _, name := range ordered {
		data, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		result = append(result, string(data))
	}
	return result, nil
}
