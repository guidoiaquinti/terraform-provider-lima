// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

// PlainMode reports whether a Lima document enables plain mode, and whether the
// document could be read at all.
//
// Plain mode is worth singling out because of how quietly it changes things:
// Lima ignores the mounts, port forwarding and containerd settings outright and
// does not start the guest agent that implements forwarding. A configuration
// declaring port forwards alongside `plain: true` is accepted, applies cleanly,
// and simply has no forwards.
//
// known is false when the document is not a YAML mapping, in which case the
// caller should stay quiet rather than guess.
func PlainMode(doc string) (bool, bool) {
	m, err := parseMapping(doc, "config")
	if err != nil {
		return false, false
	}
	value, ok := m["plain"].(bool)
	if !ok {
		return false, true
	}
	return value, true
}
