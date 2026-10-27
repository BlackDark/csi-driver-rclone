package identity

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"github.com/BlackDark/csi-driver-rclone/internal/driver"
)

func TestIdentity(t *testing.T) {
	ctx := context.Background()
	s := &Server{Version: "v1.2.3"}
	info, _ := s.GetPluginInfo(ctx, &csi.GetPluginInfoRequest{})
	if info.Name != driver.Name || info.VendorVersion != "v1.2.3" {
		t.Errorf("info: %+v", info)
	}
	if p, _ := s.Probe(ctx, &csi.ProbeRequest{}); !p.Ready.GetValue() {
		t.Error("not ready")
	}
	if c, _ := s.GetPluginCapabilities(ctx, nil); len(c.Capabilities) != 0 {
		t.Errorf("node caps: %v", c.Capabilities)
	}
	s.Controller = true
	c, _ := s.GetPluginCapabilities(ctx, nil)
	if len(c.Capabilities) != 1 || c.Capabilities[0].GetService().Type != csi.PluginCapability_Service_CONTROLLER_SERVICE {
		t.Errorf("controller caps: %v", c.Capabilities)
	}
}
