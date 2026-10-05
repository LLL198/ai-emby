package main

import (
	"github.com/mozillazg/go-pinyin"
	"strings"
	"sync"
)

var initialsOnce sync.Once
var initialsSQL string

// PostgreSQL computes indexed initials for both existing and newly scanned titles.
// Unknown characters remain searchable as written; polyphones use dictionary order.
func (a *App) initialsSchema() error {
	initialsOnce.Do(func() {
		var han, latin strings.Builder
		opts := pinyin.NewArgs()
		opts.Style = pinyin.FirstLetter
		for ch := rune(0x3400); ch <= 0x9fff; ch++ {
			values := pinyin.SinglePinyin(ch, opts)
			if len(values) > 0 && len(values[0]) == 1 && values[0][0] >= 'a' && values[0][0] <= 'z' {
				han.WriteRune(ch)
				latin.WriteString(values[0])
			}
		}
		initialsSQL = "CREATE OR REPLACE FUNCTION media_initials(n text) RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS 'SELECT lower(translate(n,''" + han.String() + "'',''" + latin.String() + "''))'; CREATE INDEX IF NOT EXISTS items_initials_search ON items USING gin(media_initials(name) gin_trgm_ops); CREATE INDEX IF NOT EXISTS items_initials_prefix ON items(media_initials(name) text_pattern_ops);"
	})
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("SET LOCAL statement_timeout='5min'"); e != nil {
		return e
	}
	if _, e = tx.Exec(initialsSQL); e != nil {
		return e
	}
	return tx.Commit()
}

// Chinese text must use the original title; transliteration is only needed for ASCII initials.
func isInitialsQuery(s string) bool {
	for _, ch := range s {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return s != ""
}
