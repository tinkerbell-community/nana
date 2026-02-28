package unifi

import (
	"testing"

	"github.com/ubiquiti-community/go-unifi/unifi"
)

func TestParsePoEStatus(t *testing.T) {
	output := `Total Power Limit(mW): 150000
Port   OpMode   HpMode    PwrLimit   Class      PoEPwr PwrGood  Pwr(W)  Volt(V) Curr(mA)
----   ------   ------    --------   -----      ------ -------  ------  ------- --------
3      Auto     Dot3at    30000      Class 4    On     Good     12.5    53.2    235.0
`
	status, err := parsePoEStatus(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(status.ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(status.ports))
	}
	if status.ports[0].port != 3 {
		t.Errorf("expected port 3, got %d", status.ports[0].port)
	}
	if status.ports[0].poePwr != "On" {
		t.Errorf("expected poePwr 'On', got %q", status.ports[0].poePwr)
	}
}

func TestParseMacList(t *testing.T) {
	output := `Port   VLAN    MAC                IP              Hostname         Uptime   Age    Type
----   ----    --                --              --------         ------   ---    ----
3      1       aa:bb:cc:dd:ee:ff 192.168.1.100   server-01        1000     500
5      1       11:22:33:44:55:66 192.168.1.101   server-02        2000     600
Total number of entries: 2
`
	ml, err := parseMacList(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ml.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(ml.entries))
	}
	if ml.entries[0].port != 3 {
		t.Errorf("expected port 3, got %d", ml.entries[0].port)
	}
	if ml.entries[0].macAddress != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected MAC aa:bb:cc:dd:ee:ff, got %q", ml.entries[0].macAddress)
	}
	if ml.entries[1].port != 5 {
		t.Errorf("expected port 5, got %d", ml.entries[1].port)
	}
}

func TestFindPortByMAC(t *testing.T) {
	portTable := []unifi.DevicePortTable{
		{PortIdx: 1, LastConnection: unifi.DeviceLastConnection{MAC: "aa:bb:cc:dd:ee:ff"}},
		{PortIdx: 5, LastConnection: unifi.DeviceLastConnection{MAC: "2c:cf:67:0a:a3:33"}},
		{PortIdx: 8, LastConnection: unifi.DeviceLastConnection{MAC: ""}},
	}

	tests := []struct {
		name string
		mac  string
		want int
	}{
		{"exact match", "2c:cf:67:0a:a3:33", 5},
		{"case insensitive", "2C:CF:67:0A:A3:33", 5},
		{"dash format", "2c-cf-67-0a-a3-33", 5},
		{"other device", "aa:bb:cc:dd:ee:ff", 1},
		{"no match", "11:22:33:44:55:66", 0},
		{"empty table", "2c:cf:67:0a:a3:33", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := portTable
			if tt.name == "empty table" {
				table = nil
			}
			got := findPortByMAC(table, tt.mac)
			if got != tt.want {
				t.Errorf("findPortByMAC() = %d, want %d", got, tt.want)
			}
		})
	}
}
