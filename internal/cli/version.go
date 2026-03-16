package cli

import "runtime/debug"

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "(unknown)"
	}

	return info.Main.Version
}
