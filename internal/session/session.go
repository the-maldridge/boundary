package session

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"strings"
	"time"

	"github.com/hashicorp/boundary/internal/db"
	"github.com/hashicorp/boundary/internal/db/timestamp"
	wrapping "github.com/hashicorp/go-kms-wrapping"
	"github.com/hashicorp/go-kms-wrapping/structwrapping"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultSessionTableName = "session"
)

// ComposedOf defines the boundary data that is referenced to compose a session.
type ComposedOf struct {
	// UserId of the session
	UserId string
	// HostId of the session
	HostId string
	// TargetId of the session
	TargetId string
	// HostSetId of the session
	HostSetId string
	// AuthTokenId of the session
	AuthTokenId string
	// ScopeId of the session
	ScopeId string
	// Endpoint. This is generated by the target, but is not stored in the
	// warehouse as the worker may need to e.g. resolve DNS. This is to round
	// trip the information to the worker when it validates a session.
	Endpoint string
	// Expiration time for the session
	ExpirationTime *timestamp.Timestamp
	// Max connections for the session
	ConnectionLimit int32
}

// Session contains information about a user's session with a target
type Session struct {
	// PublicId is used to access the session via an API
	PublicId string `json:"public_id,omitempty" gorm:"primary_key"`
	// UserId for the session
	UserId string `json:"user_id,omitempty" gorm:"default:null"`
	// HostId of the session
	HostId string `json:"host_id,omitempty" gorm:"default:null"`
	// ServerId that proxied the session
	ServerId string `json:"server_id,omitempty" gorm:"default:null"`
	// ServerType that proxied the session
	ServerType string `json:"server_type,omitempty" gorm:"default:null"`
	// TargetId for the session
	TargetId string `json:"target_id,omitempty" gorm:"default:null"`
	// HostSetId for the session
	HostSetId string `json:"host_set_id,omitempty" gorm:"default:null"`
	// AuthTokenId for the session
	AuthTokenId string `json:"auth_token_id,omitempty" gorm:"default:null"`
	// ScopeId for the session
	ScopeId string `json:"scope_id,omitempty" gorm:"default:null"`
	// Certificate to use when connecting (or if using custom certs, to
	// serve as the "login"). Raw DER bytes.  Private key is not, and should not be
	// stored in the database.
	Certificate []byte `json:"certificate,omitempty" gorm:"default:null"`
	// ExpirationTime - after this time the connection will be expired, e.g. forcefully terminated
	ExpirationTime *timestamp.Timestamp `json:"expiration_time,omitempty" gorm:"default:null"`
	// CtTofuToken is the ciphertext Tofutoken value stored in the database
	CtTofuToken []byte `json:"ct_tofu_token,omitempty" gorm:"column:tofu_token;default:null" wrapping:"ct,tofu_token"`
	// TofuToken - plain text of the "trust on first use" token for session
	TofuToken []byte `json:"tofu_token,omitempty" gorm:"-" wrapping:"pt,tofu_token"`
	// termination_reason for the session
	TerminationReason string `json:"termination_reason,omitempty" gorm:"default:null"`
	// CreateTime from the RDBMS
	CreateTime *timestamp.Timestamp `json:"create_time,omitempty" gorm:"default:current_timestamp"`
	// UpdateTime from the RDBMS
	UpdateTime *timestamp.Timestamp `json:"update_time,omitempty" gorm:"default:current_timestamp"`
	// Version for the session
	Version uint32 `json:"version,omitempty" gorm:"default:null"`
	// Endpoint
	Endpoint string `json:"-" gorm:"default:null"`
	// Maximum number of connections in a session
	ConnectionLimit int32 `json:"connection_limit,omitempty" gorm:"default:null"`

	// key_id is the key ID that was used for the encryption operation. It can be
	// used to identify a specific version of the key needed to decrypt the value,
	// which is useful for caching purposes.
	// @inject_tag: `gorm:"not_null"`
	KeyId string `json:"key_id,omitempty" gorm:"not_null"`

	// States for the session which are for read only and are ignored during
	// write operations
	States    []*State `gorm:"-"`
	tableName string   `gorm:"-"`
}

func (s *Session) GetPublicId() string {
	return s.PublicId
}

var _ Cloneable = (*Session)(nil)
var _ db.VetForWriter = (*Session)(nil)

// New creates a new in memory session.
func New(c ComposedOf, opt ...Option) (*Session, error) {
	s := Session{
		UserId:          c.UserId,
		HostId:          c.HostId,
		TargetId:        c.TargetId,
		HostSetId:       c.HostSetId,
		AuthTokenId:     c.AuthTokenId,
		ScopeId:         c.ScopeId,
		Endpoint:        c.Endpoint,
		ExpirationTime:  c.ExpirationTime,
		ConnectionLimit: c.ConnectionLimit,
	}
	if err := s.validateNewSession("new session:"); err != nil {
		return nil, err
	}
	return &s, nil
}

// AllocSession will allocate a Session
func AllocSession() Session {
	return Session{}
}

