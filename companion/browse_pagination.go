package main

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type browseSortedRow struct {
	row    interface{ Scan(...any) error }
	values []any
}

func (row browseSortedRow) Scan(destinations ...any) error {
	return row.row.Scan(append(destinations, row.values...)...)
}

func browseSortHolders(kinds []string) ([]any, func() []string) {
	integers := make([]int64, len(kinds))
	floats := make([]float64, len(kinds))
	texts := make([]string, len(kinds))
	holders := make([]any, len(kinds))
	for index, kind := range kinds {
		switch kind {
		case "int":
			holders[index] = &integers[index]
		case "float":
			holders[index] = &floats[index]
		default:
			holders[index] = &texts[index]
		}
	}
	return holders, func() []string {
		values := make([]string, len(kinds))
		for index, kind := range kinds {
			switch kind {
			case "int":
				values[index] = strconv.FormatInt(integers[index], 10)
			case "float":
				values[index] = strconv.FormatFloat(floats[index], 'g', -1, 64)
			default:
				values[index] = texts[index]
			}
		}
		return values
	}
}

func stringValues(values, kinds []string) []any {
	parameters := make([]any, len(values))
	for index, value := range values {
		switch kinds[index] {
		case "int":
			parameters[index], _ = strconv.ParseInt(value, 10, 64)
		case "float":
			parameters[index], _ = strconv.ParseFloat(value, 64)
		default:
			parameters[index] = value
		}
	}
	return parameters
}

func validateBrowseValues(values, kinds []string) error {
	if len(values) != len(kinds) {
		return errors.New("invalid cursor keys")
	}
	for index, kind := range kinds {
		switch kind {
		case "int":
			if _, err := strconv.ParseInt(values[index], 10, 64); err != nil {
				return errors.New("invalid numeric cursor key")
			}
		case "float":
			value, err := strconv.ParseFloat(values[index], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return errors.New("invalid numeric cursor key")
			}
		}
	}
	return nil
}

func (a *App) browsePlanPage(r *http.Request, user User, where string, args []any, order string, limit int) ([]Item, pageToken, string, bool, error) {
	plan := a.buildBrowseSortPlan(order, user.ID)
	parameters := append(append([]any{}, plan.args...), args...)
	selects, names, orders := []string{}, []string{}, []string{}
	direction, comparison := " ASC", ">"
	if plan.desc {
		direction, comparison = " DESC", "<"
	}
	for index, expression := range plan.columns {
		name := "browse_sort_" + strconv.Itoa(index)
		selects = append(selects, expression+" AS "+name)
		names = append(names, name)
		orders = append(orders, name+direction)
	}
	qualified := "items." + strings.ReplaceAll(cols, ",", ",items.")
	base := "WITH browse_catalog AS (SELECT " + qualified + "," + strings.Join(selects, ",") + " FROM items" + plan.joins + " WHERE " + where + ")"
	key := a.pageSignature(r, base, order, parameters)
	page, err := a.pageStart(r, key)
	if err != nil {
		return nil, page, "", false, err
	}
	if page.Count < 0 && !strings.EqualFold(q(r, "EnableTotalRecordCount"), "false") {
		if err = a.mediaReader(r).QueryRow("SELECT count(*) FROM items WHERE "+where, args...).Scan(&page.Count); err != nil {
			return nil, page, "", false, err
		}
	}
	statement := base + " SELECT " + cols + "," + strings.Join(names, ",") + " FROM browse_catalog"
	if len(page.Values) > 0 {
		if err = validateBrowseValues(page.Values, plan.kinds); err != nil {
			return nil, page, "", false, err
		}
		statement += " WHERE (" + strings.Join(names, ",") + ") " + comparison + " (" + catalogPlaceholders(len(names)) + ")"
		parameters = append(parameters, stringValues(page.Values, plan.kinds)...)
	}
	statement += " ORDER BY " + strings.Join(orders, ",") + " LIMIT ?"
	parameters = append(parameters, limit+1)
	if len(page.Values) == 0 && page.Position > 0 {
		statement += " OFFSET ?"
		parameters = append(parameters, page.Position)
	}
	return a.readBrowsePage(r, statement, parameters, page, plan.kinds, limit)
}

