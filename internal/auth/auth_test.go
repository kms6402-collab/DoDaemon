package auth

import "testing"

func TestAuthenticate_Success(t *testing.T) {
	hash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore([]User{{Username: "alice", PasswordHash: hash, HomeDir: "/home/alice", Permissions: "lr"}})

	u, err := store.Authenticate("alice", "correct-horse")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if u.HomeDir != "/home/alice" {
		t.Errorf("got home dir %q", u.HomeDir)
	}
}

func TestAuthenticate_WrongPassword(t *testing.T) {
	hash, _ := HashPassword("correct-horse")
	store := NewStore([]User{{Username: "alice", PasswordHash: hash}})

	if _, err := store.Authenticate("alice", "wrong"); err != ErrInvalidCredentials {
		t.Errorf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticate_UnknownUser(t *testing.T) {
	store := NewStore(nil)
	if _, err := store.Authenticate("nobody", "anything"); err != ErrInvalidCredentials {
		t.Errorf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestUser_Allows(t *testing.T) {
	u := User{Permissions: "elradfmwMT"}
	for _, p := range []byte("elradfmwMT") {
		if !u.Allows(p) {
			t.Errorf("Allows(%q) = false, want true", p)
		}
	}
	if u.Allows('x') {
		t.Error("Allows('x') = true for a permission char not in the string")
	}

	readOnly := User{Permissions: "lr"}
	if readOnly.Allows(PermStore) {
		t.Error("read-only user should not be allowed to store (w)")
	}
	if !readOnly.Allows(PermRetrieve) {
		t.Error("read-only user should be allowed to retrieve (r)")
	}
}
