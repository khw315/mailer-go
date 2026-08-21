package server

import (
	"errors"

	"github.com/emersion/go-sasl"
)

// LoginAuthenticator validates username and password for SASL LOGIN.
type LoginAuthenticator func(username, password string) error

type loginServer struct {
	authenticator LoginAuthenticator
	step          int
	username      string
}

// NewLoginServer creates a SASL LOGIN server implementation.
func NewLoginServer(authenticator LoginAuthenticator) sasl.Server {
	return &loginServer{
		authenticator: authenticator,
		step:          0,
	}
}

func (s *loginServer) Next(response []byte) (challenge []byte, done bool, err error) {
	switch s.step {
	case 0:
		s.step++
		// Challenge with "Username:"
		return []byte("Username:"), false, nil
	case 1:
		s.username = string(response)
		s.step++
		// Challenge with "Password:"
		return []byte("Password:"), false, nil
	case 2:
		password := string(response)
		if err := s.authenticator(s.username, password); err != nil {
			return nil, true, err
		}
		return nil, true, nil
	default:
		return nil, true, errors.New("sasl: unexpected extra step in LOGIN mechanism")
	}
}
