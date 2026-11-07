package grafana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/hayone1/gitopsify/sdk"
)

// provenance tells who manages an alerting object: "" (the UI or the API
// without provenance), "api" (the provisioning API, Terraform included),
// "file" (provisioning files) or others (converted Prometheus rules, ...).
// Only the first two can be managed as code.
func fromFiles(provenance string) bool { return provenance != "" && provenance != "api" }

func provenanceReason(p string) string {
	if p == "file" {
		return "provisioned from files"
	}
	return "managed by another system (provenance " + strconvQuote(p) + ")"
}

// disableProvenance is the Terraform setting that matches an object's current
// provenance: objects made in the UI stay editable there.
func disableProvenance(attrs map[string]any, provenance string) {
	attrs["disable_provenance"] = provenance == ""
}

// --- message templates -----------------------------------------------------

func readMessageTemplates(ctx context.Context, r *run, o org) error {
	var list []struct {
		Name       string `json:"name"`
		Template   string `json:"template"`
		Provenance string `json:"provenance"`
	}
	if err := r.s.c.get(ctx, o.ID, "/api/v1/provisioning/templates", &list); err != nil {
		return err
	}
	for _, t := range list {
		if fromFiles(t.Provenance) {
			if err := r.skip(o, key("messagetemplate", o, t.Name), kMessageTemplate, t.Name, provenanceReason(t.Provenance)); err != nil {
				return err
			}
			continue
		}
		attrs := r.orgAttrs(o, map[string]any{"name": t.Name, "template": t.Template})
		disableProvenance(attrs, t.Provenance)
		if err := r.emit(sdk.Resource{Key: key("messagetemplate", o, t.Name), Kind: kMessageTemplate, Partition: strconv.FormatInt(o.ID, 10),
			Name: t.Name, ImportID: r.importID(o, t.Name), Attributes: attrs}); err != nil {
			return err
		}
	}
	return nil
}

// --- mute timings ----------------------------------------------------------

type timeInterval struct {
	Times []struct {
		Start string `json:"start_time"`
		End   string `json:"end_time"`
	} `json:"times"`
	Weekdays    []string `json:"weekdays"`
	DaysOfMonth []string `json:"days_of_month"`
	Months      []string `json:"months"`
	Years       []string `json:"years"`
	Location    string   `json:"location"`
}

func readMuteTimings(ctx context.Context, r *run, o org) error {
	var list []struct {
		Name          string         `json:"name"`
		TimeIntervals []timeInterval `json:"time_intervals"`
		Provenance    string         `json:"provenance"`
	}
	if err := r.s.c.get(ctx, o.ID, "/api/v1/provisioning/mute-timings", &list); err != nil {
		return err
	}
	for _, m := range list {
		if fromFiles(m.Provenance) {
			if err := r.skip(o, key("mutetiming", o, m.Name), kMuteTiming, m.Name, provenanceReason(m.Provenance)); err != nil {
				return err
			}
			continue
		}
		var intervals []any
		for _, ti := range m.TimeIntervals {
			iv := map[string]any{}
			var times []any
			for _, t := range ti.Times {
				times = append(times, map[string]any{"start": t.Start, "end": t.End})
			}
			if len(times) > 0 {
				iv["times"] = times
			}
			for k, v := range map[string][]string{"weekdays": ti.Weekdays, "days_of_month": ti.DaysOfMonth, "months": ti.Months, "years": ti.Years} {
				if len(v) > 0 {
					iv[k] = strings2any(v)
				}
			}
			if ti.Location != "" {
				iv["location"] = ti.Location
			}
			intervals = append(intervals, iv)
		}
		attrs := r.orgAttrs(o, map[string]any{"name": m.Name})
		if len(intervals) > 0 {
			attrs["intervals"] = intervals
		}
		// Changing disable_provenance replaces a mute timing (which first
		// strips it from every policy and rule): keep false, and Terraform
		// ignores it (it can't read it back).
		attrs["disable_provenance"] = false
		var notes []string
		if m.Provenance == "" {
			notes = append(notes, "Made in the UI: the first change Terraform or Crossplane makes to it locks it in the UI (provenance becomes api).")
		}
		if err := r.emit(sdk.Resource{Key: key("mutetiming", o, m.Name), Kind: kMuteTiming, Partition: strconv.FormatInt(o.ID, 10),
			Name: m.Name, ImportID: r.importID(o, m.Name), Attributes: attrs, Notes: notes}); err != nil {
			return err
		}
	}
	return nil
}

