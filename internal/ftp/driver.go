package ftp

import (
	"crypto/tls"
	"fmt"
	"log/slog"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/xeonliu/TboxWebdav/internal/auth"
	"github.com/xeonliu/TboxWebdav/internal/config"
	"github.com/xeonliu/TboxWebdav/internal/tbox"
)

// Driver implements ftpserver.MainDriver backed by the Tbox API.
type Driver struct {
	client     *tbox.Client
	cfg        *config.Config
	credCache  *tbox.CredCache
	tokenCache *tbox.TokenCache
}

// NewDriver creates a new FTP Driver.
func NewDriver(client *tbox.Client, cfg *config.Config) *Driver {
	return &Driver{
		client:     client,
		cfg:        cfg,
		credCache:  tbox.GlobalCredCache,
		tokenCache: tbox.GlobalTokenCache,
	}
}

// GetSettings returns the FTP server settings derived from cfg.
func (d *Driver) GetSettings() (*ftpserver.Settings, error) {
	s := &ftpserver.Settings{
		ListenAddr:        fmt.Sprintf("%s:%d", d.cfg.Host, d.cfg.FTPPort),
		ActiveConnectionsCheck: ftpserver.IPMatchDisabled,
		PasvConnectionsCheck:   ftpserver.IPMatchDisabled,
	}
	if d.cfg.FTPPassiveHost != "" {
		s.PublicHost = d.cfg.FTPPassiveHost
	}
	if d.cfg.FTPPassivePortStart > 0 && d.cfg.FTPPassivePortEnd >= d.cfg.FTPPassivePortStart {
		s.PassiveTransferPortRange = &ftpserver.PortRange{
			Start: d.cfg.FTPPassivePortStart,
			End:   d.cfg.FTPPassivePortEnd,
		}
	}
	return s, nil
}

// ClientConnected is called when a new client connects.
func (d *Driver) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	slog.Info("ftp: client connected", "id", cc.ID(), "addr", cc.RemoteAddr())
	return "TboxWebdav FTP Server", nil
}

// ClientDisconnected is called when a client disconnects.
func (d *Driver) ClientDisconnected(cc ftpserver.ClientContext) {
	slog.Info("ftp: client disconnected", "id", cc.ID())
}

// GetTLSConfig returns nil (TLS not configured).
func (d *Driver) GetTLSConfig() (*tls.Config, error) {
	return nil, nil
}

// AuthUser authenticates user/pass and returns a per-user ClientDriver (TboxFs).
func (d *Driver) AuthUser(cc ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	userToken, jaCookie, am, err := d.resolveCredentials(user, pass)
	if err != nil {
		slog.Warn("ftp: auth failed", "user", user, "error", err)
		return nil, fmt.Errorf("authentication failed")
	}

	// Exchange JaCookie for a UserToken if needed.
	if userToken == "" && jaCookie != "" {
		if cached := d.tokenCache.Get(jaCookie); cached != "" {
			userToken = cached
		} else {
			loginRes, loginErr := d.client.LoginUseJaccount(jaCookie)
			if loginErr != nil {
				slog.Warn("ftp: JaCookie login failed", "error", loginErr)
				return nil, fmt.Errorf("authentication failed")
			}
			d.tokenCache.Set(jaCookie, loginRes.UserToken)
			userToken = loginRes.UserToken
		}
	}

	cred, credErr := d.resolveCred(userToken)
	if credErr != nil {
		slog.Error("ftp: failed to get space credentials", "error", credErr)
		return nil, fmt.Errorf("authentication failed")
	}

	slog.Info("ftp: user authenticated", "user", user, "access", am)
	return NewTboxFs(d.client, cred, tbox.GlobalDirCache, am), nil
}

// resolveCredentials extracts a userToken or jaCookie from the given user/pass pair
// according to the configured AuthMode. It mirrors the logic in auth.Middleware.
func (d *Driver) resolveCredentials(user, pass string) (userToken, jaCookie string, am config.AccessMode, err error) {
	am = d.cfg.AccessMode

	switch d.cfg.AuthMode {
	case config.AuthModeNone:
		if d.cfg.UserToken != "" {
			userToken = d.cfg.UserToken
		} else if d.cfg.Cookie != "" {
			jaCookie = d.cfg.Cookie
		} else {
			err = fmt.Errorf("no credentials configured")
		}

	case config.AuthModeUserToken:
		if auth.IsValidUserToken(pass) {
			userToken = pass
		} else {
			err = fmt.Errorf("invalid user token")
		}

	case config.AuthModeJaCookie:
		if auth.IsValidJaCookie(pass) {
			jaCookie = pass
		} else {
			err = fmt.Errorf("invalid JaCookie")
		}

	case config.AuthModeCustom:
		userToken, jaCookie, am, err = d.resolveCustomUser(user, pass)

	case config.AuthModeMixed:
		if auth.IsValidUserToken(pass) {
			userToken = pass
		} else if auth.IsValidJaCookie(pass) {
			jaCookie = pass
		} else {
			userToken, jaCookie, am, err = d.resolveCustomUser(user, pass)
		}

	default:
		err = fmt.Errorf("unsupported auth mode: %v", d.cfg.AuthMode)
	}

	return
}

// resolveCustomUser looks up user/pass in the custom Users list.
func (d *Driver) resolveCustomUser(user, pass string) (userToken, jaCookie string, am config.AccessMode, err error) {
	am = d.cfg.AccessMode
	for _, u := range d.cfg.Users {
		if u.UserName == "" || u.UserName != user {
			continue
		}
		if u.Password != "" && u.Password != pass {
			continue
		}
		if u.UserToken != "" {
			userToken = u.UserToken
		} else if u.Cookie != "" {
			jaCookie = u.Cookie
		}
		am = u.AccessMode
		return
	}
	err = fmt.Errorf("invalid credentials")
	return
}

// resolveCred resolves a SpaceCred for the given userToken, using the cache.
func (d *Driver) resolveCred(userToken string) (*tbox.SpaceCred, error) {
	if cached := d.credCache.Get(userToken); cached != nil {
		return cached, nil
	}
	cred, err := d.client.GetSpace(userToken)
	if err != nil {
		return nil, err
	}
	d.credCache.Set(userToken, cred)
	return cred, nil
}
