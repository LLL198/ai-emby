let resumeMenuAbort = new AbortController();
let browseGeneration = 0,
  wallCursors = [""],
  wallKey = "",
  browseType = "",
  browseTitle = "",
  browseKind = "",
  browseSeries = "",
  librarySort = {key:"DateCreated", order:"Descending", seed:""},
  browseTrail = [];
const listParams = {
  Fields: "PrimaryImageAspectRatio,CommunityRating,RunTimeTicks",
  Limit: 18,
  Recursive: true,
  EnableTotalRecordCount: false,
  SortBy: "DateCreated",
  SortOrder: "Descending",
};
async function browseRoot() {
  if (location.hash !== '#browse') history.replaceState({page:'browse'}, '', '#browse');
  resumeMenuAbort.abort();
  resumeMenuAbort = new AbortController();
  page = 0;
  parent = "";
  term = "";
  browseType = "";
  browseKind = "";
  browseSeries = "";
  browseTitle = "";
  librarySort = {key:"DateCreated", order:"Descending", seed:""};
  browseTrail = [];
  await browse();
}
async function browse() {
  view = "browse";
  nav();
  const generation = ++browseGeneration;
  const app = $("#app");
  app.replaceChildren();
  const heroSlot = UI.el("div", {class:"featured-slot", "aria-busy":"true"});
  app.append(heroSlot);
  const alive = () => generation === browseGeneration && view === "browse";
  const titles = ["继续观看", "最近添加", "我的媒体库"];
  const holders = titles.map(title => UI.el("section", {class:"shelf-section"}, [UI.el("h2", {}, title), UI.LoadingSkeleton()]));
  const categories = UI.el("div", {class:"library-categories"});
  app.append(holders[0], holders[1], categories, holders[2]);
  const failure = (host, title, error) => host.replaceChildren(UI.el("h2", {}, title), UI.el("p", {class:"muted"}, "加载失败：" + error.message), UI.el("button", {class:"secondary", onclick:run(browse)}, "重试"));
  await Promise.allSettled([
    (async () => {
      try {
        const result = await api.getResume();
        if (!alive()) return;
        const items = await Promise.all((result.Items||[]).map(async item => {
          if (item.Type !== 'Episode' || !item.UserData?.Played || !item.SeriesId) return item;
          try { return (await api.getNextUp(item.SeriesId)).Items?.[0] || item; }
          catch { return item; }
        }));
        if (!alive()) return;
        if (items.length) holders[0].replaceWith(resumeShelf(items, openMedia));
        else holders[0].remove();
      } catch (e) { if (alive()) failure(holders[0], titles[0], e); }
    })(),
    (async () => {
      try {
        const items = await api.getLatest({Limit:18, IncludeItemTypes:"Movie,Series", Fields:listParams.Fields});
        if (!alive()) return;
        const featured = items.filter(x => ["Movie", "Series"].includes(x.Type)).slice(0, 5);
        if (featured.length) heroSlot.replaceWith(UI.FeaturedCarousel(featured));
        else heroSlot.remove();
        holders[1].replaceWith(UI.MediaShelf(titles[1], items, openMedia, () => openCollection("", titles[1], "Movie,Series")));
        bindPosterMenus();
      } catch (e) { if (alive()) { heroSlot.remove(); failure(holders[1], titles[1], e); } }
    })(),
    (async () => {
      try {
        const result = await api.getViews();
        if (!alive()) return;
        const libraries = result.Items || [];
        holders[2].replaceWith(UI.MediaShelf(titles[2], libraries, openMedia, null, true));
        await Promise.allSettled(libraries.slice(0, 4).map(async library => {
          const host = UI.el("section", {class:"shelf-section"}, [UI.el("h2", {}, library.Name), UI.LoadingSkeleton()]);
          categories.append(host);
          try {
            const result = await api.getItems({...listParams, ParentId:library.Id, IncludeItemTypes:"Movie,Series"});
            if (!alive()) return;
            host.replaceWith(UI.MediaShelf(library.Name, result.Items || [], openMedia, () => openMedia(library)));
            bindPosterMenus();
          } catch (e) { if (alive()) failure(host, library.Name, e); }
        }));
      } catch (e) { if (alive()) failure(holders[2], titles[2], e); }
    })(),
  ]);
  if (alive()) bindPosterMenus();
}
async function openMedia(item, browseFolder = false) {
  if (item.Type === "Series" && !browseFolder) return detail(item.Id);
  if (
    item.IsFolder ||
    ["Series", "Season", "CollectionFolder", "Folder"].includes(item.Type)
  ) {
    browseTrail.push({
      parent,
      title: browseTitle,
      type: browseType,
      kind: browseKind,
      series: browseSeries,
      term,
      page,
      sort: {...librarySort},
    });
    await openCollection(
      item.Id,
      item.Name,
      "",
      false,
      item.Type,
      item.ParentId,
    );
  } else await detail(item.Id);
}
async function openCollection(
  id,
  title,
  type = "",
  reset = true,
  kind = "",
  series = "",
) {
  if (reset) browseTrail = [];
  parent = id;
  browseTitle = title;
  browseType = type;
  browseKind = kind;
  browseSeries = series;
  page = 0;
  term = "";
  if (kind === "CollectionFolder") librarySort = {key:"DateCreated", order:"Descending", seed:""};
  await loadWall();
}
function openSearch() {
  UI.SearchOverlay(async (value) => {
    browseTrail = [];
    parent = "";
    browseKind = "";
    browseSeries = "";
    browseType = "Movie,Series";
    browseTitle = "搜索结果";
    term = value;
    page = 0;
    await loadWall();
  });
}
async function browseBack() {
  const previous = browseTrail.pop();
  if (!previous) return browseRoot();
  ({
    parent,
    title: browseTitle,
    type: browseType,
    kind: browseKind,
    series: browseSeries,
    term,
    page,
    sort,
  } = previous);
  if (sort) librarySort = sort;
  await loadWall();
}
async function refreshBrowse() {
  if (view !== "browse") return;
  if (!parent && !term && !browseType) return browse();
  return loadWall();
}
function librarySortControl() {
  const options = [
    ["DateCreated","添加日期"], ["PremiereDate","发行日期"], ["ProductionYear","年份"],
    ["CommunityRating","TMDB评分"], ["CriticRating","影评人评分"], ["DatePlayed","播放日期"],
    ["SeriesDateLastContentAdded","最后一集添加日期"], ["SeriesDateLastReleased","最后一集发行日期"],
    ["PlayCount","播放次数"], ["Random","随机"], ["OfficialRating","分级"],
  ];
  const details = UI.el("details", {class:"library-sort"});
  const summary = UI.el("summary", {class:"library-sort-trigger", title:"排序", "aria-label":"选择排序方式"});
  summary.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 4v16M4 8l4-4 4 4M16 20V4m-4 12 4 4 4-4"/></svg><span>排序</span>';
  const menu = UI.el("div", {class:"library-sort-menu", role:"menu"});
  for (const [key,label] of options) {
    const selected = librarySort.key === key;
    const button = UI.el("button", {type:"button", role:"menuitemradio", "aria-checked":String(selected), class:selected?"is-selected":""});
    const direction = librarySort.order === "Ascending" ? "升序" : "降序";
    button.innerHTML = `<span>${label}</span>${selected?`<svg class="library-sort-direction" data-order="${librarySort.order}" viewBox="0 0 24 24" aria-label="${direction}"><path d="M6 15l6-6 6 6"/></svg>`:""}`;
    button.onclick = run(async()=>{
      if (selected) librarySort.order = librarySort.order === "Ascending" ? "Descending" : "Ascending";
      else {
        librarySort.key = key;
        librarySort.order = ["DateCreated","PremiereDate","CommunityRating","CriticRating","DatePlayed","SeriesDateLastContentAdded","SeriesDateLastReleased","PlayCount","Random"].includes(key) ? "Descending" : "Ascending";
        librarySort.seed = key === "Random" ? `${parent}-${Date.now()}-${Math.random()}` : "";
      }
      page = 0; wallCursors = [""]; wallKey = ""; details.open = false;
      await loadWall();
    });
    menu.append(button);
  }
  details.append(summary, menu);
  return details;
}

