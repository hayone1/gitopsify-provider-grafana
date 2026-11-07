package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hayone1/gitopsify/sdk"
)

// --- organizations ---------------------------------------------------------

type orgUser struct {
	Login string `json:"login"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func readOrganization(ctx context.Context, r *run, o org) error {
	if !r.s.admin {
		return sdk.Errorf(sdk.CodeForbidden, "organizations need a server admin's user:password")
	}
	if o.ID == 1 {
		return r.skip(o, orgKey(o.ID), kOrganization, o.Name, "the main organization always exists; it can't be created or deleted")
	}
	var users []orgUser
	if err := r.s.c.get(ctx, 0, fmt.Sprintf("/api/orgs/%d/users", o.ID), &users); err != nil {
		return err
	}
	roles := map[string][]any{}
	for _, u := range users {
		// Terraform identifies members by email (empty for users without one).
		roles[u.Role] = append(roles[u.Role], u.Email)
	}
	// Every member, the server admin included, with admin_user empty: after
	// an import Terraform reads the admin as a member, and an admin_user
	// would make it plan to remove them.
	attrs := map[string]any{"name": o.Name, "admin_user": "", "create_users": false}
	for role, attr := range map[string]string{"Admin": "admins", "Editor": "editors", "Viewer": "viewers", "None": "users_without_access"} {
		if list := roles[role]; len(list) > 0 {
			sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]) < fmt.Sprint(list[j]) })
			attrs[attr] = list
		}
	}
	return r.emit(sdk.Resource{Key: orgKey(o.ID), Kind: kOrganization, Partition: strconv.FormatInt(o.ID, 10), Name: o.Name,
		ImportID: strconv.FormatInt(o.ID, 10), Attributes: attrs,
		Notes: []string{"Member lists are authoritative: users added outside Terraform are removed on apply."}})
}

// --- folders ---------------------------------------------------------------

type folder struct {
	UID       string `json:"uid"`
	Title     string `json:"title"`
	FolderUID string `json:"folderUid"`
}

type searchHit struct {
	UID       string `json:"uid"`
	Title     string `json:"title"`
	FolderUID string `json:"folderUid"`
	Type      string `json:"type"`
}

func (r *run) loadFolders(ctx context.Context, o org) error {
	if _, ok := r.folders[o.ID]; ok {
		return nil
	}
	hits, err := paged[searchHit](ctx, r.s.c, o.ID, "/api/search?type=dash-folder", 5000)
	if err != nil {
		return err
	}
	m := map[string]folder{}
	for _, h := range hits {
		m[h.UID] = folder{UID: h.UID, Title: h.Title, FolderUID: h.FolderUID}
	}
	r.folders[o.ID] = m
	return nil
}

// folderPath is the folder's path of titles, for file names.
func (r *run) folderPath(o org, uid string) string {
	var parts []string
	seen := map[string]bool{}
	for uid != "" && !seen[uid] {
		seen[uid] = true
		f, ok := r.folders[o.ID][uid]
		if !ok {
			break
		}
		parts = append([]string{f.Title}, parts...)
		uid = f.FolderUID
	}
	if len(parts) == 0 {
		return "general"
	}
	return strings.Join(parts, "/")
}

func (r *run) folderRef(o org, uid string) any {
	return sdk.Ref(key("folder", o, uid), "uid", uid, "folderRef")
}

func readFolders(ctx context.Context, r *run, o org) error {
	uids := make([]string, 0, len(r.folders[o.ID]))
	for uid := range r.folders[o.ID] {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	for _, uid := range uids {
		f := r.folders[o.ID][uid]
		attrs := r.orgAttrs(o, map[string]any{"title": f.Title, "uid": f.UID})
		if f.FolderUID != "" {
			attrs["parent_folder_uid"] = r.folderRef(o, f.FolderUID)
		}
		if err := r.emit(sdk.Resource{Key: key("folder", o, f.UID), Kind: kFolder, Partition: strconv.FormatInt(o.ID, 10), Name: f.Title,
			ImportID: r.importID(o, f.UID), Attributes: attrs}); err != nil {
			return err
		}
	}
	return nil
}

// --- dashboards ------------------------------------------------------------

type dashboardFull struct {
	Dashboard json.RawMessage `json:"dashboard"`
	Meta      struct {
		Provisioned bool   `json:"provisioned"`
		FolderUID   string `json:"folderUid"`
	} `json:"meta"`
}

func readDashboards(ctx context.Context, r *run, o org) error {
	hits, err := paged[searchHit](ctx, r.s.c, o.ID, "/api/search?type=dash-db", 5000)
	if err != nil {
		return err
	}
	for _, h := range hits {
		k := key("dashboard", o, h.UID)
		if !r.req.Scope.HasKey(k) {
			continue
		}
		res := sdk.Resource{Key: k, Kind: kDashboard, Partition: strconv.FormatInt(o.ID, 10), Name: h.Title, ImportID: r.importID(o, h.UID)}
		if r.summary {
			if h.FolderUID != "" {
				res.DependsOn = []string{key("folder", o, h.FolderUID)}
			}
			if err := r.emit(res); err != nil {
				return err
			}
			continue
		}
		var d dashboardFull
		if err := r.s.c.get(ctx, o.ID, "/api/dashboards/uid/"+url.PathEscape(h.UID), &d); err != nil {
			if statusOf(err) == 404 {
				continue // deleted since the search
			}
			// The others can still be read; this scope is incomplete.
			if err := r.warn(o, kDashboard, fmt.Errorf("dashboard %q: %w", h.Title, err)); err != nil {
				return err
			}
			continue
		}
		if v := r.storedVersion(ctx, o, h.UID); strings.HasPrefix(v, "v2") {
			if err := r.skip(o, k, kDashboard, h.Title, "stored as a "+v+" dashboard (Grafana's new layouts); grafana_dashboard would convert it to the classic schema"); err != nil {
				return err
			}
			continue
		}
		if d.Meta.Provisioned {
			if err := r.skip(o, k, kDashboard, h.Title, "provisioned from files by Grafana; manage it where it's provisioned"); err != nil {
				return err
			}
			continue
		}
		model, err := cleanDashboard(d.Dashboard)
		if err != nil {
			if err := r.warn(o, kDashboard, fmt.Errorf("dashboard %q: %w", h.Title, err)); err != nil {
				return err
			}
			continue
		}
		attrs := r.orgAttrs(o, map[string]any{
			"config_json": sdk.File("dashboards/"+r.folderPath(o, d.Meta.FolderUID)+"/"+h.Title+".json", model),
		})
		if d.Meta.FolderUID != "" {
			attrs["folder"] = r.folderRef(o, d.Meta.FolderUID)
		}
		res.Attributes = attrs
		for _, uid := range libraryPanels(d.Dashboard) {
			res.DependsOn = append(res.DependsOn, key("librarypanel", o, uid))
		}
		if err := r.emit(res); err != nil {
			return err
		}
	}
	return nil
}

// storedVersion asks Grafana's resource API how a dashboard is stored
// ("v0alpha1" for classic ones, "v2beta1" for the new layouts); "" when it
// can't tell (older Grafana).
func (r *run) storedVersion(ctx context.Context, o org, uid string) string {
	ns := r.namespace(ctx, o)
	var res struct {
		Status struct {
			Conversion struct {
				StoredVersion string `json:"storedVersion"`
			} `json:"conversion"`
		} `json:"status"`
	}
	if err := r.s.c.get(ctx, o.ID, "/apis/dashboard.grafana.app/v1beta1/namespaces/"+ns+"/dashboards/"+url.PathEscape(uid), &res); err != nil {
		return ""
	}
	return res.Status.Conversion.StoredVersion
}

// namespace is the organization's namespace in Grafana's resource APIs
// (default, org-2, stacks-123 on Grafana Cloud).
func (r *run) namespace(ctx context.Context, o org) string {
	if ns, ok := r.namespaces[o.ID]; ok {
		return ns
	}
	var fs struct {
		Namespace string `json:"namespace"`
	}
	ns := "default"
	if o.ID != 1 {
		ns = fmt.Sprintf("org-%d", o.ID)
	}
	if err := r.s.c.get(ctx, o.ID, "/api/frontend/settings", &fs); err == nil && fs.Namespace != "" {
		ns = fs.Namespace
	}
	r.namespaces[o.ID] = ns
	return ns
}

// libraryPanels lists the library panels a dashboard uses.
func libraryPanels(raw json.RawMessage) []string {
	var d struct {
		Panels []struct {
			LibraryPanel *struct {
				UID string `json:"uid"`
			} `json:"libraryPanel"`
			Panels []json.RawMessage `json:"panels"`
		} `json:"panels"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range d.Panels {
		if p.LibraryPanel != nil && p.LibraryPanel.UID != "" && !seen[p.LibraryPanel.UID] {
			seen[p.LibraryPanel.UID] = true
			out = append(out, p.LibraryPanel.UID)
		}
		for _, sub := range p.Panels { // rows
			for _, uid := range libraryPanels(json.RawMessage(`{"panels":[` + string(sub) + `]}`)) {
				if !seen[uid] {
					seen[uid] = true
					out = append(out, uid)
				}
			}
		}
	}
	return out
}