// Clone creates a clone of the Session
func (s *Session) Clone() interface{} {
	clone := &Session{
		PublicId:          s.PublicId,
		UserId:            s.UserId,
		HostId:            s.HostId,
		ServerId:          s.ServerId,
		ServerType:        s.ServerType,
		TargetId:          s.TargetId,
		HostSetId:         s.HostSetId,
		AuthTokenId:       s.AuthTokenId,
		ScopeId:           s.ScopeId,
		TerminationReason: s.TerminationReason,
		Version:           s.Version,
		Endpoint:          s.Endpoint,
		ConnectionLimit:   s.ConnectionLimit,
	}
	if s.TofuToken != nil {
		clone.TofuToken = make([]byte, len(s.TofuToken))
		copy(clone.TofuToken, s.TofuToken)
	}
	if s.CtTofuToken != nil {
		clone.CtTofuToken = make([]byte, len(s.CtTofuToken))
		copy(clone.CtTofuToken, s.CtTofuToken)
	}
	if s.Certificate != nil {
		clone.Certificate = make([]byte, len(s.Certificate))
		copy(clone.Certificate, s.Certificate)
	}
	if s.ExpirationTime != nil {
		clone.ExpirationTime = &timestamp.Timestamp{
			Timestamp: &timestamppb.Timestamp{
				Seconds: s.ExpirationTime.Timestamp.Seconds,
				Nanos:   s.ExpirationTime.Timestamp.Nanos,
			},
		}
	}
	if s.CreateTime != nil {
		clone.CreateTime = &timestamp.Timestamp{
			Timestamp: &timestamppb.Timestamp{
				Seconds: s.CreateTime.Timestamp.Seconds,
				Nanos:   s.CreateTime.Timestamp.Nanos,
			},
		}
	}
	if s.UpdateTime != nil {
		clone.UpdateTime = &timestamp.Timestamp{
			Timestamp: &timestamppb.Timestamp{
				Seconds: s.UpdateTime.Timestamp.Seconds,
				Nanos:   s.UpdateTime.Timestamp.Nanos,
			},
		}
	}
	return clone
}

