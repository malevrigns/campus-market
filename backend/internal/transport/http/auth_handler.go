package httpapi

import (
	"net/http"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/service"
)

func (s *Server) requestCode(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	var v struct {
		Channel string `json:"channel"`
		Target  string `json:"target"`
		Purpose string `json:"purpose"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	var actor *domain.User
	if _, err := s.sessionCookie(r); err == nil {
		if u, err := s.currentUser(r); err == nil {
			actor = u
		}
	}
	out, err := s.deps.Auth.RequestCode(r.Context(), s.clientIP(r), actor, service.RequestCodeInput{Channel: v.Channel, Target: v.Target, Purpose: v.Purpose})
	if err != nil {
		return err
	}
	return result(w, 200, out)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	var v struct {
		domain.CodeProof
		Name     string `json:"name"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	out, err := s.deps.Auth.Register(r.Context(), service.RegisterInput{CodeProof: v.CodeProof, Name: v.Name, Email: v.Email, Phone: v.Phone, Password: v.Password}, s.sessionHash(r))
	if err != nil {
		return err
	}
	s.writeSession(w, r, out.Session)
	return result(w, 201, out.User)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	var v struct {
		Account  string `json:"account"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	out, err := s.deps.Auth.Login(r.Context(), service.LoginInput{Account: v.Account, Email: v.Email, Password: v.Password}, s.sessionHash(r))
	if err != nil {
		return err
	}
	s.writeSession(w, r, out.Session)
	return result(w, 200, out.User)
}

func (s *Server) codeLogin(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	var proof domain.CodeProof
	if err := decode(r, &proof); err != nil {
		return err
	}
	out, err := s.deps.Auth.CodeLogin(r.Context(), proof, s.sessionHash(r))
	if err != nil {
		return err
	}
	s.writeSession(w, r, out.Session)
	return result(w, 200, out.User)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	var v struct {
		domain.CodeProof
		Password string `json:"password"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	out, err := s.deps.Auth.ResetPassword(r.Context(), service.ResetPasswordInput{CodeProof: v.CodeProof, Password: v.Password}, s.sessionHash(r))
	if err != nil {
		return err
	}
	if out.LoggedOut {
		s.clearSessionCookie(w, r)
	}
	return result(w, 200, map[string]bool{"password_reset": out.Reset, "logged_out": out.LoggedOut})
}

func (s *Server) bindIdentity(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var proof domain.CodeProof
	if err := decode(r, &proof); err != nil {
		return err
	}
	fresh, err := s.deps.Auth.BindIdentity(r.Context(), u, proof)
	if err != nil {
		return err
	}
	return result(w, 200, fresh)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	return result(w, 200, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	if err := s.deps.Auth.Logout(r.Context(), s.sessionHash(r)); err != nil {
		return err
	}
	s.clearSessionCookie(w, r)
	return result(w, 200, map[string]bool{"logged_out": true})
}

func (s *Server) devInbox(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	var v struct {
		ChallengeID string `json:"challenge_id"`
		Token       string `json:"delivery_token"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	out, err := s.deps.Auth.DevInbox(r.Context(), r.RemoteAddr, r.Header.Get("X-Campus-Client-IP"), service.DevInboxInput{ChallengeID: v.ChallengeID, Token: v.Token})
	if err != nil {
		return err
	}
	return result(w, 200, out)
}
