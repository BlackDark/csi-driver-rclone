package controller

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newServer() *Server { return &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))} }

func mountCap(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
	}
}

func params(extra map[string]string) map[string]string {
	p := map[string]string{
		"remote":          "minio",
		"remotePath":      "bucket/${pvc.namespace}/${pvc.name}",
		"vfs-cache-mode":  "writes",
		ParamPVCName:      "data",
		ParamPVCNamespace: "team-a",
		ParamPVName:       "pvc-123",
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func createReq(p map[string]string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:               "pvc-123",
		CapacityRange:      &csi.CapacityRange{RequiredBytes: 1 << 30},
		VolumeCapabilities: []*csi.VolumeCapability{mountCap(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
		Parameters:         p,
	}
}

func TestCreateVolume(t *testing.T) {
	s := newServer()
	resp, err := s.CreateVolume(context.Background(), createReq(params(nil)))
	if err != nil {
		t.Fatal(err)
	}
	v := resp.Volume
	want := map[string]string{"remote": "minio", "remotePath": "bucket/team-a/data", "vfs-cache-mode": "writes"}
	if v.VolumeId != "pvc-123" || v.CapacityBytes != 1<<30 || !reflect.DeepEqual(v.VolumeContext, want) {
		t.Errorf("volume: %+v", v)
	}
	again, _ := s.CreateVolume(context.Background(), createReq(params(nil)))
	if !reflect.DeepEqual(again.Volume.VolumeContext, v.VolumeContext) || again.Volume.VolumeId != v.VolumeId {
		t.Error("retry not idempotent")
	}
}

func TestExpandPath(t *testing.T) {
	p := params(nil)
	ok := map[string]string{
		"${pv.name}":                     "pvc-123",
		"b/${pvc.namespace}-${pvc.name}": "b/team-a-data",
		"x/${pvc.name}/y":                "x/data/y",
	}
	for tmpl, want := range ok {
		if got, err := expandPath(tmpl, p); err != nil || got != want {
			t.Errorf("%q: got %q, %v", tmpl, got, err)
		}
	}
	for _, tmpl := range []string{
		"bucket", "bucket/${pvc.namespace}", "${pvc.uid}", "b/${pvc.name", "/b/${pvc.name}",
		"b/../${pvc.name}", "b//${pvc.name}", "./${pvc.name}", "${pvc.name}/",
	} {
		if _, err := expandPath(tmpl, p); err == nil {
			t.Errorf("%q accepted", tmpl)
		}
	}
	if _, err := expandPath("${pvc.name}", map[string]string{}); err == nil || !strings.Contains(err.Error(), "extra-create-metadata") {
		t.Errorf("missing metadata: %v", err)
	}
}

func TestCreateVolumeRejects(t *testing.T) {
	block := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}
	noPath := params(nil)
	delete(noPath, "remotePath")
	cases := map[string]func(*csi.CreateVolumeRequest){
		"no name":       func(r *csi.CreateVolumeRequest) { r.Name = "" },
		"no caps":       func(r *csi.CreateVolumeRequest) { r.VolumeCapabilities = nil },
		"block":         func(r *csi.CreateVolumeRequest) { r.VolumeCapabilities = []*csi.VolumeCapability{block} },
		"credential":    func(r *csi.CreateVolumeRequest) { r.Parameters = params(map[string]string{"configData": "x"}) },
		"unknown flag":  func(r *csi.CreateVolumeRequest) { r.Parameters = params(map[string]string{"rc-addr": ":1"}) },
		"no remotePath": func(r *csi.CreateVolumeRequest) { r.Parameters = noPath },
		"traversal": func(r *csi.CreateVolumeRequest) {
			r.Parameters = params(map[string]string{"remotePath": "../${pv.name}"})
		},
		"content source": func(r *csi.CreateVolumeRequest) {
			r.VolumeContentSource = &csi.VolumeContentSource{}
		},
	}
	for name, mut := range cases {
		r := createReq(params(nil))
		mut(r)
		_, err := newServer().CreateVolume(context.Background(), r)
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: got %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "configData\" = x") {
			t.Errorf("%s leaks value", name)
		}
	}
}

func TestAllAccessModes(t *testing.T) {
	for m := range csi.VolumeCapability_AccessMode_Mode_name {
		mode := csi.VolumeCapability_AccessMode_Mode(m)
		if mode == csi.VolumeCapability_AccessMode_UNKNOWN {
			continue
		}
		r := createReq(params(nil))
		r.VolumeCapabilities = []*csi.VolumeCapability{mountCap(mode)}
		if _, err := newServer().CreateVolume(context.Background(), r); err != nil {
			t.Errorf("%v: %v", mode, err)
		}
	}
}

func TestDeleteVolume(t *testing.T) {
	s := newServer()
	for range 2 {
		if _, err := s.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "pvc-123"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty id: %v", err)
	}
}

func TestValidateVolumeCapabilities(t *testing.T) {
	s := newServer()
	ctx := context.Background()
	ok, err := s.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: "v", VolumeCapabilities: []*csi.VolumeCapability{mountCap(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)},
	})
	if err != nil || ok.Confirmed == nil {
		t.Errorf("mount: %v %v", ok, err)
	}
	block := &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}
	no, err := s.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: "v", VolumeCapabilities: []*csi.VolumeCapability{block},
	})
	if err != nil || no.Confirmed != nil {
		t.Errorf("block: %v %v", no, err)
	}
	if _, err := s.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: "v"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no caps: %v", err)
	}
}

func TestControllerGetCapabilities(t *testing.T) {
	c, _ := newServer().ControllerGetCapabilities(context.Background(), nil)
	if len(c.Capabilities) != 1 || c.Capabilities[0].GetRpc().Type != csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME {
		t.Errorf("caps: %v", c.Capabilities)
	}
}
