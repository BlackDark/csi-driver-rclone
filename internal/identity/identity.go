// Package identity implements the CSI Identity service.
package identity

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/BlackDark/csi-driver-rclone/internal/driver"
)

// Server answers Identity RPCs. Controller advertises CONTROLLER_SERVICE.
type Server struct {
	csi.UnimplementedIdentityServer
	Version    string
	Controller bool
}

// GetPluginInfo returns the driver name and build version.
func (s *Server) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: driver.Name, VendorVersion: s.Version}, nil
}

// Probe reports ready: the gRPC server only runs once dependencies are set up.
func (s *Server) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}

// GetPluginCapabilities lists CONTROLLER_SERVICE in controller mode only.
func (s *Server) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	resp := &csi.GetPluginCapabilitiesResponse{}
	if s.Controller {
		resp.Capabilities = append(resp.Capabilities, &csi.PluginCapability{
			Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{
				Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
			}},
		})
	}
	return resp, nil
}
