package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) titleSearchWhere(where string, args []any, term string, titlesOnly bool) (string, []any) {
	term = strings.TrimSpace(term)
	if term == "" {
		return where, args
	}
	if titlesOnly {
		where += " AND kind IN ('Movie','Series')"
	}
	pattern := "%" + catalogLike(term) + "%"
	clause := "name ILIKE ? ESCAPE '\\' OR sort_name ILIKE ? ESCAPE '\\'"
	args = append(args, pattern, pattern)
	if a.defaultOn("search_by_initials") && isInitialsQuery(term) {
		prefix := catalogLike(strings.ToLower(term)) + "%"
		clause += " OR id IN (SELECT id FROM items WHERE name ~ '[一-龥]' AND media_initials(name) LIKE ? ESCAPE '\\' UNION ALL SELECT id FROM items WHERE sort_name ~ '[一-龥]' AND media_initials(sort_name) LIKE ? ESCAPE '\\')"
		args = append(args, prefix, prefix)
	}
	return where + " AND (" + clause + ")", args
}

func titleRankSQL(term string) (string, []any) {
	term = strings.TrimSpace(term)
	if term == "" || isInitialsQuery(term) {
		return "0", nil
	}
	conditions := []string{}
	args := []any{}
	for rank := 0; rank < 3; rank++ {
		matches := []string{}
		for _, column := range []string{"name", "sort_name"} {
			if rank == 0 {
				matches = append(matches, "lower(btrim("+column+"))=lower(?)")
				args = append(args, term)
			} else {
				pattern := catalogLike(term) + "%"
				if rank == 2 {
					pattern = "%" + pattern
				}
				matches = append(matches, column+" ILIKE ? ESCAPE '\\'")
				args = append(args, pattern)
			}
		}
		conditions = append(conditions, "WHEN ("+strings.Join(matches, " OR ")+") THEN "+strconv.Itoa(rank))
	}
	return "CASE " + strings.Join(conditions, " ") + " ELSE 3 END", args
}

func (a *App) relevancePage(r *http.Request, user User, where string, args []any, order string, limit int) ([]Item, pageToken, string, bool, error) {
	rank, rankArgs := titleRankSQL(q(r, "SearchTerm"))
	parameters := append(append([]any{}, rankArgs...), args...)
	base := "WITH ranked_catalog AS (SELECT " + cols + "," + rank + " AS search_rank FROM items WHERE " + where + ")"
	source := "ranked_catalog"
	if !user.API && order != "resume" && q(r, "Ids") == "" {
		if group := a.versionGrouping(); group != "id" {
			base += ", ranked_groups AS (SELECT DISTINCT ON (" + group + ") * FROM ranked_catalog ORDER BY " + group + ",search_rank,sort_name,id)"
			source = "ranked_groups"
		}
	}
	key := a.pageSignature(r, base, "title-relevance-v1", parameters)
	page, err := a.pageStart(r, key)
	if err != nil {
		return nil, page, "", false, err
	}
	if page.Count < 0 && !strings.EqualFold(q(r, "EnableTotalRecordCount"), "false") {
		if err = a.mediaReader(r).QueryRow(base+" SELECT count(*) FROM "+source, parameters...).Scan(&page.Count); err != nil {
			return nil, page, "", false, err
		}
	}
	statement := base + " SELECT " + cols + ",search_rank,sort_name,id FROM " + source
	if len(page.Values) > 0 {
		if err = validateBrowseValues(page.Values, []string{"int", "text", "text"}); err != nil {
			return nil, page, "", false, err
		}
		values := stringValues(page.Values, []string{"int", "text", "text"})
		if values[0].(int64) < 0 || values[0].(int64) > 3 {
			return nil, page, "", false, errors.New("invalid search rank")
		}
		statement += " WHERE (search_rank,sort_name,id)>(?,?,?)"
		parameters = append(parameters, values...)
	}
	statement += " ORDER BY search_rank,sort_name,id LIMIT ?"
	parameters = append(parameters, limit+1)
	if len(page.Values) == 0 && page.Position > 0 {
		statement += " OFFSET ?"
		parameters = append(parameters, page.Position)
	}
	return a.readBrowsePage(r, statement, parameters, page, []string{"int", "text", "text"}, limit)
}
