// Package grafana is the gitopsify provider for Grafana: it reads a Grafana
// through its HTTP API and describes what it finds as resources of the
// grafana/grafana Terraform provider (and, through gitopsify, of Crossplane's
// provider-grafana).
package grafana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hayone1/gitopsify/sdk"
)

// Provider reads Grafana.
type Provider struct {
	version string
}

// New returns the provider.
func New(version string) *Provider { return &Provider{version: version} }

// Describe implements sdk.Provider.
func (p *Provider) Describe() sdk.Description { return description(p.version) }

type health struct {
	Version string `json:"version"`
}

type user struct {
	Login          string `json:"login"`
	Email          string `json:"email"`
	IsGrafanaAdmin bool   `json:"isGrafanaAdmin"`
}

type org struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// session is what check and discover learn about the connection.
type session struct {
	c          *client
	version    string
	who        string
	admin      bool
	adminLogin string
	orgs       []org
	// member holds the organizations a server admin belongs to (nil: not
	// checked); Grafana only lets members read an organization.
	member map[int64]bool
}

func (s *session) canRead(o org) bool { return s.member == nil || s.member[o.ID] }

func (p *Provider) connect(ctx context.Context, req sdk.Request) (*session, error) {
	c, err := newClient(req.Settings)
	if err != nil {
		return nil, err
	}
	s := &session{c: c}
	var h health
	if err := c.get(ctx, 0, "/api/health", &h); err != nil {
		return nil, err
	}
	s.version = h.Version
	if c.basic() {
		var u user
		if err := c.get(ctx, 0, "/api/user", &u); err != nil {
			return nil, err
		}
		s.who, s.admin, s.adminLogin = u.Login, u.IsGrafanaAdmin, u.Login
		if u.IsGrafanaAdmin {
			s.who += " (server admin)"
			// /api/orgs counts pages from 0 (/api/search from 1).
			for page := 0; page < 1000; page++ {
				var batch []org
				if err := c.get(ctx, 0, fmt.Sprintf("/api/orgs?perpage=1000&page=%d", page), &batch); err != nil {
					return nil, err
				}
				s.orgs = append(s.orgs, batch...)
				if len(batch) < 1000 {
					break
				}
			}
			var mine []struct {
				OrgID int64 `json:"orgId"`
			}
			if err := c.get(ctx, 0, "/api/user/orgs", &mine); err != nil {
				return nil, err
			}
			s.member = map[int64]bool{}
			for _, m := range mine {
				s.member[m.OrgID] = true
			}
		} else {
			var mine []struct {
				OrgID int64  `json:"orgId"`
				Name  string `json:"name"`
			}
			if err := c.get(ctx, 0, "/api/user/orgs", &mine); err != nil {
				return nil, err
			}
			for _, m := range mine {
				s.orgs = append(s.orgs, org{ID: m.OrgID, Name: m.Name})
			}
		}
	} else {
		var o org
		if err := c.get(ctx, 0, "/api/org", &o); err != nil {
			return nil, err
		}
		s.orgs = []org{o}
		s.who = "a token for organization " + strconv.Quote(o.Name)
	}
	return s, nil
}

func (s *session) partition(o org) sdk.Partition {
	return sdk.Partition{ID: strconv.FormatInt(o.ID, 10), Name: o.Name}
}

// Check implements sdk.Provider.
func (p *Provider) Check(ctx context.Context, req sdk.Request) (*sdk.CheckResult, error) {
	s, err := p.connect(ctx, req)
	if err != nil {
		return nil, err
	}
	res := &sdk.CheckResult{Target: fmt.Sprintf("Grafana %s at %s", s.version, s.c.base.String()), Identity: s.who}
	for _, o := range s.orgs {
		res.Partitions = append(res.Partitions, s.partition(o))
		if !s.canRead(o) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s isn't a member of organization %q, so it can't be read; add %s to it (as Admin)", s.adminLogin, o.Name, s.adminLogin))
		}
	}
	if !s.c.basic() {
		res.Warnings = append(res.Warnings, "a token sees one organization; use a server admin's user:password to export them all")
	}
	if len(s.orgs) == 0 {
		return res, nil
	}
	first := s.orgs[0].ID
	probe := func(kind, path string) {
		kc := sdk.KindCheck{Kind: kind, OK: true}
		if err := s.c.get(ctx, first, path, nil); err != nil {
			kc.OK, kc.Message = false, sdk.AsError(err).Error()
		}
		res.Kinds = append(res.Kinds, kc)
	}
	if s.admin {
		res.Kinds = append(res.Kinds, sdk.KindCheck{Kind: kOrganization, OK: true})
	} else {
		res.Kinds = append(res.Kinds, sdk.KindCheck{Kind: kOrganization, Message: "needs a server admin's user:password"})
	}
	probe(kFolder, "/api/search?type=dash-folder&limit=1")
	probe(kDataSource, "/api/datasources")
	probe(kTeam, "/api/teams/search?perpage=1")
	probe(kContactPoint, "/api/v1/provisioning/contact-points")
	probe(kRuleGroup, "/api/v1/provisioning/alert-rules")
	return res, nil
}

