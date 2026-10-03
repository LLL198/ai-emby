package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var trackingChannelPattern = regexp.MustCompile(`^[A-Za-z0-9_]{5,64}$`)
var trackingLinkPattern = regexp.MustCompile(`https?://[^\s<>"']+`)
var trackingExtractPasswordPattern = regexp.MustCompile(`(?:提取码|访问码|密码)\s*[:：]\s*([A-Za-z0-9]{4,8})`)

func trackingMobileIncomplete(resource trackingResource) bool {
	if resource.Cloud != "mobile" {
		return false
	}
	u, err := url.Parse(resource.URL)
	return err == nil && (u.Hostname() == "yun.139.com" || u.Hostname() == "caiyun.139.com") && strings.Trim(u.Path, "/") == "shareweb" && u.RawQuery == "" && u.Fragment == ""
}

func trackingHTMLAttribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func trackingHTMLText(node *html.Node) string {
	var result strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			result.WriteString(n.Data)
		} else if n.Type == html.ElementNode && n.Data == "br" {
			result.WriteByte('\n')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return result.String()
}

func trackingMessageTitle(raw string) string {
	line := strings.TrimSpace(strings.SplitN(raw, "\n", 2)[0])
	for _, prefix := range []string{"名称：", "名称:", "标题：", "标题:"} {
		line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
	}
	return trackingTitleKey(line)
}

func trackingRepairFromTelegram(ctx context.Context, title string, resource trackingResource) (trackingResource, error) {
	channel := strings.TrimPrefix(resource.Source, "tg:")
	if !strings.HasPrefix(resource.Source, "tg:") || !trackingChannelPattern.MatchString(channel) {
		return resource, errors.New("移动云盘分享链接缺少分享编号，请重新搜索或补充完整链接")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint := "https://t.me/s/" + channel + "?" + url.Values{"q": {title}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return resource, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := trackingProviderHTTP.Do(request)
	if err != nil {
		return resource, errors.New("搜索结果中的移动盘链接不完整，暂时无法读取原帖，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return resource, errors.New("搜索结果中的移动盘链接不完整，原帖暂时不可用，请重新搜索或补充完整链接")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return resource, errors.New("读取资源原帖失败，请重新搜索或补充完整链接")
	}
	document, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return resource, errors.New("资源原帖格式无法识别，请补充完整链接")
	}
	published, publishedErr := time.Parse(time.RFC3339, resource.Published)
	candidates := map[string]trackingResource{}
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && trackingHTMLAttribute(node, "data-post") != "" {
			var text, timestamp string
			var links []string
			var inspect func(*html.Node)
			inspect = func(n *html.Node) {
				classes := strings.Fields(trackingHTMLAttribute(n, "class"))
				for _, class := range classes {
					if class == "tgme_widget_message_text" {
						text = trackingHTMLText(n)
						var anchors func(*html.Node)
						anchors = func(a *html.Node) {
							if a.Type == html.ElementNode && a.Data == "a" {
								links = append(links, trackingHTMLAttribute(a, "href"))
							}
							for c := a.FirstChild; c != nil; c = c.NextSibling {
								anchors(c)
							}
						}
						anchors(n)
					}
				}
				if n.Type == html.ElementNode && n.Data == "time" {
					timestamp = trackingHTMLAttribute(n, "datetime")
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					inspect(c)
				}
			}
			inspect(node)
			posted, postedErr := time.Parse(time.RFC3339, timestamp)
			if trackingMessageTitle(text) == trackingMessageTitle(resource.Title) && (publishedErr != nil || (postedErr == nil && posted.Equal(published))) {
				links = append(links, trackingLinkPattern.FindAllString(text, -1)...)
				for _, link := range links {
					candidate := resource
					candidate.URL = strings.TrimRight(link, "，。；）")
					if candidate.Password == "" {
						if match := trackingExtractPasswordPattern.FindStringSubmatch(text); len(match) > 1 {
							candidate.Password = match[1]
						}
					}
					if _, _, e := trackingShareCode(candidate, "139Yun"); e == nil {
						candidates[candidate.URL] = candidate
					}
				}
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	if len(candidates) != 1 {
		return resource, errors.New("移动盘分享链接缺少分享编号，无法从原帖唯一确认，请重新搜索或补充完整链接")
	}
	for _, candidate := range candidates {
		return candidate, nil
	}
	return resource, errors.New("资源原帖中没有完整分享链接")
}

func (a *App) trackingRepairShare(ctx context.Context, s trackingSubscription, resource trackingResource) (trackingResource, error) {
	if !trackingMobileIncomplete(resource) {
		return resource, nil
	}
	resolved, err := trackingRepairFromTelegram(ctx, s.Title, resource)
	if err != nil {
		return resource, err
	}
	fingerprint := digest(resolved.Cloud + "\x00" + resolved.Title + "\x00" + resolved.Password)
	_, err = a.db.Exec("UPDATE feature_tracking_resources SET data=?,fingerprint=?,updated=? WHERE id=? AND subscription=?", featureJSON(resolved), fingerprint, featureNow(), resource.ID, s.ID)
	if err != nil {
		return resource, errors.New("完整分享链接已找回，但保存失败，请稍后重试")
	}
	return resolved, nil
}
