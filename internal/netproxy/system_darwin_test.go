// SPDX-License-Identifier: MPL-2.0
//go:build darwin

package netproxy

import "testing"

func TestParseScutil(t *testing.T) {
	full := `<dictionary> {
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 0
  ProxyAutoConfigEnable : 0
}`
	kv := parseScutil([]byte(full))
	if kv["HTTPProxy"] != "127.0.0.1" || kv["HTTPPort"] != "7890" || kv["HTTPSEnable"] != "1" {
		t.Errorf("full parse: %v", kv)
	}

	off := `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 0
  ProxyAutoConfigEnable : 0
}`
	if kv := parseScutil([]byte(off)); kv["HTTPEnable"] != "0" {
		t.Errorf("off parse: %v", kv)
	}

	pac := `<dictionary> {
  ProxyAutoConfigEnable : 1
  ProxyAutoConfigURLString : http://example.com/proxy.pac
}`
	if kv := parseScutil([]byte(pac)); kv["ProxyAutoConfigURLString"] != "http://example.com/proxy.pac" {
		t.Errorf("pac parse: %v", kv)
	}
}
