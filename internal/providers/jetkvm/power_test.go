package jetkvm

import (
	"bytes"
	"testing"
)

func TestBuildMagicPacket(t *testing.T) {
	tests := []struct {
		name    string
		mac     string
		wantErr bool
	}{
		{name: "colon-separated", mac: "AA:BB:CC:DD:EE:FF"},
		{name: "hyphen-separated", mac: "AA-BB-CC-DD-EE-FF"},
		{name: "dot-separated", mac: "AABB.CCDD.EEFF"},
		{name: "lowercase", mac: "aa:bb:cc:dd:ee:ff"},
		{name: "invalid too short", mac: "AA:BB:CC", wantErr: true},
		{name: "invalid characters", mac: "ZZ:BB:CC:DD:EE:FF", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkt, err := buildMagicPacket(tt.mac)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(pkt) != 102 {
				t.Fatalf("expected 102-byte packet, got %d", len(pkt))
			}
			// First 6 bytes must be 0xFF
			for i := range 6 {
				if pkt[i] != 0xFF {
					t.Fatalf("byte %d: expected 0xFF, got 0x%02X", i, pkt[i])
				}
			}
			// MAC must repeat 16 times starting at offset 6
			mac := pkt[6:12]
			for i := range 16 {
				seg := pkt[6+i*6 : 6+i*6+6]
				if !bytes.Equal(seg, mac) {
					t.Fatalf("repetition %d: got %x, want %x", i, seg, mac)
				}
			}
		})
	}
}
