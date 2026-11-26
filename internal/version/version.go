// SPDX-License-Identifier: MPL-2.0
// Package version holds the application, engine and protocol versions.
// Desktop and Agent ship independent app versions; the protocol version
// is bumped only when the Agent API changes. Every release ships both
// entries together and the release notes state whether the protocol
// version changed.
//
// AppVersion mirrors the VERSION file at the repository root.
// version_test.go fails the build if the two drift apart — bump both
// in the same commit.
package version

// AppVersion is the backend version.
const AppVersion = "0.13.0"

// EngineVersion identifies the sync/transfer engine build.
const EngineVersion = "0.1.0"

// ProtocolVersion is the desktop ↔ Agent protocol version.
const ProtocolVersion = "1"