// --- contact points --------------------------------------------------------

type receiver struct {
	UID                   string         `json:"uid"`
	Name                  string         `json:"name"`
	Type                  string         `json:"type"`
	Settings              map[string]any `json:"settings"`
	DisableResolveMessage bool           `json:"disableResolveMessage"`
	Provenance            string         `json:"provenance"`
}

// field maps one Grafana setting to a Terraform attribute.
type field struct {
	attr   string
	secret string // Crossplane *SecretRef field; set for sensitive attributes
	list   bool   // split "a;b,c" into a list
	number bool
	bool   bool
}

// integrations are the contact point types this provider maps, by type:
// Grafana setting → Terraform attribute of the type's block.
var integrations = map[string]map[string]field{
	"email": {
		"addresses":   {attr: "addresses", list: true},
		"singleEmail": {attr: "single_email", bool: true},
		"message":     {attr: "message"},
		"subject":     {attr: "subject"},
	},
	"slack": {
		"url":            {attr: "url", secret: "urlSecretRef"},
		"token":          {attr: "token", secret: "tokenSecretRef"},
		"recipient":      {attr: "recipient"},
		"username":       {attr: "username"},
		"icon_emoji":     {attr: "icon_emoji"},
		"icon_url":       {attr: "icon_url"},
		"mentionChannel": {attr: "mention_channel"},
		"mentionUsers":   {attr: "mention_users"},
		"mentionGroups":  {attr: "mention_groups"},
		"title":          {attr: "title"},
		"text":           {attr: "text"},
		"endpointUrl":    {attr: "endpoint_url"},
		"color":          {attr: "color"},
	},
	"webhook": {
		"url":                       {attr: "url"},
		"httpMethod":                {attr: "http_method"},
		"maxAlerts":                 {attr: "max_alerts", number: true},
		"username":                  {attr: "basic_auth_user"},
		"password":                  {attr: "basic_auth_password", secret: "basicAuthPasswordSecretRef"},
		"authorization_scheme":      {attr: "authorization_scheme"},
		"authorization_credentials": {attr: "authorization_credentials", secret: "authorizationCredentialsSecretRef"},
		"title":                     {attr: "title"},
		"message":                   {attr: "message"},
	},
}

