package node

import (
	"errors"
	"reflect"
	"testing"
)

type recSys struct {
	calls   []string
	failRO  bool
	failAll bool
}

func (r *recSys) bind(s, t string) error {
	r.calls = append(r.calls, "bind "+s+" "+t)
	if r.failAll {
		return errors.New("boom")
	}
	return nil
}

func (r *recSys) remountReadOnly(t string) error {
	r.calls = append(r.calls, "ro "+t)
	if r.failRO {
		return errors.New("boom")
	}
	return nil
}

func (r *recSys) unmount(t string) error {
	r.calls = append(r.calls, "umount "+t)
	return nil
}

func TestBindMounter(t *testing.T) {
	cases := map[string]struct {
		fstype  string
		opts    []string
		sys     recSys
		want    []string
		wantErr bool
	}{
		"rw":         {opts: []string{"bind"}, want: []string{"bind s t"}},
		"ro":         {opts: []string{"bind", "ro"}, want: []string{"bind s t", "ro t"}},
		"ro fails":   {opts: []string{"bind", "ro"}, sys: recSys{failRO: true}, want: []string{"bind s t", "ro t", "umount t"}, wantErr: true},
		"bind fails": {opts: []string{"bind"}, sys: recSys{failAll: true}, want: []string{"bind s t"}, wantErr: true},
		"not bind":   {opts: []string{"ro"}, wantErr: true},
		"fstype":     {fstype: "ext4", opts: []string{"bind"}, wantErr: true},
		"unknown":    {opts: []string{"bind", "noexec"}, wantErr: true},
	}
	for name, c := range cases {
		sys := c.sys
		b := &BindMounter{sys: &sys}
		err := b.Mount("s", "t", c.fstype, c.opts)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err %v", name, err)
		}
		if !reflect.DeepEqual(sys.calls, c.want) {
			t.Errorf("%s: calls %v, want %v", name, sys.calls, c.want)
		}
	}
	sys := recSys{}
	if err := (&BindMounter{sys: &sys}).Unmount("t"); err != nil || len(sys.calls) != 1 {
		t.Errorf("unmount: %v %v", err, sys.calls)
	}
}
