package security

import (
	"net"
	"testing"
)

func TestACL_AllowDeny(t *testing.T) {
	acl, err := NewACL([]string{"10.0.0.0/8", "192.168.1.100"}, []string{"10.0.0.5"})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.0.0.5", false}, // denylist wins even though it's within the allowed CIDR
		{"192.168.1.100", true},
		{"192.168.1.101", false}, // not in allowlist
		{"8.8.8.8", false},
	}
	for _, c := range cases {
		got := acl.Allowed(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("Allowed(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestACL_OpenByDefault(t *testing.T) {
	acl, err := NewACL(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !acl.IsOpen() {
		t.Error("empty allowlist should report IsOpen() == true")
	}
	if !acl.Allowed(net.ParseIP("1.2.3.4")) {
		t.Error("empty ACL should allow all addresses")
	}
}

func TestACL_DenyOnlyStillOpen(t *testing.T) {
	acl, err := NewACL(nil, []string{"1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if !acl.Allowed(net.ParseIP("5.6.7.8")) {
		t.Error("addresses not on the denylist should be allowed when no allowlist is set")
	}
	if acl.Allowed(net.ParseIP("1.2.3.4")) {
		t.Error("denylisted address should be rejected")
	}
}

func TestACL_InvalidEntry(t *testing.T) {
	if _, err := NewACL([]string{"not-an-ip"}, nil); err == nil {
		t.Error("expected error for invalid allowlist entry")
	}
}

func TestACL_NilReceiverAllowsAll(t *testing.T) {
	var acl *ACL
	if !acl.Allowed(net.ParseIP("1.2.3.4")) {
		t.Error("nil *ACL should allow everything (no ACL configured)")
	}
}
