package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var trackingProviderHTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
var trackingShareIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,256}$`)

type trackingNativeShare struct {
	driver, root, share, password, shareToken, cookie, accessToken, device string
	quark                                                                  *quarkSession
}

func (p *trackingNativeShare) Root() string { return p.root }
func (p *trackingNativeShare) ShareRoot() string {
	if p.driver == "Quark" {
		return "0"
	}
	return ""
}
func trackingShareCode(resource trackingResource, driver string) (string, string, error) {
	u, err := url.Parse(resource.URL)
	if err != nil {
		return "", "", errors.New("分享链接格式错误")
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	switch driver {
	case "Quark":
		allowed = host == "pan.quark.cn"
	case "115 Cloud":
		allowed = host == "115.com" || host == "www.115.com" || host == "115cdn.com" || host == "www.115cdn.com" || host == "anxia.com" || host == "www.anxia.com"
	case "GuangYaPan":
		allowed = host == "guangyapan.com" || strings.HasSuffix(host, ".guangyapan.com")
	case "139Yun":
		allowed = host == "yun.139.com" || host == "caiyun.139.com"
	}
	if !allowed {
		return "", "", errors.New("分享链接域名与所选网盘不匹配")
	}
	code := ""
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, segment := range segments {
		if (segment == "s" || segment == "share") && i+1 < len(segments) {
			code = segments[i+1]
			break
		}
	}
	query := u.Query()
	if code == "" {
		code = query.Get("shareId")
	}
	if driver == "139Yun" {
		for _, route := range []string{u.RequestURI(), u.Fragment} {
			route = strings.TrimPrefix(route, "/")
			if strings.HasPrefix(route, "w/i/") || strings.HasPrefix(route, "m/i/") {
				code = strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(route, "w/i/"), "m/i/"), "?", 2)[0]
				code = strings.SplitN(code, "&", 2)[0]
			} else if strings.HasPrefix(route, "m/i?") {
				tail := strings.TrimPrefix(route, "m/i?")
				values, _ := url.ParseQuery(tail)
				if value := values.Get("shareId"); value != "" {
					code = value
				} else if value := values.Get("linkID"); value != "" {
					code = value
				} else if first := strings.SplitN(tail, "&", 2)[0]; !strings.Contains(first, "=") {
					code = first
				}
			}
		}
		if code == "" {
			code = query.Get("linkID")
		}
	}
	if code == "" && u.Fragment != "" {
		f, e := url.Parse(strings.TrimPrefix(u.Fragment, "/"))
		if e == nil {
			code = f.Query().Get("shareId")
			parts := strings.Split(f.Path, "/")
			if len(parts) >= 2 && (parts[0] == "share" || parts[0] == "s") {
				code = parts[1]
			}
		}
	}
	if !trackingShareIDPattern.MatchString(code) {
		return "", "", errors.New("无法解析分享 ID，请使用网盘原始分享链接")
	}
	password := resource.Password
	if password == "" && strings.Contains(u.Fragment, "?") {
		_, tail, _ := strings.Cut(u.Fragment, "?")
		if extra, e := url.ParseQuery(tail); e == nil {
			for _, key := range []string{"password", "pwd", "passcode", "code", "passwd", "receive_code"} {
				if extra.Get(key) != "" {
					password = extra.Get(key)
					break
				}
			}
		}
	}
	if password == "" {
		for _, key := range []string{"password", "pwd", "passcode", "code", "receive_code"} {
			if query.Get(key) != "" {
				password = query.Get(key)
				break
			}
		}
	}
	return code, password, nil
}
func trackingOpenShare(ctx context.Context, mount cloudMount, resource trackingResource) (trackingShareProvider, error) {
	// Refresh engine credentials before reading its saved account configuration.
	if _, err := cloudList(ctx, mount, "/", 1, true); err != nil {
		return nil, err
	}
	storage, err := cloudGetStorage(ctx, mount.StorageID)
	if err != nil {
		return nil, err
	}
	var addition map[string]json.RawMessage
	if json.Unmarshal([]byte(storage.Addition), &addition) != nil {
		return nil, errCloudAccount
	}
	read := func(key string) string { var value string; _ = json.Unmarshal(addition[key], &value); return value }
	code, password, err := trackingShareCode(resource, mount.Driver)
	if err != nil {
		return nil, err
	}
	p := &trackingNativeShare{driver: mount.Driver, share: code, password: password, root: read("root_folder_id"), cookie: read("cookie"), accessToken: read("access_token"), device: read("device_id")}
	switch mount.Driver {
	case "Quark":
		if p.cookie == "" {
			return nil, errCloudAccount
		}
		if p.root == "" {
			p.root = "0"
		}
		p.quark = &quarkSession{cookie: p.cookie}
		data, err := p.quarkCall(ctx, "POST", "/share/sharepage/token", nil, M{"pwd_id": code, "passcode": password})
		if err != nil {
			return nil, err
		}
		p.shareToken = trackingString(data, "stoken")
	case "115 Cloud":
		if p.cookie == "" {
			return nil, errCloudAccount
		}
		if p.root == "" {
			p.root = "0"
		}
	case "GuangYaPan":
		if p.accessToken == "" {
			return nil, errCloudAccount
		}
		data, err := p.guangyaCall(ctx, "/userres/v1/get_share_access_token", M{"shareId": code, "code": password})
		if err != nil {
			return nil, err
		}
		p.shareToken = trackingString(data, "accessToken")
		p.root = ""
		rootPath := read("root_path")
		if rootPath != "" {
			// The configured mount root must already exist; never create it in another account root.
			for _, name := range strings.Split(strings.Trim(strings.ReplaceAll(rootPath, "\\", "/"), "/"), "/") {
				if name == "" {
					continue
				}
				list, e := p.List(ctx, p.root, false)
				if e != nil {
					return nil, e
				}
				found := false
				for _, f := range list {
					if f.Dir && f.Name == name {
						p.root, found = f.ID, true
						break
					}
				}
				if !found {
					return nil, errors.New("光鸭挂载根目录不存在，请检查网盘配置")
				}
			}
		}
	case "139Yun":
		return trackingOpenMobile(ctx, mount, resource, storage.Addition)
	default:
		return nil, errors.New("此网盘暂不支持分享转存")
	}
	if (p.driver == "Quark" || p.driver == "GuangYaPan") && p.shareToken == "" {
		return nil, errors.New("分享未返回访问凭据，请检查提取码、有效期与分享权限")
	}
	return p, nil
}
func trackingString(object map[string]json.RawMessage, key string) string {
	raw := object[key]
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}
func trackingNumber(object map[string]json.RawMessage, key string) int64 {
	n, _ := strconv.ParseInt(trackingString(object, key), 10, 64)
	return n
}
func trackingObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || data == nil {
		return nil, errors.New("网盘返回的数据格式无效")
	}
	return data, nil
}
func (p *trackingNativeShare) quarkCall(ctx context.Context, method, endpoint string, query url.Values, payload any) (map[string]json.RawMessage, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("pr", "ucpro")
	query.Set("fr", "pc")
	result, err := p.quark.request(ctx, method, "https://drive.quark.cn/1/clouddrive"+endpoint+"?"+query.Encode(), payload)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if result.Status == 0 && result.Code == 0 {
			return nil, err
		}
		return nil, fmt.Errorf("夸克分享转存失败，请检查 Cookie、提取码、分享有效期和网盘空间（接口状态 %d/%d）", result.Status, result.Code)
	}
	return trackingObject(result.Data)
}
func trackingJSONRequest(ctx context.Context, method, endpoint string, payload []byte, headers map[string]string) (map[string]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("网盘请求格式无效")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := trackingProviderHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("网盘接口暂时无法连接")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("网盘返回 HTTP %d，请检查账号权限或稍后重试", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(content) > 8<<20 {
		return nil, errors.New("网盘响应过大或读取失败")
	}
	return trackingObject(content)
}
func (p *trackingNativeShare) pan115Call(ctx context.Context, method, endpoint string, query url.Values) (map[string]json.RawMessage, error) {
	var body []byte
	if method == "GET" {
		endpoint += "?" + query.Encode()
	} else {
		body = []byte(query.Encode())
	}
	result, err := trackingJSONRequest(ctx, method, "https://webapi.115.com"+endpoint, body, map[string]string{"Cookie": p.cookie, "User-Agent": "Mozilla/5.0", "Referer": "https://115.com/", "Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return nil, err
	}
	var ok bool
	_ = json.Unmarshal(result["state"], &ok)
	if !ok {
		return nil, errors.New("115 分享转存失败，请检查 Cookie、提取码、分享权限及网盘空间")
	}
	return result, nil
}
func (p *trackingNativeShare) guangyaCall(ctx context.Context, endpoint string, payload any) (map[string]json.RawMessage, error) {
	if err := trackingPause(ctx, 500*time.Millisecond); err != nil {
		return nil, err
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	result, err := trackingJSONRequest(ctx, "POST", "https://api.guangyapan.com"+endpoint, content, map[string]string{"Authorization": "Bearer " + p.accessToken, "Did": p.device, "Dt": "4", "Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	code, message := trackingNumber(result, "code"), trackingString(result, "msg")
	if (code != 0 && code != 200) || (message != "" && !strings.EqualFold(message, "success")) {
		return nil, fmt.Errorf("光鸭分享转存失败（代码 %d），请检查账号、提取码、有效期和网盘空间", code)
	}
	return trackingObject(result["data"])
}
func (p *trackingNativeShare) List(ctx context.Context, parent string, share bool) ([]trackingShareFile, error) {
	all := []trackingShareFile{}
	for page := 0; page < 1000; page++ {
		var rows []map[string]json.RawMessage
		pageSize := 100
		switch p.driver {
		case "Quark":
			endpoint := "/file/sort"
			query := url.Values{"pdir_fid": {parent}, "_page": {strconv.Itoa(page + 1)}, "_size": {"100"}, "_fetch_total": {"1"}, "fetch_all_file": {"1"}, "fetch_risk_file_name": {"1"}}
			if share {
				pageSize = 50
				query.Set("_size", "50")
				endpoint = "/share/sharepage/detail"
				query.Set("pwd_id", p.share)
				query.Set("stoken", p.shareToken)
				query.Set("force", "0")
			}
			data, err := p.quarkCall(ctx, "GET", endpoint, query, nil)
			if err != nil {
				return nil, err
			}
			if json.Unmarshal(data["list"], &rows) != nil {
				return nil, errors.New("夸克目录数据无效")
			}
			for _, r := range rows {
				var file bool
				_ = json.Unmarshal(r["file"], &file)
				all = append(all, trackingShareFile{ID: trackingString(r, "fid"), Name: html.UnescapeString(trackingString(r, "file_name")), Size: trackingNumber(r, "size"), Dir: !file, Token: trackingString(r, "share_fid_token"), Parent: parent})
			}
		case "115 Cloud":
			endpoint := "/files"
			query := url.Values{"aid": {"1"}, "cid": {parent}, "offset": {strconv.Itoa(page * 100)}, "limit": {"100"}, "show_dir": {"1"}, "type": {"0"}, "format": {"json"}, "o": {"file_name"}, "asc": {"1"}, "fc_mix": {"0"}}
			if share {
				endpoint = "/share/snap"
				query.Set("share_code", p.share)
				query.Set("receive_code", p.password)
			}
			result, err := p.pan115Call(ctx, "GET", endpoint, query)
			if err != nil {
				return nil, err
			}
			raw := result["data"]
			if share {
				data, e := trackingObject(raw)
				if e != nil {
					return nil, e
				}
				raw = data["list"]
			}
			if json.Unmarshal(raw, &rows) != nil {
				return nil, errors.New("115 目录数据无效")
			}
			for _, r := range rows {
				fid := trackingString(r, "fid")
				folder := fid == "" || fid == "0"
				if folder {
					fid = trackingString(r, "cid")
				}
				all = append(all, trackingShareFile{ID: fid, Name: trackingString(r, "n"), Size: trackingNumber(r, "s"), Dir: folder, Parent: parent})
			}
		case "GuangYaPan":
			endpoint := "/userres/v1/file/get_file_list"
			payload := M{"parentId": parent, "page": page, "pageSize": 100, "orderBy": 0, "sortType": 0}
			if share {
				endpoint = "/userres/v1/get_share_page_files_list"
				payload = M{"accessToken": p.shareToken, "parent_id": parent, "page": page, "pageSize": 100, "orderType": 0, "sortType": 0}
			}
			data, err := p.guangyaCall(ctx, endpoint, payload)
			if err != nil {
				return nil, err
			}
			if json.Unmarshal(data["list"], &rows) != nil {
				return nil, errors.New("光鸭目录数据无效")
			}
			for _, r := range rows {
				all = append(all, trackingShareFile{ID: trackingString(r, "fileId"), Name: trackingString(r, "fileName"), Size: trackingNumber(r, "fileSize"), Dir: trackingNumber(r, "resType") == 2, Parent: parent})
			}
		}
		if len(rows) < pageSize {
			return all, nil
		}
	}
	return nil, errors.New("网盘目录文件过多，请缩小目录范围")
}
func (p *trackingNativeShare) Mkdir(ctx context.Context, parent, name string) (string, error) {
	var err error
	switch p.driver {
	case "Quark":
		_, err = p.quarkCall(ctx, "POST", "/file", nil, M{"pdir_fid": parent, "file_name": name, "dir_path": "", "dir_init_lock": false})
	case "115 Cloud":
		_, err = p.pan115Call(ctx, "POST", "/files/add", url.Values{"pid": {parent}, "cname": {name}})
	case "GuangYaPan":
		_, err = p.guangyaCall(ctx, "/nd.bizuserres.s/v1/file/create_dir", M{"parentId": parent, "dirName": name})
	}
	if err != nil {
		return "", err
	}
	list, err := p.List(ctx, parent, false)
	if err != nil {
		return "", err
	}
	for _, f := range list {
		if f.Dir && f.Name == name {
			return f.ID, nil
		}
	}
	return "", errors.New("新建的网盘目录尚不可见，请稍后重试")
}
func (p *trackingNativeShare) Save(ctx context.Context, parent string, files []trackingShareFile) (string, error) {
	ids, tokens := []string{}, []string{}
	for _, f := range files {
		ids = append(ids, f.ID)
		tokens = append(tokens, f.Token)
	}
	switch p.driver {
	case "Quark":
		for _, token := range tokens {
			if token == "" {
				return "", errors.New("夸克分享缺少文件转存凭据")
			}
		}
		data, err := p.quarkCall(ctx, "POST", "/share/sharepage/save", nil, M{"fid_list": ids, "fid_token_list": tokens, "to_pdir_fid": parent, "pwd_id": p.share, "stoken": p.shareToken, "pdir_fid": files[0].Parent, "scene": "link"})
		if err != nil {
			return "", err
		}
		task := trackingString(data, "task_id")
		if task == "" {
			return "", errors.New("夸克未返回转存任务 ID，请稍后重新核对目录")
		}
		return task, nil
	case "115 Cloud":
		_, err := p.pan115Call(ctx, "POST", "/share/receive", url.Values{"cid": {parent}, "share_code": {p.share}, "receive_code": {p.password}, "file_id": {strings.Join(ids, ",")}})
		return "", err
	case "GuangYaPan":
		data, err := p.guangyaCall(ctx, "/userres/v1/restore_share", M{"accessToken": p.shareToken, "fileIds": ids, "parentId": parent})
		if err != nil {
			return "", err
		}
		task := trackingString(data, "taskId")
		if task == "" {
			return "", errors.New("光鸭未返回转存任务 ID，请稍后重新核对目录")
		}
		return task, nil
	}
	return "", errors.New("网盘转存类型不受支持")
}
func (p *trackingNativeShare) Wait(ctx context.Context, task string) error {
	for attempt := 0; attempt < 120; attempt++ {
		var data map[string]json.RawMessage
		var err error
		if p.driver == "Quark" {
			data, err = p.quarkCall(ctx, "GET", "/task", url.Values{"task_id": {task}, "retry_index": {strconv.Itoa(attempt)}}, nil)
		} else {
			data, err = p.guangyaCall(ctx, "/nd.bizuserres.s/v1/get_task_status", M{"taskId": task})
		}
		if err != nil {
			return err
		}
		status := trackingNumber(data, "status")
		if status == 2 {
			return nil
		}
		if status == -1 || status == 3 {
			return errTrackingTransferFailed
		}
		if err = trackingPause(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("网盘仍在处理转存，下次追新会继续确认此任务")
}
