package main

type episodeCounts struct {
	total    int
	unplayed int
}

// Count Series/Season roots in one query; apply results only after reading every row.
func (a *App) fillEpisodeCounts(items []Item, dtos []M, user User) {
	positions := map[string][]int{}
	args := []any{}
	for i, item := range items {
		if item.Kind != "Series" && item.Kind != "Season" {
			continue
		}
		if _, exists := positions[item.ID]; !exists {
			args = append(args, item.ID)
		}
		positions[item.ID] = append(positions[item.ID], i)
	}
	if len(args) == 0 || a.db == nil || !a.defaultOn("show_episode_count") {
		return
	}
	query := `WITH RECURSIVE episode_tree AS (
 SELECT id AS root,id,kind FROM items WHERE id IN (` + catalogPlaceholders(len(args)) + `)
 UNION ALL SELECT t.root,i.id,i.kind FROM items i JOIN episode_tree t ON i.parent=t.id
 ) SELECT t.root,count(*),count(*) FILTER (WHERE COALESCE(d.played,0)=0)
 FROM episode_tree t LEFT JOIN userdata d ON d.item=t.id AND d.user_id=?
 WHERE t.kind='Episode' GROUP BY t.root`
	args = append(args, user.ID)
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	byRoot := map[string]episodeCounts{}
	for rows.Next() {
		var root string
		var count episodeCounts
		if err := rows.Scan(&root, &count.total, &count.unplayed); err != nil {
			return
		}
		byRoot[root] = count
	}
	if rows.Err() != nil {
		return
	}
	for root, indices := range positions {
		count := byRoot[root]
		for _, i := range indices {
			dtos[i]["RecursiveItemCount"] = count.total
			if data, ok := dtos[i]["UserData"].(M); !user.API && ok {
				data["UnplayedItemCount"] = count.unplayed
			}
		}
	}
}
