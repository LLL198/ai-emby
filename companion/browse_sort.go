package main

import (
	"net/http"
	"strings"
)

type browseSortSpec struct {
	expr    string
	kind    string
	userArg bool
}

type browseSortPlan struct {
	joins   string
	columns []string
	kinds   []string
	args    []any
	desc    bool
}

var browseSortSpecs = map[string]browseSortSpec{
	"communityrating":            {expr: `CASE WHEN COALESCE(md.data::jsonb->>'Rating','') ~ '^[0-9]+([.][0-9]+)?$' THEN (md.data::jsonb->>'Rating')::double precision ELSE 0 END`, kind: "float"},
	"criticrating":               {expr: `CASE WHEN COALESCE(md.data::jsonb->>'CriticRating','') ~ '^[0-9]+([.][0-9]+)?$' THEN (md.data::jsonb->>'CriticRating')::double precision ELSE 0 END`, kind: "float"},
	"dateplayed":                 {expr: `CASE WHEN items.kind='Series' THEN COALESCE((SELECT max(ue.last_played) FROM items e LEFT JOIN items s ON s.id=e.parent AND s.kind='Season' JOIN userdata_extra ue ON ue.item=e.id AND ue.user_id=viewer.user_id WHERE e.kind='Episode' AND (e.parent=items.id OR s.parent=items.id)),'') ELSE COALESCE((SELECT ue.last_played FROM userdata_extra ue WHERE ue.item=items.id AND ue.user_id=viewer.user_id),'') END`, kind: "text", userArg: true},
	"latestadded":                {kind: "int"},
	"seriesdatelastcontentadded": {expr: `CASE WHEN items.kind='Series' THEN COALESCE((SELECT max(e.added_at) FROM items e LEFT JOIN items s ON s.id=e.parent AND s.kind='Season' WHERE e.kind='Episode' AND (e.parent=items.id OR s.parent=items.id)),0) ELSE items.added_at END`, kind: "int"},
	"seriesdatelastreleased":     {expr: `CASE WHEN items.kind='Series' THEN COALESCE((SELECT max(e.premiere_date) FROM items e LEFT JOIN items s ON s.id=e.parent AND s.kind='Season' WHERE e.kind='Episode' AND (e.parent=items.id OR s.parent=items.id)),'') ELSE items.premiere_date END`, kind: "text"},
	"playcount":                  {expr: `CASE WHEN items.kind='Series' THEN COALESCE((SELECT sum(ue.play_count) FROM items e LEFT JOIN items s ON s.id=e.parent AND s.kind='Season' JOIN userdata_extra ue ON ue.item=e.id AND ue.user_id=viewer.user_id WHERE e.kind='Episode' AND (e.parent=items.id OR s.parent=items.id)),0) ELSE COALESCE((SELECT ue.play_count FROM userdata_extra ue WHERE ue.item=items.id AND ue.user_id=viewer.user_id),0) END`, kind: "int", userArg: true},
	"officialrating":             {expr: `COALESCE(md.data::jsonb->>'MPAA','')`, kind: "text"},
	"resumeupdated": {expr: `COALESCE((SELECT GREATEST(COALESCE(ra.updated,0),
	 CASE WHEN COALESCE(ue.last_played,'') ~ '^\d{4}-\d{2}-\d{2}T' THEN
	 (EXTRACT(EPOCH FROM ue.last_played::timestamptz)*1000000000)::bigint ELSE 0 END)
	 FROM userdata d LEFT JOIN resume_activity ra ON ra.user_id=d.user_id AND ra.item=d.item
	 LEFT JOIN userdata_extra ue ON ue.user_id=d.user_id AND ue.item=d.item WHERE d.user_id=viewer.user_id AND d.item=items.id),0)`, kind: "int", userArg: true},
	"resumegroup": {expr: `CASE WHEN EXISTS (SELECT 1 FROM userdata d WHERE d.user_id=viewer.user_id AND d.item=items.id AND d.played=1) THEN 0 ELSE 1 END`, kind: "int", userArg: true},
}

func (a *App) latestAddedJoin() string {
	episodes := `SELECT COALESCE(season.parent,e.parent) AS series_id,max(e.added_at) AS latest_added FROM items e LEFT JOIN items season ON season.id=e.parent AND season.kind='Season' WHERE e.kind='Episode' GROUP BY COALESCE(season.parent,e.parent)`
	if a.versionGrouping() == "id" {
		return " LEFT JOIN (" + episodes + ") latest_episode ON latest_episode.series_id=items.id AND items.kind='Series'"
	}
	group := a.versionGroupingFor("v")
	return " LEFT JOIN (SELECT " + group + " AS group_key,max(GREATEST(v.added_at,COALESCE(latest_episode.latest_added,0))) AS latest_added FROM items v LEFT JOIN (" + episodes + ") latest_episode ON latest_episode.series_id=v.id AND v.kind='Series' WHERE v.kind IN ('Movie','Series') GROUP BY " + group + ") latest_version ON latest_version.group_key=" + a.versionGroupingFor("items")
}

func (a *App) buildBrowseSortPlan(order, userID string) browseSortPlan {
	plan := browseSortPlan{desc: strings.Contains(order, "DESC")}
	joined := map[string]bool{}
	seen := map[string]bool{}
	for _, part := range strings.Split(order, ",") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		key := strings.ToLower(fields[0])
		if key == "id" || seen[key] || len(plan.columns) >= 16 {
			continue
		}
		spec, found := browseSortSpecs[key]
		if !found {
			switch key {
			case "name", "sort_name", "premiere_date", "random_key":
				spec = browseSortSpec{expr: "items." + key, kind: "text"}
			case "year", "season", "episode", "mtime", "added_at", "size":
				spec = browseSortSpec{expr: "items." + key, kind: "int"}
			default:
				continue
			}
		}
		seen[key] = true
		if strings.Contains(spec.expr, "md.") && !joined["metadata"] {
			plan.joins += " LEFT JOIN item_metadata md ON md.item=items.id"
			joined["metadata"] = true
		}
		if spec.userArg && !joined["viewer"] {
			plan.joins += " CROSS JOIN (SELECT CAST(? AS text) AS user_id) viewer"
			plan.args = append(plan.args, userID)
			joined["viewer"] = true
		}
		if key == "latestadded" {
			plan.joins += a.latestAddedJoin()
			spec.expr = "GREATEST(items.added_at,COALESCE(latest_episode.latest_added,0))"
			if a.versionGrouping() != "id" {
				spec.expr = "COALESCE(latest_version.latest_added,items.added_at)"
			}
		}
		plan.columns = append(plan.columns, spec.expr)
		plan.kinds = append(plan.kinds, spec.kind)
	}
	if len(plan.columns) == 0 {
		plan.columns = append(plan.columns, "items.name")
		plan.kinds = append(plan.kinds, "text")
	}
	plan.columns = append(plan.columns, "items.id")
	plan.kinds = append(plan.kinds, "text")
	return plan
}

func randomPivot(r *http.Request) string {
	seed := strings.TrimSpace(q(r, "RandomSeed"))
	if seed == "" {
		return id()
	}
	return digest(seed)
}
