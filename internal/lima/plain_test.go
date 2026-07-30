// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import "testing"

// Lima's plain mode silently ignores mounts, port forwarding and containerd,
// and does not start the guest agent that implements forwarding. Nothing
// errors; the settings simply have no effect. Detecting it is what lets the
// provider say so at plan time.
func TestPlainMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		doc       string
		wantPlain bool
		wantKnown bool
	}{
		{name: "plain true", doc: "plain: true\n", wantPlain: true, wantKnown: true},
		{name: "plain false", doc: "plain: false\n", wantKnown: true},
		{name: "plain absent", doc: "cpus: 2\n", wantKnown: true},
		{name: "empty document", doc: "", wantKnown: true},
		{name: "not a mapping", doc: "- a\n- b\n"},
		{name: "malformed yaml", doc: "\tnope\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plain, known := PlainMode(tt.doc)
			if plain != tt.wantPlain || known != tt.wantKnown {
				t.Errorf("PlainMode(%q) = (%v, %v), want (%v, %v)",
					tt.doc, plain, known, tt.wantPlain, tt.wantKnown)
			}
		})
	}
}
