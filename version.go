package main

import (
	"runtime/debug"
	"strings"
)

var version = "dev"

func currentVersion() string {
	return resolveVersion(version, moduleVersion())
}

func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Version
}

func resolveVersion(injectedVersion, moduleVersion string) string {
	if v := displayVersion(injectedVersion); v != "" && v != "dev" {
		return v
	}
	if v := displayVersion(moduleVersion); v != "" {
		return v
	}
	if v := displayVersion(injectedVersion); v != "" {
		return v
	}
	return "dev"
}

func displayVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "(devel)" {
		return ""
	}
	if strings.HasPrefix(v, "v") && len(v) > 1 && v[1] >= '0' && v[1] <= '9' {
		return v[1:]
	}
	return v
}