func readContactPoints(ctx context.Context, r *run, o org) error {
	var list []receiver
	if err := r.s.c.get(ctx, o.ID, "/api/v1/provisioning/contact-points", &list); err != nil {
		return err
	}
	// Secret values: the export endpoint decrypts them for admins.
	secrets := map[string]map[string]any{}
	var export struct {
		ContactPoints []struct {
			Receivers []struct {
				UID      string         `json:"uid"`
				Settings map[string]any `json:"settings"`
			} `json:"receivers"`
		} `json:"contactPoints"`
	}
	if err := r.s.c.get(ctx, o.ID, "/api/v1/provisioning/contact-points/export?format=json&decrypt=true", &export); err == nil {
		for _, cp := range export.ContactPoints {
			for _, rc := range cp.Receivers {
				secrets[rc.UID] = rc.Settings
			}
		}
	} else {
		sdk.Logger(ctx).Info("secret contact point settings can't be read (needs an Admin); they are left out", "organization", o.Name)
	}

	byName := map[string][]receiver{}
	var names []string
	for _, rc := range list {
		if _, ok := byName[rc.Name]; !ok {
			names = append(names, rc.Name)
		}
		byName[rc.Name] = append(byName[rc.Name], rc)
	}
	sort.Strings(names)
next:
	for _, name := range names {
		rcs := byName[name]
		attrs := r.orgAttrs(o, map[string]any{"name": name})
		provenance := rcs[0].Provenance
		if fromFiles(provenance) {
			if err := r.skip(o, key("contactpoint", o, name), kContactPoint, name, provenanceReason(provenance)); err != nil {
				return err
			}
			continue
		}
		blocks := map[string][]any{}
		for _, rc := range rcs {
			fields, ok := integrations[rc.Type]
			if !ok {
				if err := r.skip(o, key("contactpoint", o, name), kContactPoint, name, fmt.Sprintf("has a %s integration, which this provider doesn't map yet (exporting the rest would delete it on apply)", rc.Type)); err != nil {
					return err
				}
				continue next
			}
			block := map[string]any{}
			if rc.DisableResolveMessage {
				block["disable_resolve_message"] = true
			}
			plain := secrets[rc.UID]
			var unknown []string
			for k, v := range rc.Settings {
				f, ok := fields[k]
				if !ok {
					// Terraform keeps settings it doesn't model in its
					// settings map, empty ones too: the plan would differ.
					unknown = append(unknown, k)
					continue
				}
				if f.secret != "" {
					var val any
					if s, ok := plain[k].(string); ok && s != "" && s != "[REDACTED]" {
						val = s
					}
					block[f.attr] = sdk.Secret(f.attr, val, f.secret, rc.Type+" "+f.attr)
					continue
				}
				conv, ok := convert(v, f)
				if ok {
					block[f.attr] = conv
				}
			}
			if len(unknown) > 0 {
				sort.Strings(unknown)
				if err := r.skip(o, key("contactpoint", o, name), kContactPoint, name, fmt.Sprintf("its %s integration uses settings this provider doesn't map yet: %s", rc.Type, strings.Join(unknown, ", "))); err != nil {
					return err
				}
				continue next
			}
			blocks[rc.Type] = append(blocks[rc.Type], block)
		}
		for t, b := range blocks {
			attrs[t] = b
		}
		disableProvenance(attrs, provenance)
		var notes []string
		if hasSecretValue(attrs) {
			notes = append(notes, "Grafana doesn't return secret settings to Terraform, so the first plan updates this contact point in place with the same values.")
		}
		if err := r.emit(sdk.Resource{Key: key("contactpoint", o, name), Kind: kContactPoint, Partition: strconv.FormatInt(o.ID, 10),
			Name: name, ImportID: r.importID(o, name), Attributes: attrs, Notes: notes}); err != nil {
			return err
		}
	}
	return nil
}

func convert(v any, f field) (any, bool) {
	switch {
	case f.list:
		s, _ := v.(string)
		// Exactly like the Terraform provider (no trimming), or the plan
		// differs.
		var out []any
		for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == '\n' }) {
			out = append(out, part)
		}
		return out, len(out) > 0
	// false and 0 are the Terraform provider's defaults, which it also writes
	// into Grafana when it updates a contact point: treat them as unset, so
	// an export after a Terraform update matches the one before.
	case f.bool:
		switch t := v.(type) {
		case bool:
			return t, t
		case string:
			b, err := strconv.ParseBool(t)
			return b, err == nil && b
		}
		return nil, false
	case f.number:
		switch t := v.(type) {
		case float64:
			return json.Number(strconv.FormatFloat(t, 'f', -1, 64)), t != 0
		case string:
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return json.Number(t), f != 0
			}
		}
		return nil, false
	}
	switch t := v.(type) {
	case string:
		return t, t != ""
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	}
	return nil, false
}

// --- notification policies -------------------------------------------------

type route struct {
	Receiver            string     `json:"receiver"`
	GroupBy             []string   `json:"group_by"`
	ObjectMatchers      [][]string `json:"object_matchers"`
	Continue            bool       `json:"continue"`
	GroupWait           string     `json:"group_wait"`
	GroupInterval       string     `json:"group_interval"`
	RepeatInterval      string     `json:"repeat_interval"`
	MuteTimeIntervals   []string   `json:"mute_time_intervals"`
	ActiveTimeIntervals []string   `json:"active_time_intervals"`
	Routes              []route    `json:"routes"`
	Provenance          string     `json:"provenance"`
	// Older matcher forms Terraform doesn't model.
	Matchers []string          `json:"matchers"`
	Match    map[string]string `json:"match"`
	MatchRE  map[string]string `json:"match_re"`
}