// VetForWrite implements db.VetForWrite() interface and validates the session
// before it's written.
func (s *Session) VetForWrite(ctx context.Context, r db.Reader, opType db.OpType, opt ...db.Option) error {
	opts := db.GetOpts(opt...)
	if s.PublicId == "" {
		return fmt.Errorf("session vet for write: missing public id: %w", db.ErrInvalidParameter)
	}
	switch opType {
	case db.CreateOp:
		if err := s.validateNewSession("session vet for write:"); err != nil {
			return err
		}
		if len(s.Certificate) == 0 {
			return fmt.Errorf("session vet for write: certificate is missing: %w", db.ErrInvalidParameter)
		}
	case db.UpdateOp:
		switch {
		case contains(opts.WithFieldMaskPaths, "PublicId"):
			return fmt.Errorf("session vet for write: public id is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "UserId"):
			return fmt.Errorf("session vet for write: user id is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "HostId"):
			return fmt.Errorf("session vet for write: host id is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "TargetId"):
			return fmt.Errorf("session vet for write: target id is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "HostSetId"):
			return fmt.Errorf("session vet for write: host set id is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "AuthTokenId"):
			return fmt.Errorf("session vet for write: auth token id is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "Certificate"):
			return fmt.Errorf("session vet for write: certificate is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "CreateTime"):
			return fmt.Errorf("session vet for write: create time is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "UpdateTime"):
			return fmt.Errorf("session vet for write: update time is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "Endpoint"):
			return fmt.Errorf("session vet for write: endpoint is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "ExpirationTime"):
			return fmt.Errorf("session vet for write: expiration time is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "ConnectionLimit"):
			return fmt.Errorf("session vet for write: connection limit is immutable: %w", db.ErrInvalidParameter)
		case contains(opts.WithFieldMaskPaths, "TerminationReason"):
			if _, err := convertToReason(s.TerminationReason); err != nil {
				return fmt.Errorf("session vet for write: termination reason '%s' is invalid: %w", s.TerminationReason, db.ErrInvalidParameter)
			}
		}
	}
	return nil
}

// TableName returns the tablename to override the default gorm table name
func (s *Session) TableName() string {
	if s.tableName != "" {
		return s.tableName
	}
	return defaultSessionTableName
}

// SetTableName sets the tablename and satisfies the ReplayableMessage
// interface. If the caller attempts to set the name to "" the name will be
// reset to the default name.
func (s *Session) SetTableName(n string) {
	s.tableName = n
}

// validateNewSession checks everything but the session's PublicId
func (s *Session) validateNewSession(errorPrefix string) error {
	if s.UserId == "" {
		return fmt.Errorf("%s missing user id: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.HostId == "" {
		return fmt.Errorf("%s missing host id: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.TargetId == "" {
		return fmt.Errorf("%s missing target id: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.HostSetId == "" {
		return fmt.Errorf("%s missing host set id: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.AuthTokenId == "" {
		return fmt.Errorf("%s missing auth token id: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.ScopeId == "" {
		return fmt.Errorf("%s missing scope id: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.Endpoint == "" {
		return fmt.Errorf("%s missing endpoint: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.ExpirationTime.GetTimestamp().AsTime().IsZero() {
		return fmt.Errorf("%s missing expiration time: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.TerminationReason != "" {
		return fmt.Errorf("%s termination reason must be empty: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.ServerId != "" {
		return fmt.Errorf("%s server id must be empty: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.ServerType != "" {
		return fmt.Errorf("%s server type must be empty: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.TofuToken != nil {
		return fmt.Errorf("%s tofu token must be empty: %w", errorPrefix, db.ErrInvalidParameter)
	}
	if s.CtTofuToken != nil {
		return fmt.Errorf("%s ct must be empty: %w", errorPrefix, db.ErrInvalidParameter)
	}
	return nil
}

func contains(ss []string, t string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, t) {
			return true
		}
	}
	return false
}

func newCert(wrapper wrapping.Wrapper, userId, jobId string, exp time.Time) (ed25519.PrivateKey, []byte, error) {
	if wrapper == nil {
		return nil, nil, fmt.Errorf("new session cert: missing wrapper: %w", db.ErrInvalidParameter)
	}
	if userId == "" {
		return nil, nil, fmt.Errorf("new session cert: missing user id: %w", db.ErrInvalidParameter)
	}
	if jobId == "" {
		return nil, nil, fmt.Errorf("new session cert: missing job id: %w", db.ErrInvalidParameter)
	}
	pubKey, privKey, err := DeriveED25519Key(wrapper, userId, jobId)
	if err != nil {
		return nil, nil, fmt.Errorf("new session cert: ")
	}
	template := &x509.Certificate{
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		DNSNames:              []string{jobId},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageKeyAgreement | x509.KeyUsageCertSign,
		SerialNumber:          big.NewInt(mathrand.Int63()),
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              exp,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, pubKey, privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("new session cert: %w", err)
	}
	return privKey, certBytes, nil
}

func (s *Session) encrypt(ctx context.Context, cipher wrapping.Wrapper) error {
	if err := structwrapping.WrapStruct(ctx, cipher, s, nil); err != nil {
		return fmt.Errorf("error encrypting session: %w", err)
	}
	s.KeyId = cipher.KeyID()
	return nil
}

func (s *Session) decrypt(ctx context.Context, cipher wrapping.Wrapper) error {
	if err := structwrapping.UnwrapStruct(ctx, cipher, s, nil); err != nil {
		return fmt.Errorf("error decrypting session: %w", err)
	}
	return nil
}

type sessionView struct {
	// Session fields
	PublicId          string               `json:"public_id,omitempty" gorm:"primary_key"`
	UserId            string               `json:"user_id,omitempty" gorm:"default:null"`
	HostId            string               `json:"host_id,omitempty" gorm:"default:null"`
	ServerId          string               `json:"server_id,omitempty" gorm:"default:null"`
	ServerType        string               `json:"server_type,omitempty" gorm:"default:null"`
	TargetId          string               `json:"target_id,omitempty" gorm:"default:null"`
	HostSetId         string               `json:"host_set_id,omitempty" gorm:"default:null"`
	AuthTokenId       string               `json:"auth_token_id,omitempty" gorm:"default:null"`
	ScopeId           string               `json:"scope_id,omitempty" gorm:"default:null"`
	Certificate       []byte               `json:"certificate,omitempty" gorm:"default:null"`
	ExpirationTime    *timestamp.Timestamp `json:"expiration_time,omitempty" gorm:"default:null"`
	CtTofuToken       []byte               `json:"ct_tofu_token,omitempty" gorm:"column:tofu_token;default:null" wrapping:"ct,tofu_token"`
	TofuToken         []byte               `json:"tofu_token,omitempty" gorm:"-" wrapping:"pt,tofu_token"`
	TerminationReason string               `json:"termination_reason,omitempty" gorm:"default:null"`
	CreateTime        *timestamp.Timestamp `json:"create_time,omitempty" gorm:"default:current_timestamp"`
	UpdateTime        *timestamp.Timestamp `json:"update_time,omitempty" gorm:"default:current_timestamp"`
	Version           uint32               `json:"version,omitempty" gorm:"default:null"`
	Endpoint          string               `json:"-" gorm:"default:null"`
	ConnectionLimit   int32                `json:"connection_limit,omitempty" gorm:"default:null"`
	KeyId             string               `json:"key_id,omitempty" gorm:"not_null"`

	// State fields
	Status          string               `json:"state,omitempty" gorm:"column:state"`
	PreviousEndTime *timestamp.Timestamp `json:"previous_end_time,omitempty" gorm:"default:current_timestamp"`
	StartTime       *timestamp.Timestamp `json:"start_time,omitempty" gorm:"default:current_timestamp;primary_key"`
	EndTime         *timestamp.Timestamp `json:"end_time,omitempty" gorm:"default:current_timestamp"`
}

// TableName returns the tablename to override the default gorm table name
func (s *sessionView) TableName() string {
	return "session_with_state"
}