// cleanDashboard drops the fields Grafana assigns (id, version), which
// Terraform ignores too, keeping the order of the rest.
func cleanDashboard(raw json.RawMessage) (sdk.RawJSON, error) {
	return dropKeys(raw, "id", "version")
}

// dropKeys removes top-level keys from a JSON object, keeping the order of
// the others.
func dropKeys(raw json.RawMessage, drop ...string) (sdk.RawJSON, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	skip := map[string]bool{}
	for _, k := range drop {
		skip[k] = true
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for d.More() {
		kt, err := d.Token()
		if err != nil {
			return nil, err
		}
		k := kt.(string)
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		if skip[k] {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return sdk.RawJSON(buf.Bytes()), nil
}

// --- library panels --------------------------------------------------------

type libraryElements struct {
	Result struct {
		TotalCount int `json:"totalCount"`
		Elements   []struct {
			UID       string          `json:"uid"`
			Name      string          `json:"name"`
			FolderUID string          `json:"folderUid"`
			Model     json.RawMessage `json:"model"`
		} `json:"elements"`
	} `json:"result"`
}

func readLibraryPanels(ctx context.Context, r *run, o org) error {
	for page := 1; page < 1000; page++ {
		var le libraryElements
		if err := r.s.c.get(ctx, o.ID, fmt.Sprintf("/api/library-elements?kind=1&perPage=100&page=%d", page), &le); err != nil {
			return err
		}
		for _, e := range le.Result.Elements {
			// The model as Grafana returns it: Terraform's read keeps every
			// key, so dropping any would show as a change.
			attrs := r.orgAttrs(o, map[string]any{"name": e.Name, "uid": e.UID, "model_json": sdk.JSON(sdk.RawJSON(e.Model))})
			if e.FolderUID != "" {
				attrs["folder_uid"] = r.folderRef(o, e.FolderUID)
			}
			if err := r.emit(sdk.Resource{Key: key("librarypanel", o, e.UID), Kind: kLibraryPanel, Partition: strconv.FormatInt(o.ID, 10),
				Name: e.Name, ImportID: r.importID(o, e.UID), Attributes: attrs}); err != nil {
				return err
			}
		}
		if len(le.Result.Elements) < 100 {
			break
		}
	}
	return nil
}

// --- data sources ----------------------------------------------------------

type dataSource struct {
	UID              string          `json:"uid"`
	Name             string          `json:"name"`
	Type             string          `json:"type"`
	URL              string          `json:"url"`
	Access           string          `json:"access"`
	Database         string          `json:"database"`
	User             string          `json:"user"`
	BasicAuth        bool            `json:"basicAuth"`
	BasicAuthUser    string          `json:"basicAuthUser"`
	IsDefault        bool            `json:"isDefault"`
	ReadOnly         bool            `json:"readOnly"`
	JSONData         json.RawMessage `json:"jsonData"`
	SecureJSONFields map[string]bool `json:"secureJsonFields"`
}

var httpHeaderName = regexp.MustCompile(`^httpHeaderName(\d+)$`)

func readDataSources(ctx context.Context, r *run, o org) error {
	var list []dataSource
	if err := r.s.c.get(ctx, o.ID, "/api/datasources", &list); err != nil {
		return err
	}
	for _, item := range list {
		if !r.req.Scope.HasKey(key("datasource", o, item.UID)) {
			continue
		}
		// The list leaves out basicAuthUser and secureJsonFields.
		var ds dataSource
		if err := r.s.c.get(ctx, o.ID, "/api/datasources/uid/"+url.PathEscape(item.UID), &ds); err != nil {
			if statusOf(err) == 404 {
				continue
			}
			if err := r.warn(o, kDataSource, fmt.Errorf("data source %q: %w", item.Name, err)); err != nil {
				return err
			}
			continue
		}
		if ds.ReadOnly {
			if err := r.skip(o, key("datasource", o, ds.UID), kDataSource, ds.Name, "provisioned from files by Grafana (read-only)"); err != nil {
				return err
			}
			continue
		}
		attrs := r.orgAttrs(o, map[string]any{"name": ds.Name, "type": ds.Type, "uid": ds.UID})
		var notes []string
		set := func(k string, v any, keep bool) {
			if keep {
				attrs[k] = v
			}
		}
		set("url", ds.URL, ds.URL != "")
		set("access_mode", ds.Access, ds.Access != "" && ds.Access != "proxy")
		set("database_name", ds.Database, ds.Database != "")
		set("username", ds.User, ds.User != "")
		set("basic_auth_enabled", true, ds.BasicAuth)
		set("basic_auth_username", ds.BasicAuthUser, ds.BasicAuthUser != "")
		set("is_default", true, ds.IsDefault)
		if len(ds.JSONData) > 0 && string(ds.JSONData) != "null" && string(ds.JSONData) != "{}" {
			jd, headers, err := splitHeaders(ds.JSONData)
			if err != nil {
				return fmt.Errorf("data source %q: %w", ds.Name, err)
			}
			// Terraform refuses teamHttpHeaders here (they're managed with
			// grafana_data_source_config_lbac_rules) and drops them on read.
			if strings.Contains(string(jd), `"teamHttpHeaders"`) {
				if jd, err = dropKeys(json.RawMessage(jd), "teamHttpHeaders"); err != nil {
					return fmt.Errorf("data source %q: %w", ds.Name, err)
				}
				notes = append(notes, "Its team HTTP headers (LBAC rules) are left out; manage them with grafana_data_source_config_lbac_rules.")
			}
			if len(jd) > 2 {
				attrs["json_data_encoded"] = sdk.JSON(jd)
			}
			if len(headers) > 0 {
				attrs["http_headers"] = sdk.Secret("http_headers", nil, "httpHeadersSecretRef", "HTTP headers ("+strings.Join(headers, ", ")+")")
				notes = append(notes, "Its custom HTTP headers ("+strings.Join(headers, ", ")+") have secret values Grafana doesn't return: set http_headers before changing this data source with Terraform or Crossplane, or the headers are removed.")
			}
		}
		var secure []string
		for k, on := range ds.SecureJSONFields {
			if on && !strings.HasPrefix(k, "httpHeaderValue") {
				secure = append(secure, k)
			}
		}
		sort.Strings(secure)
		if len(secure) > 0 {
			attrs["secure_json_data_encoded"] = sdk.Secret("secure_json_data", nil, "secureJsonDataEncodedSecretRef", strings.Join(secure, ", "))
			notes = append(notes, "Grafana never returns secret fields ("+strings.Join(secure, ", ")+"); set secure_json_data_encoded to manage them.")
		}
		if err := r.emit(sdk.Resource{Key: key("datasource", o, ds.UID), Kind: kDataSource, Partition: strconv.FormatInt(o.ID, 10),
			Name: ds.Name, ImportID: r.importID(o, ds.UID), Attributes: attrs, Notes: notes}); err != nil {
			return err
		}
	}
	return nil
}

// splitHeaders takes the httpHeaderNameN keys out of jsonData (Terraform
// manages them through http_headers) and returns the header names.
func splitHeaders(raw json.RawMessage) (sdk.RawJSON, []string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, err
	}
	var drop, names []string
	for k, v := range m {
		if httpHeaderName.MatchString(k) {
			drop = append(drop, k)
			var s string
			_ = json.Unmarshal(v, &s)
			names = append(names, s)
		}
	}
	sort.Strings(names)
	out, err := dropKeys(raw, drop...)
	return out, names, err
}

// --- teams -----------------------------------------------------------------

func readTeams(ctx context.Context, r *run, o org) error {
	for page := 1; page < 1000; page++ {
		var res struct {
			Teams []struct {
				ID    int64  `json:"id"`
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"teams"`
		}
		if err := r.s.c.get(ctx, o.ID, fmt.Sprintf("/api/teams/search?perpage=500&page=%d", page), &res); err != nil {
			return err
		}
		for _, t := range res.Teams {
			attrs := r.orgAttrs(o, map[string]any{"name": t.Name})
			if t.Email != "" {
				attrs["email"] = t.Email
			}
			id := strconv.FormatInt(t.ID, 10)
			if !r.summary {
				var prefs struct {
					Theme            string `json:"theme"`
					HomeDashboardUID string `json:"homeDashboardUID"`
					Timezone         string `json:"timezone"`
					WeekStart        string `json:"weekStart"`
				}
				if err := r.s.c.get(ctx, o.ID, "/api/teams/"+id+"/preferences", &prefs); err != nil {
					if err := r.warn(o, kTeam, fmt.Errorf("team %q preferences: %w", t.Name, err)); err != nil {
						return err
					}
					continue
				}
				if bad := unsupportedPrefs(prefs.Theme, prefs.Timezone, prefs.WeekStart); bad != "" {
					// Dropping the block would reset them on the next update.
					if err := r.skip(o, key("team", o, id), kTeam, t.Name, "its preferences use "+bad+", which the Terraform provider refuses"); err != nil {
						return err
					}
					continue
				}
				p := map[string]any{}
				for k, v := range map[string]string{"theme": prefs.Theme, "timezone": prefs.Timezone, "week_start": prefs.WeekStart} {
					if v != "" {
						p[k] = v
					}
				}
				if prefs.HomeDashboardUID != "" {
					p["home_dashboard_uid"] = sdk.Ref(key("dashboard", o, prefs.HomeDashboardUID), "uid", prefs.HomeDashboardUID, "")
				}
				// Terraform reads the block whenever a preference is set; left
				// out, an update would reset them.
				if len(p) > 0 {
					attrs["preferences"] = []any{p}
				}
			}
			if err := r.emit(sdk.Resource{Key: key("team", o, id), Kind: kTeam, Partition: strconv.FormatInt(o.ID, 10), Name: t.Name,
				ImportID: r.importID(o, id), Attributes: attrs}); err != nil {
				return err
			}
		}
		if len(res.Teams) < 500 {
			break
		}
	}
	return nil
}

// unsupportedPrefs names a team preference value the Terraform provider's
// validators refuse, or returns "".
func unsupportedPrefs(theme, timezone, weekStart string) string {
	allowed := func(v string, ok ...string) bool {
		for _, o := range ok {
			if v == o {
				return true
			}
		}
		return false
	}
	switch {
	case !allowed(theme, "", "light", "dark", "system"):
		return "theme " + strconv.Quote(theme)
	case !allowed(timezone, "", "utc", "browser"):
		return "timezone " + strconv.Quote(timezone)
	case !allowed(weekStart, "", "sunday", "monday", "saturday"):
		return "week start " + strconv.Quote(weekStart)
	}
	return ""
}

// --- playlists -------------------------------------------------------------

func readPlaylists(ctx context.Context, r *run, o org) error {
	var list []struct {
		UID  string `json:"uid"`
		Name string `json:"name"`
	}
	if err := r.s.c.get(ctx, o.ID, "/api/playlists", &list); err != nil {
		return err
	}
	for _, p := range list {
		var full struct {
			Name     string `json:"name"`
			Interval string `json:"interval"`
			Items    []struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"items"`
		}
		if err := r.s.c.get(ctx, o.ID, "/api/playlists/"+url.PathEscape(p.UID), &full); err != nil {
			if statusOf(err) == 404 {
				continue
			}
			if err := r.warn(o, kPlaylist, fmt.Errorf("playlist %q: %w", p.Name, err)); err != nil {
				return err
			}
			continue
		}
		var items []any
		for i, it := range full.Items {
			items = append(items, map[string]any{"order": json.Number(strconv.Itoa(i + 1)), "type": it.Type, "value": it.Value})
		}
		attrs := r.orgAttrs(o, map[string]any{"name": full.Name, "interval": full.Interval})
		if len(items) > 0 {
			attrs["item"] = items
		}
		if err := r.emit(sdk.Resource{Key: key("playlist", o, p.UID), Kind: kPlaylist, Partition: strconv.FormatInt(o.ID, 10), Name: p.Name,
			ImportID: r.importID(o, p.UID), Attributes: attrs}); err != nil {
			return err
		}
	}
	return nil
}
