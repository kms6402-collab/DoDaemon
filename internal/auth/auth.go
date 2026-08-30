// Package auth implements virtual-user authentication shared by the FTP
// server and the web UI's own login, backed by bcrypt password hashes
// (never plaintext passwords in config or memory longer than needed).
package auth

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("auth: invalid username or password")

// User is a single virtual account.
type User struct {
	Username     string
	PasswordHash string
	HomeDir      string
	Permissions  string // e.g. "elradfmwMT" — see Permissions for meaning
}

// Store is an in-memory, case-sensitive-username credential store. It is
// rebuilt wholesale on every config reload rather than mutated in place,
// so callers should swap the whole *Store via atomic.Pointer.
type Store struct {
	users map[string]User
}

func NewStore(users []User) *Store {
	m := make(map[string]User, len(users))
	for _, u := range users {
		m[u.Username] = u
	}
	return &Store{users: m}
}

// Authenticate verifies username/password and returns the matching User.
// It runs bcrypt.CompareHashAndPassword even when the username is unknown
// (against a fixed dummy hash) so that lookups take constant time
// regardless of whether the account exists, avoiding user-enumeration via
// timing.
const dummyHash = "$2a$10$C6UzMDM.H6dfI/f/IKcEeO0uTaqLKJmiZq2K5ImUOKWc8oxIhwmXe"

func (s *Store) Authenticate(username, password string) (User, error) {
	u, ok := s.users[username]
	hash := dummyHash
	if ok {
		hash = u.PasswordHash
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if !ok || err != nil {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

// HashPassword produces a bcrypt hash suitable for storing in config.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// Permission characters, following the "elradfmwMT" convention shared by
// 3CDaemon-style FTP daemons.
const (
	PermChangeDir = 'e' // change working directory (CWD)
	PermList      = 'l' // list directory contents (LIST/NLST/MLSD)
	PermRetrieve  = 'r' // download (RETR)
	PermAppend    = 'a' // append to an existing file (APPE)
	PermDelete    = 'd' // delete files or directories (DELE/RMD)
	PermRename    = 'f' // rename (RNFR/RNTO)
	PermMakeDir   = 'm' // create directories (MKD)
	PermStore     = 'w' // upload (STOR/STOU)
	PermChmod     = 'M' // change file mode (SITE CHMOD)
	PermModTime   = 'T' // change modification time (MFMT)
)

// Allows reports whether this user's permission string grants perm.
func (u User) Allows(perm byte) bool {
	return strings.IndexByte(u.Permissions, perm) >= 0
}