func (a *App) readBrowsePage(r *http.Request, statement string, args []any, page pageToken, kinds []string, limit int) ([]Item, pageToken, string, bool, error) {
	rows, err := a.mediaReader(r).Query(statement, args...)
	if err != nil {
		return nil, page, "", false, err
	}
	defer rows.Close()
	items := []Item{}
	var lastValues []string
	holders, values := browseSortHolders(kinds)
	for rows.Next() {
		item, err := readItem(browseSortedRow{row: rows, values: holders})
		if err != nil {
			return nil, page, "", false, err
		}
		items = append(items, item)
		if len(items) <= limit {
			lastValues = values()
		}
	}
	if err = rows.Err(); err != nil {
		return nil, page, "", false, err
	}
	more := len(items) > limit
	next := ""
	if more {
		items = items[:limit]
		continuation := page
		continuation.Position += len(items)
		continuation.Expires = time.Now().Add(15 * time.Minute).Unix()
		continuation.Values = lastValues
		next = a.savePage(continuation)
	}
	return items, page, next, more, nil
}

func (a *App) randomPage(r *http.Request, where string, args []any, order string, limit int) ([]Item, pageToken, string, bool, error) {
	key := a.pageSignature(r, where, order, args)
	page, err := a.pageStart(r, key)
	if err != nil {
		return nil, page, "", false, err
	}
	if page.Pivot == "" {
		if page.Position > 0 {
			a.pages.Lock()
			first := a.pages.Entries[key+":0"]
			a.pages.Unlock()
			if time.Now().Before(first.Until) {
				if initial, err := a.decodePage(first.Token, key); err == nil {
					page.Pivot = initial.Pivot
				}
			}
		}
		if page.Pivot == "" {
			page.Pivot = randomPivot(r)
		}
		initial := page
		initial.Position, initial.Values, initial.Wrapped = 0, nil, false
		a.savePage(initial)
	}
	if page.Count < 0 && !strings.EqualFold(q(r, "EnableTotalRecordCount"), "false") {
		if err = a.mediaReader(r).QueryRow("SELECT count(*) FROM items WHERE "+where, args...).Scan(&page.Count); err != nil {
			return nil, page, "", false, err
		}
	}
	if len(page.Values) != 0 && len(page.Values) != 2 {
		return nil, page, "", false, errors.New("invalid random cursor keys")
	}
	descending := strings.Contains(order, "DESC")
	direction, comparison := " ASC", ">"
	if descending {
		direction, comparison = " DESC", "<"
	}
	offset := 0
	if len(page.Values) == 0 {
		offset = page.Position
	}
	wrapped := page.Wrapped
	items := []Item{}
	for {
		boundary := ">="
		if descending != wrapped {
			boundary = "<"
		}
		filter := "(" + where + ") AND random_key " + boundary + " ?"
		parameters := append(append([]any{}, args...), page.Pivot)
		if len(page.Values) > 0 {
			filter += " AND (random_key,id) " + comparison + " (?,?)"
			parameters = append(parameters, page.Values[0], page.Values[1])
		}
		if offset > 0 && !wrapped {
			var count int
			if err = a.mediaReader(r).QueryRow("SELECT count(*) FROM items WHERE "+filter, parameters...).Scan(&count); err != nil {
				return nil, page, "", false, err
			}
			if offset >= count {
				offset -= count
				wrapped = true
				page.Values = nil
				continue
			}
		}
		parameters = append(parameters, limit+1-len(items), offset)
		rows, err := a.mediaReader(r).Query("SELECT "+cols+" FROM items WHERE "+filter+" ORDER BY random_key"+direction+",id"+direction+" LIMIT ? OFFSET ?", parameters...)
		if err != nil {
			return nil, page, "", false, err
		}
		for rows.Next() {
			item, readErr := readItem(rows)
			if readErr != nil {
				rows.Close()
				return nil, page, "", false, readErr
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, page, "", false, err
		}
		if len(items) > limit || wrapped {
			break
		}
		wrapped, offset = true, 0
		page.Values = nil
	}
	more, next := len(items) > limit, ""
	if more {
		items = items[:limit]
		last := items[len(items)-1]
		continuation := page
		continuation.Position += len(items)
		continuation.Expires = time.Now().Add(15 * time.Minute).Unix()
		continuation.Values = []string{last.RandomKey, last.ID}
		continuation.Wrapped = last.RandomKey < page.Pivot
		if descending {
			continuation.Wrapped = last.RandomKey >= page.Pivot
		}
		next = a.savePage(continuation)
	}
	return items, page, next, more, nil
}
