package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"

	"github.com/styropyr0/visionary/internal/authbridge"
)

var ssoAddedColumns = map[string][]sdk.Column{
	"visionary_users": {
		{Name: "source", Kind: "text"},
		{Name: "external_id", Kind: "text"},
		{Name: "email", Kind: "text"},
	},
}

type ssoIdentity struct {
	Subject       string
	Email         string
	EmailVerified *bool
	Name          string
	// Groups is nil when the provider sent no groups claim at all
	Groups []string
	Claims map[string]any
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func isSSOSession(sess *session) bool { return strings.HasPrefix(sess.UserID, ssoPrefix) }

type ssoDecision struct {
	username string
	role     role
	existing *account
	roleFrom string
}

// A verified email in an allowed domain, a role from the groups or the default,
// and an account that exists or may be created
func (s *Server) ssoDecide(ctx context.Context, p *ssoProvider, id *ssoIdentity) (*ssoDecision, error) {
	email := strings.ToLower(strings.TrimSpace(id.Email))
	if email == "" {
		if p.Kind == ssoOIDC {
			return nil, fmt.Errorf("%s didn't send an email address; add the email scope and make sure the account has one", p.Name)
		}
		return nil, fmt.Errorf("%s didn't send an email address; send it as the NameID or name its attribute in Settings", p.Name)
	}
	if id.EmailVerified != nil && !*id.EmailVerified {
		return nil, fmt.Errorf("%s says %s isn't verified", p.Name, email)
	}
	_, domain, _ := strings.Cut(email, "@")
	if len(p.AllowedDomains) > 0 && !slices.Contains(p.AllowedDomains, domain) {
		return nil, fmt.Errorf("%s accounts can't sign in here; only %s", "@"+domain, strings.Join(p.AllowedDomains, ", "))
	}
	username, err := normalizeUsername(email)
	if err != nil {
		return nil, fmt.Errorf("%s can't be a Visionary username: %v", email, err)
	}
	existing, err := s.findAccount(ctx, "username", username)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Source == "" {
		return nil, fmt.Errorf("%s is a Visionary account with a password; sign in with that, or ask an admin to delete it so single sign-on can take it over", username)
	}
	d := &ssoDecision{username: username, existing: existing}
	mapping, _ := newRoleMapping(p.RoleMap, nil)
	switch {
	case existing != nil && existing.Source == scimSource && s.scimManagesRoles():
		d.role, d.roleFrom = parseRole(existing.Role), "SCIM"
	case p.GroupsClaim != "" && id.Groups != nil:
		d.role, d.roleFrom = mapping.resolve(id.Groups), "groups"
	default:
		d.role, d.roleFrom = mapping.resolve(nil), "mapping"
	}
	if def := parseRole(p.DefaultRole); def > d.role && d.roleFrom != "SCIM" {
		d.role, d.roleFrom = def, "default role"
	}
	switch {
	case d.role == roleNone:
		return d, errors.New("your account isn't in a group that has access to Visionary")
	case existing == nil && !p.CreateUsers:
		return d, errors.New("you don't have a Visionary account yet; ask an admin to add you through your identity provider")
	case existing != nil && existing.Disabled:
		return d, errors.New("your Visionary account is disabled")
	}
	return d, nil
}

func (s *Server) ssoComplete(w http.ResponseWriter, r *http.Request, p *ssoProvider, id *ssoIdentity, test bool) {
	d, err := s.ssoDecide(r.Context(), p, id)
	if test {
		s.ssoTestPage(w, p, id, d, err)
		return
	}
	if err != nil {
		target := ""
		if d != nil {
			target = d.username
		}
		s.logger.Info("single sign-on refused", "provider", p.ID, "reason", err.Error())
		s.auditAs(r, nil, "auth.sign_in_refused", target, map[string]any{"mode": p.Kind, "provider": p.ID, "reason": clip(err.Error(), 200), "groups": id.Groups})
		ssoFail(w, r, err.Error())
		return
	}
	if sg := s.signin(); sg.open != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	} else if sg.broken != nil {
		ssoFail(w, r, "Sign-in isn't working: "+sg.broken.Error())
		return
	}
	a, err := s.ssoUpsert(r, p, id, d)
	if err != nil {
		s.logger.Error("single sign-on: saving the account failed", "err", err)
		ssoFail(w, r, "Signing you in failed; try again.")
		return
	}
	if p.GroupsClaim != "" && id.Groups != nil {
		s.rememberGroups(r.Context(), a.ID, id.Groups)
	}
	sess := s.auth.newSession(&authbridge.Result{
		Token: ssoPrefix + p.ID + ":" + randomToken(),
		User:  authbridge.User{ID: ssoPrefix + a.ID, Name: a.Name, Handle: a.Username, Roles: []string{a.Role}},
	})
	sess.Role = a.Role
	if err := s.auth.write(w, r, sess); err != nil {
		s.fail(w, err)
		return
	}
	s.logger.Info("sign-in", "user_id", sess.UserID, "provider", p.ID)
	s.auditAs(r, sess, "auth.sign_in", sess.UserID, map[string]any{"mode": p.Kind, "provider": p.ID, "role": a.Role, "role_from": d.roleFrom})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) ssoUpsert(r *http.Request, p *ssoProvider, id *ssoIdentity, d *ssoDecision) (*account, error) {
	ctx := r.Context()
	now := nowTS()
	name := clip(fallback(strings.TrimSpace(id.Name), d.username), maxAccountNameLen)
	email := strings.ToLower(strings.TrimSpace(id.Email))
	if a := d.existing; a != nil {
		a.Name, a.Role, a.Email = name, d.role.String(), email
		err := s.db.Exec(ctx, `UPDATE visionary_users SET name = ?, role = ?, email = ?, last_sign_in_at = ? WHERE id = ?`, a.Name, a.Role, email, now, a.ID)
		return a, dbErr(err)
	}
	a := &account{ID: newAccountID(), Username: d.username, Name: name, Role: d.role.String(), Source: p.Kind, Email: email, CreatedBy: p.Name, CreatedAt: now, PasswordAt: now}
	err := s.db.Exec(ctx, `INSERT INTO visionary_users (id, username, name, role, password_hash, password_at, disabled, created_by, created_at, last_sign_in_at, source, email)
VALUES (?, ?, ?, ?, '', ?, 0, ?, ?, ?, ?, ?)`, a.ID, a.Username, a.Name, a.Role, now, a.CreatedBy, now, now, a.Source, email)
	if err != nil {
		// Two first sign-ins at once: the other one made it
		if again, ferr := s.findAccount(ctx, "username", d.username); ferr == nil && again != nil && again.Source != "" {
			return again, nil
		}
		return nil, dbErr(err)
	}
	s.auditAs(r, &session{UserID: ssoPrefix + a.ID, Name: a.Name, Role: a.Role}, "team.account_created", a.Username,
		map[string]any{"role": a.Role, "source": a.Source, "provider": p.ID, "just_in_time": true})
	return a, nil
}

