//go:build !linux

package main

import "log/slog"

func dropPrivileges(string, *slog.Logger) error { return nil }

func networkFS(string) string { return "" }
