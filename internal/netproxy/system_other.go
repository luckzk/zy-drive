// SPDX-License-Identifier: MPL-2.0
//go:build !darwin

package netproxy

// detectSystemOS on non-macOS platforms: there is no system proxy
// store to read, so the env vars are the only source (ModeSystem
// falls back to them in resolve).
func detectSystemOS() *SystemProxy {
	return &SystemProxy{}
}