func (s *Server) touchUser(id string) {
	st := s.sso()
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	for k, at := range st.touched {
		if now.Sub(at) > accountRecheck {
			delete(st.touched, k)
		}
	}
	st.touched[id] = now
}

func (s *Server) touchedSince(id string, t time.Time) bool {
	st := s.sso()
	st.mu.Lock()
	defer st.mu.Unlock()
	at, ok := st.touched[id]
	return ok && !at.Before(t)
}

// Re-read once a minute, or at once when this server just changed it
func (s *Server) confirmSSO(ctx context.Context, sess *session) (*session, bool, error) {
	id := strings.TrimPrefix(sess.UserID, ssoPrefix)
	rest, _ := strings.CutPrefix(sess.Token, ssoPrefix)
	provider, _, _ := strings.Cut(rest, ":")
	if s.ssoSetup().provider(provider) == nil {
		return nil, false, errSessionEnded
	}
	if time.Since(sess.checkedAt()) < accountRecheck && !s.touchedSince(id, sess.checkedAt()) {
		return sess, false, nil
	}
	a, err := s.findAccount(ctx, "id", id)
	if err != nil {
		return sess, false, nil
	}
	if a == nil || a.Disabled || parseRole(a.Role) == roleNone {
		return nil, false, errNotAllowed
	}
	if changed, err := time.Parse(tsLayout, a.PasswordAt); err == nil && changed.Unix() > sess.Issued {
		return nil, false, errSessionEnded
	}
	next := *sess
	next.Role, next.Name, next.Handle, next.Roles, next.Checked = a.Role, a.Name, a.Username, []string{a.Role}, time.Now().Unix()
	return &next, true, nil
}