async function loadWall() {
  view = "browse";
  nav();
  const generation = ++browseGeneration;
  const key = [parent, term, browseType, browseKind === "CollectionFolder" ? librarySort.key : "", browseKind === "CollectionFolder" ? librarySort.order : "", librarySort.key === "Random" ? librarySort.seed : ""].join("|");
  if (page === 0 || key !== wallKey) {
    wallCursors = [""];
    wallKey = key;
  }
  const app = $("#app");
  const libraryWall = browseKind === "CollectionFolder" && !!parent && !term;
  const headingActions = UI.el("div", {class:"library-heading-actions"}, [
    UI.el("button", {class:"text-button", onclick:run(browseRoot)}, "首页"),
    ...(libraryWall ? [librarySortControl()] : []),
  ]);
  app.replaceChildren(
    UI.el("div", { class: "section-heading library-wall-heading" }, [
      UI.IconButton("返回", "back", run(browseBack)),
      UI.el("h2", {}, term ? `搜索：${term}` : browseTitle || "媒体"),
      headingActions,
    ]),
    UI.LoadingSkeleton(),
  );
  const params = {
    Fields: "PrimaryImageAspectRatio,CommunityRating,RunTimeTicks",
    Limit: 60,
    Cursor: wallCursors[page] || "",
    EnableTotalRecordCount: false,
    ParentId: parent,
  };
  if (browseType) params.IncludeItemTypes = browseType;
  if (libraryWall) {
    params.SortBy = librarySort.key;
    params.SortOrder = librarySort.order;
    if (librarySort.key === "Random") params.RandomSeed = librarySort.seed || (librarySort.seed = `${parent}-${Date.now()}-${Math.random()}`);
  }
  if (!parent) {
    params.Recursive = true;
    params.SortBy = "DateCreated";
    params.SortOrder = "Descending";
    if (!browseType) params.IncludeItemTypes = "Movie,Series";
  }
  const b = await (term
    ? api.search(term, params)
    : browseKind === "Series"
      ? api.getSeasons(parent, params)
      : browseKind === "Season"
        ? api.getEpisodes(browseSeries || parent, {
            ...params,
            SeasonId: parent,
          })
        : api.getItems(params));
  if (generation !== browseGeneration || view !== "browse") return;
  wallCursors[page + 1] = b.NextCursor || "";
  app.querySelector("[role=status]")?.remove();
  app.append(
    UI.el(
      "div",
      { id: "wall", class: "poster-grid" },
      b.Items.length
        ? b.Items.map((x) => UI.PosterCard(x, openMedia))
        : [UI.el("p", { class: "empty" }, "暂无媒体")],
    ),
  );
  const prev = UI.el(
      "button",
      {
        class: "secondary",
        onclick: run(async () => {
          page--;
          await loadWall();
        }),
      },
      "上一页",
    ),
    next = UI.el(
      "button",
      {
        class: "secondary",
        onclick: run(async () => {
          page++;
          await loadWall();
        }),
      },
      "下一页",
    );
  prev.disabled = page === 0;
  next.disabled = !b.HasMore;
  app.append(
    UI.el("div", { class: "paging" }, [
      prev,
      UI.el("span", { class: "muted" }, `第 ${page + 1} 页`),
      next,
    ]),
  );
  bindPosterMenus();
}