// reader reads one kind in one organization.
type reader func(ctx context.Context, r *run, o org) error

var readers = []struct {
	kind string
	read reader
}{
	{kOrganization, readOrganization},
	{kFolder, readFolders},
	{kDashboard, readDashboards},
	{kLibraryPanel, readLibraryPanels},
	{kDataSource, readDataSources},
	{kTeam, readTeams},
	{kPlaylist, readPlaylists},
	{kMessageTemplate, readMessageTemplates},
	{kMuteTiming, readMuteTimings},
	{kContactPoint, readContactPoints},
	{kNotificationPolicy, readNotificationPolicy},
	{kRuleGroup, readRuleGroups},
}

// run is one discover call.
type run struct {
	s       *session
	req     sdk.Request
	out     sdk.Emitter
	summary bool
	// folders by organization and UID, for file paths.
	folders map[int64]map[string]folder
	// namespaces caches each organization's resource API namespace.
	namespaces map[int64]string
}

// Discover implements sdk.Provider.
func (p *Provider) Discover(ctx context.Context, req sdk.Request, out sdk.Emitter) error {
	s, err := p.connect(ctx, req)
	if err != nil {
		return err
	}
	r := &run{s: s, req: req, out: out, summary: req.Detail == sdk.DetailSummary, folders: map[int64]map[string]folder{}, namespaces: map[int64]string{}}
	log := sdk.Logger(ctx)
	for _, o := range s.orgs {
		part := s.partition(o)
		if !req.Scope.HasPartition(part.ID) {
			continue
		}
		if err := out.Partition(part); err != nil {
			return err
		}
		if !s.canRead(o) {
			if err := r.warn(o, "", fmt.Errorf("%s isn't a member of this organization; add %s to it (as Admin) to export it", s.adminLogin, s.adminLogin)); err != nil {
				return err
			}
			continue
		}
		// Folders are needed for dashboard file paths even when they aren't
		// exported themselves.
		if err := r.loadFolders(ctx, o); err != nil {
			if err := r.warn(o, "", err); err != nil {
				return err
			}
			continue
		}
		log.Info("reading organization " + strconv.Quote(o.Name))
		for _, rd := range readers {
			if !req.Scope.HasKind(rd.kind) {
				continue
			}
			log.Debug("reading", "kind", rd.kind, "organization", o.Name)
			if err := rd.read(ctx, r, o); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := r.warn(o, rd.kind, err); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *run) warn(o org, kind string, err error) error {
	var se *sdk.Error
	msg := err.Error()
	if errors.As(err, &se) {
		msg = se.Message
		if statusOf(err) == http.StatusForbidden || statusOf(err) == http.StatusUnauthorized {
			msg += " (" + se.Hint + ")"
		}
	}
	return r.out.Warn(sdk.Warning{Partition: strconv.FormatInt(o.ID, 10), Kind: kind, Message: msg})
}

// emit sends a resource unless the scope leaves it out. In summary mode the
// attributes are dropped but dependencies kept.
func (r *run) emit(res sdk.Resource) error {
	if !r.req.Scope.HasKey(res.Key) {
		return nil
	}
	if r.summary {
		res.DependsOn = append(res.DependsOn, refKeys(res.Attributes)...)
		res.Attributes = nil
	}
	return r.out.Resource(res)
}

// skip reports an object left out on purpose, unless the scope leaves it
// out anyway.
func (r *run) skip(o org, k, kind, name, reason string) error {
	if !r.req.Scope.HasKey(k) {
		return nil
	}
	return r.out.Skip(sdk.Skipped{Kind: kind, Partition: strconv.FormatInt(o.ID, 10), Name: name, Reason: reason})
}

func refKeys(v any) []string {
	var out []string
	switch t := v.(type) {
	case sdk.RefValue:
		out = append(out, t.Ref)
	case map[string]any:
		for _, e := range t {
			out = append(out, refKeys(e)...)
		}
	case []any:
		for _, e := range t {
			out = append(out, refKeys(e)...)
		}
	}
	return out
}

// orgAttrs adds org_id (a reference to the organization) when the session can
// address organizations; tokens are scoped to one and refuse org_id.
func (r *run) orgAttrs(o org, attrs map[string]any) map[string]any {
	if r.s.c.basic() {
		id := strconv.FormatInt(o.ID, 10)
		attrs["org_id"] = sdk.Ref(orgKey(o.ID), "id", id, "organizationRef")
	}
	return attrs
}

// importID is "<org>:<id>", the form the Terraform provider writes itself;
// without the org, an ID that starts with digits and a colon (a mute timing
// named 09:00) would be read as one.
func (r *run) importID(o org, parts ...string) string {
	return strconv.FormatInt(o.ID, 10) + ":" + strings.Join(parts, ":")
}

func orgKey(id int64) string { return "organization/" + strconv.FormatInt(id, 10) }

func key(kind string, o org, id string) string {
	return kind + "/" + strconv.FormatInt(o.ID, 10) + "/" + id
}