// legacyMatchers reports whether a route (or one below it) uses matcher
// forms the Terraform provider can't express.
func legacyMatchers(rt route) bool {
	if len(rt.Matchers) > 0 || len(rt.Match) > 0 || len(rt.MatchRE) > 0 {
		return true
	}
	for _, c := range rt.Routes {
		if legacyMatchers(c) {
			return true
		}
	}
	return false
}

func readNotificationPolicy(ctx context.Context, r *run, o org) error {
	var root route
	if err := r.s.c.get(ctx, o.ID, "/api/v1/provisioning/policies", &root); err != nil {
		return err
	}
	name := "Notification policy"
	if fromFiles(root.Provenance) {
		return r.skip(o, "notificationpolicy/"+strconv.FormatInt(o.ID, 10), kNotificationPolicy, name, provenanceReason(root.Provenance))
	}
	pk := "notificationpolicy/" + strconv.FormatInt(o.ID, 10)
	if depth(root) > maxPolicyDepth {
		return r.skip(o, pk, kNotificationPolicy, name, fmt.Sprintf("nested deeper than the %d levels the Terraform provider supports", maxPolicyDepth))
	}
	if legacyMatchers(root) {
		return r.skip(o, pk, kNotificationPolicy, name, "uses matchers/match/match_re, which the Terraform provider can't express (it would turn those routes into catch-alls)")
	}
	attrs := r.orgAttrs(o, map[string]any{
		"contact_point": r.contactPointRef(o, root.Receiver),
		"group_by":      strings2any(root.GroupBy),
	})
	timing(attrs, root)
	if p := policies(r, o, root.Routes, 1); len(p) > 0 {
		attrs["policy"] = p
	}
	disableProvenance(attrs, root.Provenance)
	var deps []string
	for _, mt := range muteTimings(root) {
		deps = append(deps, key("mutetiming", o, mt))
	}
	return r.emit(sdk.Resource{Key: pk, Kind: kNotificationPolicy,
		Partition: strconv.FormatInt(o.ID, 10), Name: name, ImportID: r.importID(o, "policy"), Attributes: attrs, DependsOn: deps})
}

func muteTimings(rt route) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(route)
	walk = func(rt route) {
		for _, m := range append(append([]string{}, rt.MuteTimeIntervals...), rt.ActiveTimeIntervals...) {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
		for _, c := range rt.Routes {
			walk(c)
		}
	}
	walk(rt)
	sort.Strings(out)
	return out
}

func depth(rt route) int {
	d := 0
	for _, c := range rt.Routes {
		if cd := depth(c) + 1; cd > d {
			d = cd
		}
	}
	return d
}

func policies(r *run, o org, routes []route, level int) []any {
	var out []any
	for _, rt := range routes {
		p := map[string]any{}
		if rt.Receiver != "" {
			p["contact_point"] = r.contactPointRef(o, rt.Receiver)
			if level == maxPolicyDepth {
				// The deepest level has no contactPointRef in the CRD.
				p["contact_point"] = sdk.Ref(key("contactpoint", o, rt.Receiver), "name", rt.Receiver, "")
			}
		}
		switch {
		case len(rt.GroupBy) > 0:
			p["group_by"] = strings2any(rt.GroupBy)
		case level == maxPolicyDepth:
			// The deepest level requires group_by; Terraform treats [] as
			// unset.
			p["group_by"] = []any{}
		}
		if rt.Continue {
			p["continue"] = true
		}
		timing(p, rt)
		if len(rt.MuteTimeIntervals) > 0 {
			p["mute_timings"] = strings2any(rt.MuteTimeIntervals)
		}
		if len(rt.ActiveTimeIntervals) > 0 {
			p["active_timings"] = strings2any(rt.ActiveTimeIntervals)
		}
		var matchers []any
		for _, m := range rt.ObjectMatchers {
			if len(m) == 3 {
				matchers = append(matchers, map[string]any{"label": m[0], "match": m[1], "value": m[2]})
			}
		}
		if len(matchers) > 0 {
			p["matcher"] = matchers
		}
		if sub := policies(r, o, rt.Routes, level+1); len(sub) > 0 {
			p["policy"] = sub
		}
		out = append(out, p)
	}
	return out
}

