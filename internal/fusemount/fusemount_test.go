package fusemount

import "testing"

func TestData(t *testing.T) {
	cases := map[string]struct {
		o    Opts
		want string
	}{
		"plain": {Opts{UID: 1000, GID: 2000}, "fd=7,rootmode=40000,user_id=1000,group_id=2000,allow_other"},
		"default permissions": {Opts{UID: 1, GID: 1, DefaultPermissions: true},
			"fd=7,rootmode=40000,user_id=1,group_id=1,allow_other,default_permissions"},
		"read-only does not touch data": {Opts{UID: 0, GID: 0, ReadOnly: true},
			"fd=7,rootmode=40000,user_id=0,group_id=0,allow_other"},
	}
	for name, c := range cases {
		if got := Data(7, c.o); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
