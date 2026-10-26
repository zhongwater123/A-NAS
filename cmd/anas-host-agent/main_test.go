package main

import (
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestConfigPlacesTheBrokerAndTheAssistantsChangesBesideTheirDefaults(t *testing.T) {
	c, err := loadConfig(map[string]string{"ANAS_HOST_AGENT_SOCKET": "/run/a-nas/host-agent.sock", "ANAS_DATA_MOUNT": "/srv/data"})
	if err != nil {
		t.Fatal(err)
	}
	if c.FileBrokerSocket != "/run/a-nas/file-broker.sock" || c.StateDir != "/var/lib/a-nas" || c.AssistantChanges != "" ||
		c.PhotoSessionGroup != accounts.PhotoServiceUser || c.ShareSessionGroup != accounts.ShareServiceUser {
		t.Fatalf("config = %+v", c)
	}
	// The assistant brings the executor of its changes, on the data volume.
	c, err = loadConfig(map[string]string{"XDG_RUNTIME_DIR": "/run/user/0", "ANAS_DATA_MOUNT": "/srv/data", "ANAS_ASSISTANT_SOCKET_DIR": "/run/a-nas-assistant"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket != "/run/user/0/a-nas/host-agent.sock" || c.AssistantChanges != "/srv/data/assistant/.changes" {
		t.Fatalf("config = %+v", c)
	}
	if _, err := loadConfig(map[string]string{"ANAS_HOST_AGENT_SOCKET": "host-agent.sock"}); err == nil {
		t.Fatal("a relative socket was accepted")
	}
}
