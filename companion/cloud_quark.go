package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const quarkDesktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"

var quarkHTTP = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

func cloudQuarkMobileURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 32768 || u.Scheme != "https" || u.Host != "drive-m.quark.cn" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/1/clouddrive/") || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("请填写自己账号的 https://drive-m.quark.cn/1/clouddrive/… 移动端请求 URL")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("移动端请求 URL 的参数格式错误")
	}
	for _, key := range []string{"kps", "sign", "vcode", "ut"} {
		if len(query[key]) != 1 || query.Get(key) == "" {
			return "", errors.New("移动端请求 URL 缺少完整的 kps、sign、vcode、ut 参数，请重新获取")
		}
	}
	// Keep device and authentication parameters; omit parameters for the captured operation.
	for _, key := range []string{"fid", "fids", "pdir_fid", "share_id", "share_token", "share_fid_token", "resolutions", "supports", "_page", "_size", "_fetch_total", "fetch_all_file", "fetch_risk_file_name", "_sort", "_t"} {
		query.Del(key)
	}
	if query.Get("pr") == "" {
		query.Set("pr", "ucpro")
	}
	u.Path, u.RawPath, u.RawQuery = "/1/clouddrive/", "", query.Encode()
	return u.String(), nil
}

type quarkResponse struct {
	Status   int             `json:"status"`
	Code     int             `json:"code"`
	Message  string          `json:"message"`
	Data     json.RawMessage `json:"data"`
	Metadata struct {
		Total int `json:"_total"`
	} `json:"metadata"`
}

type quarkSession struct{ cookie string }

func (session *quarkSession) request(ctx context.Context, method, endpoint string, payload any) (quarkResponse, error) {
	var result quarkResponse
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return result, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("夸克取链请求无效")
	}
	req.Header.Set("Cookie", session.cookie)
	req.Header.Set("User-Agent", quarkDesktopUA)
	req.Header.Set("Referer", "https://pan.quark.cn/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	resp, err := quarkHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, errors.New("夸克取直链已取消或超时，请重试")
		}
		return result, errors.New("无法连接夸克取链接口，请稍后重试")
	}
	defer resp.Body.Close()
	for _, next := range resp.Cookies() {
		if next.Name != "__puus" && next.Name != "__pus" {
			continue
		}
		parts := []string{}
		for _, part := range strings.Split(session.cookie, ";") {
			if strings.TrimSpace(strings.SplitN(part, "=", 2)[0]) != next.Name && strings.TrimSpace(part) != "" {
				parts = append(parts, strings.TrimSpace(part))
			}
		}
		parts = append(parts, next.Name+"="+next.Value)
		session.cookie = strings.Join(parts, "; ")
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result); err != nil {
		return result, errors.New("夸克接口未返回有效数据，请稍后重试")
	}
	if resp.StatusCode != 200 || result.Status >= 400 || result.Code != 0 {
		message := strings.ToLower(result.Message)
		switch {
		case strings.Contains(message, "plf_invalid"):
			return result, errors.New("夸克仍拒绝移动端取链（plf_invalid），请重新获取自己账号的移动端请求 URL")
		case strings.Contains(message, "token"), strings.Contains(message, "login"), strings.Contains(message, "登录"), strings.Contains(message, "invalid"):
			return result, errors.New("夸克移动端凭据或 Cookie 已失效，请在网盘配置中更新")
		case resp.StatusCode == 429, strings.Contains(message, "频繁"), strings.Contains(message, "rate limit"):
			return result, errors.New("夸克请求过于频繁，请稍后重试")
		default:
			return result, errors.New("夸克取链失败，请检查账号、会员权限和移动端请求 URL")
		}
	}
	return result, nil
}

func (session *quarkSession) fileID(ctx context.Context, rootID, cloudPath string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(cloudPath, "/"), "/")
	if len(parts) > 64 {
		return "", errors.New("网盘目录层级过深")
	}
	parent := rootID
	for i, name := range parts {
		found := false
		for page := 1; page <= 1000; page++ {
			query := url.Values{"pr": {"ucpro"}, "fr": {"pc"}, "pdir_fid": {parent}, "_page": {strconv.Itoa(page)}, "_size": {"100"}, "_fetch_total": {"1"}, "fetch_all_file": {"1"}, "fetch_risk_file_name": {"1"}}
			result, err := session.request(ctx, "GET", "https://drive.quark.cn/1/clouddrive/file/sort?"+query.Encode(), nil)
			if err != nil {
				return "", err
			}
			var data struct {
				List []struct {
					ID   string `json:"fid"`
					Name string `json:"file_name"`
					File bool   `json:"file"`
				} `json:"list"`
			}
			if json.Unmarshal(result.Data, &data) != nil {
				return "", errors.New("夸克目录数据无效")
			}
			for _, item := range data.List {
				if html.UnescapeString(item.Name) == name && item.File == (i == len(parts)-1) {
					if item.ID == "" {
						return "", errors.New("夸克文件 ID 无效")
					}
					parent, found = item.ID, true
					break
				}
			}
			if found || len(data.List) == 0 || page*100 >= result.Metadata.Total {
				break
			}
		}
		if !found {
			return "", errors.New("夸克目录或文件已移除、改名，或当前根目录不包含该文件")
		}
	}
	return parent, nil
}

func cloudQuarkMobileLink(ctx context.Context, mount cloudMount, cloudPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	storage, err := cloudGetStorage(ctx, mount.StorageID)
	if err != nil {
		return "", err
	}
	var addition struct {
		Cookie string `json:"cookie"`
		RootID string `json:"root_folder_id"`
	}
	if json.Unmarshal([]byte(storage.Addition), &addition) != nil || addition.Cookie == "" {
		return "", errCloudAccount
	}
	if addition.RootID == "" {
		addition.RootID = "0"
	}
	session := quarkSession{cookie: addition.Cookie}
	fid, err := session.fileID(ctx, addition.RootID, cloudPath)
	if err != nil {
		return "", err
	}
	raw, err := cloudQuarkMobileURL(mount.MobileURL)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(raw)
	u.Path = "/1/clouddrive/file/v2/play/project"
	response, err := session.request(ctx, "POST", u.String(), M{"fid": fid, "resolutions": "low,normal,high,super,2k,4k", "supports": "fmp4_av,m3u8,dolby_vision"})
	if err != nil {
		return "", err
	}
	var data struct {
		Videos []struct {
			Resolution string `json:"resolution"`
			Info       struct {
				URL string `json:"url"`
			} `json:"video_info"`
		} `json:"video_list"`
	}
	if json.Unmarshal(response.Data, &data) != nil {
		return "", errors.New("夸克移动端播放数据无效")
	}
	var best string
	rank := -1
	quality := map[string]int{"low": 0, "normal": 1, "high": 2, "super": 3, "2k": 4, "4k": 5}
	for _, video := range data.Videos {
		if video.Info.URL == "" {
			continue
		}
		n := quality[video.Resolution]
		if best == "" || n > rank {
			best, rank = video.Info.URL, n
		}
	}
	if best == "" {
		return "", errors.New("夸克未返回可播放的转码直链，可能仍在转码或没有视频播放权限")
	}
	return best, nil
}