func timing(m map[string]any, rt route) {
	for k, v := range map[string]string{"group_wait": rt.GroupWait, "group_interval": rt.GroupInterval, "repeat_interval": rt.RepeatInterval} {
		if v != "" {
			m[k] = v
		}
	}
}

func (r *run) contactPointRef(o org, name string) any {
	return sdk.Ref(key("contactpoint", o, name), "name", name, "contactPointRef")
}

// --- rule groups -----------------------------------------------------------

type alertRule struct {
	UID          string            `json:"uid"`
	Title        string            `json:"title"`
	FolderUID    string            `json:"folderUID"`
	RuleGroup    string            `json:"ruleGroup"`
	Condition    string            `json:"condition"`
	NoDataState  string            `json:"noDataState"`
	ExecErrState string            `json:"execErrState"`
	For          string            `json:"for"`
	KeepFiring   string            `json:"keep_firing_for"`
	Annotations  map[string]string `json:"annotations"`
	Labels       map[string]string `json:"labels"`
	IsPaused     bool              `json:"isPaused"`
	Provenance   string            `json:"provenance"`
	MissingEvals *int64            `json:"missing_series_evals_to_resolve"`
	Data         []struct {
		RefID             string `json:"refId"`
		QueryType         string `json:"queryType"`
		DatasourceUID     string `json:"datasourceUid"`
		RelativeTimeRange struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"relativeTimeRange"`
		Model json.RawMessage `json:"model"`
	} `json:"data"`
	NotificationSettings *struct {
		Receiver            string   `json:"receiver"`
		Policy              string   `json:"policy"`
		GroupBy             []string `json:"group_by"`
		GroupWait           string   `json:"group_wait"`
		GroupInterval       string   `json:"group_interval"`
		RepeatInterval      string   `json:"repeat_interval"`
		MuteTimeIntervals   []string `json:"mute_time_intervals"`
		ActiveTimeIntervals []string `json:"active_time_intervals"`
	} `json:"notification_settings"`
	Record *struct {
		Metric              string `json:"metric"`
		From                string `json:"from"`
		TargetDatasourceUID string `json:"target_datasource_uid"`
	} `json:"record"`
}

func readRuleGroups(ctx context.Context, r *run, o org) error {
	var rules []alertRule
	if err := r.s.c.get(ctx, o.ID, "/api/v1/provisioning/alert-rules", &rules); err != nil {
		return err
	}
	type groupKey struct{ folder, name string }
	groups := map[groupKey][]alertRule{}
	var order []groupKey
	for _, rl := range rules {
		k := groupKey{rl.FolderUID, rl.RuleGroup}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], rl)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].folder != order[j].folder {
			return order[i].folder < order[j].folder
		}
		return order[i].name < order[j].name
	})
	for _, gk := range order {
		rs := groups[gk]
		title := gk.name + " (" + r.folderPath(o, gk.folder) + ")"
		k := key("rulegroup", o, gk.folder+"/"+gk.name)
		if !r.req.Scope.HasKey(k) {
			continue
		}
		if fromFiles(rs[0].Provenance) {
			if err := r.skip(o, k, kRuleGroup, title, provenanceReason(rs[0].Provenance)); err != nil {
				return err
			}
			continue
		}
		var group struct {
			Interval int64       `json:"interval"`
			Rules    []alertRule `json:"rules"`
		}
		if err := r.s.c.get(ctx, o.ID, "/api/v1/provisioning/folder/"+url.PathEscape(gk.folder)+"/rule-groups/"+url.PathEscape(gk.name), &group); err != nil {
			return fmt.Errorf("rule group %q: %w", title, err)
		}
		// Terraform reads rules in the group's order. The group endpoint
		// leaves provenance out: keep the flat list's.
		provenance := rs[0].Provenance
		if len(group.Rules) > 0 {
			rs = group.Rules
		}
		var ruleBlocks []any
		for _, rl := range rs {
			rule := map[string]any{"name": rl.Title, "uid": rl.UID}
			for k, v := range map[string]string{"condition": rl.Condition, "no_data_state": rl.NoDataState, "exec_err_state": rl.ExecErrState,
				"for": rl.For, "keep_firing_for": rl.KeepFiring} {
				if v != "" {
					rule[k] = v
				}
			}
			if rl.IsPaused {
				rule["is_paused"] = true
			}
			if rl.MissingEvals != nil {
				rule["missing_series_evals_to_resolve"] = json.Number(strconv.FormatInt(*rl.MissingEvals, 10))
			}
			if len(rl.Annotations) > 0 {
				rule["annotations"] = stringMap(rl.Annotations)
			}
			if len(rl.Labels) > 0 {
				rule["labels"] = stringMap(rl.Labels)
			}
			var data []any
			for _, d := range rl.Data {
				q := map[string]any{"ref_id": d.RefID, "datasource_uid": d.DatasourceUID,
					"relative_time_range": []any{map[string]any{
						"from": json.Number(strconv.FormatInt(d.RelativeTimeRange.From, 10)),
						"to":   json.Number(strconv.FormatInt(d.RelativeTimeRange.To, 10)),
					}},
					"model": sdk.JSON(sdk.RawJSON(d.Model)),
				}
				if d.QueryType != "" {
					q["query_type"] = d.QueryType
				}
				data = append(data, q)
			}
			rule["data"] = data
			if ns := rl.NotificationSettings; ns != nil && (ns.Receiver != "" || ns.Policy != "") {
				n := map[string]any{}
				if ns.Receiver != "" {
					n["contact_point"] = r.contactPointRef(o, ns.Receiver)
				}
				if ns.Policy != "" {
					n["policy"] = ns.Policy
				}
				if len(ns.GroupBy) > 0 {
					n["group_by"] = strings2any(ns.GroupBy)
				}
				for k, v := range map[string]string{"group_wait": ns.GroupWait, "group_interval": ns.GroupInterval, "repeat_interval": ns.RepeatInterval} {
					if v != "" {
						n[k] = v
					}
				}
				if len(ns.MuteTimeIntervals) > 0 {
					n["mute_timings"] = strings2any(ns.MuteTimeIntervals)
				}
				if len(ns.ActiveTimeIntervals) > 0 {
					n["active_timings"] = strings2any(ns.ActiveTimeIntervals)
				}
				rule["notification_settings"] = []any{n}
			}
			if rec := rl.Record; rec != nil && rec.Metric != "" {
				rc := map[string]any{"metric": rec.Metric, "from": rec.From}
				if rec.TargetDatasourceUID != "" {
					rc["target_datasource_uid"] = rec.TargetDatasourceUID
				}
				rule["record"] = []any{rc}
			}
			ruleBlocks = append(ruleBlocks, rule)
		}
		attrs := r.orgAttrs(o, map[string]any{
			"name":             gk.name,
			"folder_uid":       r.folderRef(o, gk.folder),
			"interval_seconds": json.Number(strconv.FormatInt(group.Interval, 10)),
			"rule":             ruleBlocks,
		})
		disableProvenance(attrs, provenance)
		if err := r.emit(sdk.Resource{Key: k, Kind: kRuleGroup, Partition: strconv.FormatInt(o.ID, 10), Name: title,
			ImportID: r.importID(o, gk.folder, gk.name), Attributes: attrs}); err != nil {
			return err
		}
	}
	return nil
}

func strings2any(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func stringMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func hasSecretValue(v any) bool {
	switch t := v.(type) {
	case sdk.SecretValue:
		return t.Value != nil
	case map[string]any:
		for _, e := range t {
			if hasSecretValue(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasSecretValue(e) {
				return true
			}
		}
	}
	return false
}

func strconvQuote(s string) string { return strconv.Quote(s) }