function resumeShelf(items, open) {
  const section = UI.MediaShelf("继续观看", items, open);
  section.querySelectorAll('.card').forEach(card => {
    card.dataset.resumeCard = "true";
    bindResumeMenu(card, run(() => posterMenu(card, [
      {label:"删除继续播放记录", danger:true, action:async () => {
        if (!await confirmDialog("删除继续播放记录？", "只清除当前用户的继续播放状态，不会删除视频文件。", "删除记录")) return;
        await api.deleteResume(card.dataset.id);
        card.remove();
        if (!section.querySelector('.card')) section.remove();
        toast("已删除继续播放记录");
      }},
      {label:"删除所有继续播放记录", danger:true, action:async () => {
        if (!await confirmDialog("删除所有继续播放记录？", "只清除当前用户的全部继续播放状态，不会删除视频文件，也不会影响其他用户。", "删除所有记录")) return;
        await api.deleteAllResume();
        section.remove();
        toast("已删除所有继续播放记录");
      }},
    ])));
  });
  return section;
}

function bindResumeMenu(card, show) {
  let timer = null, pointerId = null, start = null, held = false;
  const cancel = () => { clearTimeout(timer); timer = null; pointerId = null; start = null; };
  const open = () => {
    if (!card.isConnected || !timer) { cancel(); return; }
    cancel(); held = true; show();
  };
  card.addEventListener("pointerdown", e => {
    if (!e.isPrimary || e.button !== 0) { if (e.button === 2) held = false; return; }
    cancel(); held = false; pointerId = e.pointerId;
    start = {x:e.clientX, y:e.clientY};
    timer = setTimeout(open, 550);
  });
  card.addEventListener("pointermove", e => {
    if (e.pointerId === pointerId && start &&
        Math.hypot(e.clientX-start.x, e.clientY-start.y) > 10) cancel();
  });
  for (const event of ["pointerup","pointercancel","pointerleave","lostpointercapture"])
    card.addEventListener(event, e => { if (e.pointerId === pointerId) cancel(); });
  document.addEventListener("scroll", cancel, {capture:true, passive:true, signal:resumeMenuAbort.signal});
  card.addEventListener("click", e => {
    if (held) { e.preventDefault(); e.stopImmediatePropagation(); held = false; }
  }, true);
  card.addEventListener("contextmenu", e => {
    e.preventDefault();
    if (held) return;
    if (!document.querySelector("dialog.bottom-sheet[open]")) {
      cancel(); held = true; show();
    } else cancel();
  });
  card.addEventListener("keydown", e => {
    if (e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) {
      e.preventDefault(); cancel(); held = true; show();
    }
  });
}