func (s *Server) ssoError(w http.ResponseWriter, r *http.Request, test bool, msg string) {
	if test {
		s.ssoTestPage(w, nil, nil, nil, errors.New(msg))
		return
	}
	ssoFail(w, r, msg)
}

var ssoTestTemplate = template.Must(template.New("t").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Test sign-in</title>
<style>
:root { color-scheme: light dark; --bg: #f7f6f3; --card: #fff; --text: #1c1b19; --dim: #6b6963; --line: #e4e2dc; --ok: #2f7d4f; --bad: #b3261e; }
@media (prefers-color-scheme: dark) { :root { --bg: #0f1012; --card: #17181b; --text: #f0efec; --dim: #9a978f; --line: #2a2b2f; --ok: #6cc28f; --bad: #f2867d; } }
body { margin: 0; background: var(--bg); color: var(--text); font: 14px/1.5 system-ui, sans-serif; }
main { max-width: 760px; margin: 40px auto; padding: 0 16px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 10px; padding: 20px 22px; margin-bottom: 16px; }
h1 { font-size: 20px; margin: 0 0 4px; } h2 { font-size: 15px; margin: 0 0 10px; }
.ok { color: var(--ok); font-weight: 600; } .bad { color: var(--bad); font-weight: 600; }
table { border-collapse: collapse; width: 100%; } td { padding: 6px 8px; border-top: 1px solid var(--line); vertical-align: top; word-break: break-word; }
td:first-child { color: var(--dim); width: 34%; } code { font: 12.5px ui-monospace, monospace; }
p.dim { color: var(--dim); margin: 0; }
</style></head><body><main>
<div class="card"><h1>Test sign-in{{if .Provider}}: {{.Provider}}{{end}}</h1>
{{if .Error}}<p class="bad">{{.Error}}</p>{{else}}<p class="ok">This person would be signed in as {{.Role}}.</p>{{end}}
<p class="dim">Nobody was signed in and nothing was saved. Close this window to go back to Settings.</p></div>
{{if .Rows}}<div class="card"><h2>What would happen</h2><table>{{range .Rows}}<tr><td>{{index . 0}}</td><td>{{index . 1}}</td></tr>{{end}}</table></div>{{end}}
{{if .Claims}}<div class="card"><h2>What the provider sent</h2><table>{{range .Claims}}<tr><td><code>{{index . 0}}</code></td><td><code>{{index . 1}}</code></td></tr>{{end}}</table></div>{{end}}
</main></body></html>`))

func (s *Server) ssoTestPage(w http.ResponseWriter, p *ssoProvider, id *ssoIdentity, d *ssoDecision, err error) {
	data := struct {
		Provider, Error, Role string
		Rows, Claims          [][2]string
	}{}
	if err != nil {
		data.Error = sentence(err.Error())
	}
	if p != nil {
		data.Provider = p.Name
	}
	if id != nil {
		groups := "not sent"
		if id.Groups != nil {
			groups = fallback(strings.Join(id.Groups, ", "), "none")
		}
		data.Rows = append(data.Rows, [2]string{"Email", fallback(id.Email, "not sent")}, [2]string{"Name", fallback(id.Name, "not sent")}, [2]string{"Groups", groups})
		for k, v := range id.Claims {
			text, ok := v.(string)
			if !ok {
				b, _ := json.Marshal(v)
				text = string(b)
			}
			data.Claims = append(data.Claims, [2]string{k, clip(text, 500)})
		}
		sort.Slice(data.Claims, func(i, j int) bool { return data.Claims[i][0] < data.Claims[j][0] })
	}
	if d != nil {
		data.Role = d.role.String()
		account := "would be created"
		if d.existing != nil {
			account = "exists (" + fallback(d.existing.Source, "password") + ")"
		}
		data.Rows = append(data.Rows, [2]string{"Username", d.username}, [2]string{"Account", account},
			[2]string{"Role", fallback(d.role.String(), "no access") + " (from " + d.roleFrom + ")"})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = ssoTestTemplate.Execute(w, data)
}
