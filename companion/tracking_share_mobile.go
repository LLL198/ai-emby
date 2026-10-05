package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type trackingMobileShare struct {
	auth, account, root, host, share, password, owner string
	protected                                         bool
}

func (p *trackingMobileShare) Root() string      { return p.root }
func (p *trackingMobileShare) ShareRoot() string { return "root" }
func trackingOpenMobile(ctx context.Context, m cloudMount, resource trackingResource, raw string) (trackingShareProvider, error) {
	var config struct {
		Authorization string `json:"authorization"`
		Root          string `json:"root_folder_id"`
		Type          string `json:"type"`
	}
	if json.Unmarshal([]byte(raw), &config) != nil {
		return nil, errCloudAccount
	}
	if config.Type != "" && config.Type != "personal_new" {
		return nil, errors.New("移动盘自动转存需要个人云（personal_new）挂载，家庭云、群组和只读分享暂不支持")
	}
	auth := strings.TrimSpace(strings.TrimPrefix(config.Authorization, "Basic "))
	decoded, err := base64.StdEncoding.DecodeString(auth)
	parts := strings.Split(string(decoded), ":")
	if err != nil || len(parts) < 3 || parts[1] == "" {
		return nil, errors.New("移动盘 Authorization 未保存或已失效，请重新登录挂载")
	}
	share, password, err := trackingShareCode(resource, m.Driver)
	if err != nil {
		return nil, err
	}
	p := &trackingMobileShare{auth: auth, account: parts[1], root: config.Root, share: share, password: password}
	if p.root == "" {
		p.root = "/"
	}
	data, err := p.personalRequest(ctx, "https://user-njs.yun.139.com/user/route/qryRoutePolicy", M{"userInfo": M{"userType": 1, "accountType": 1, "accountName": p.account}, "modAddrType": 1})
	if err != nil {
		return nil, err
	}
	var routes []struct {
		ModName string `json:"modName"`
		HTTPS   string `json:"httpsUrl"`
	}
	if json.Unmarshal(data["routePolicyList"], &routes) != nil {
		return nil, errors.New("移动盘未返回个人云路由")
	}
	for _, route := range routes {
		if route.ModName != "personal" {
			continue
		}
		u, e := url.Parse(route.HTTPS)
		if e == nil && u.Scheme == "https" && strings.HasSuffix(u.Hostname(), ".yun.139.com") && u.User == nil {
			p.host = strings.TrimRight(route.HTTPS, "/")
			break
		}
	}
	if p.host == "" {
		return nil, errors.New("移动盘个人云路由无效")
	}
	data, err = p.shareRequest(ctx, "/richlifeApp/devapp/IOutLink/getOutLinkGeneral", M{"getOutLinkGeneralReq": M{"linkID": p.share, "isPasswd": 1}})
	if err != nil {
		return nil, err
	}
	p.protected = trackingNumber(data, "isPasswd") == 1
	general, e := trackingObject(data["getOutLinkGeneralResp"])
	if e == nil {
		var links []map[string]json.RawMessage
		_ = json.Unmarshal(general["outLinkGeneral"], &links)
		if len(links) > 0 {
			p.owner = trackingString(links[0], "ownerUserId")
		}
	}
	if p.owner == "" {
		return nil, errors.New("移动盘分享未返回分享者信息，无法安全转存")
	}
	return p, nil
}
func trackingMobileMD5(raw string) string {
	sum := md5.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func trackingMobileSign(body, timestamp, nonce string) string {
	encoded := strings.ReplaceAll(url.QueryEscape(body), "+", "%20")
	for _, pair := range [][2]string{{"%21", "!"}, {"%27", "'"}, {"%28", "("}, {"%29", ")"}, {"%2A", "*"}} {
		encoded = strings.ReplaceAll(encoded, pair[0], pair[1])
	}
	characters := strings.Split(encoded, "")
	sort.Strings(characters)
	encoded = base64.StdEncoding.EncodeToString([]byte(strings.Join(characters, "")))
	return strings.ToUpper(trackingMobileMD5(trackingMobileMD5(encoded) + trackingMobileMD5(timestamp+":"+nonce)))
}
func (p *trackingMobileShare) headers() map[string]string {
	return map[string]string{"Authorization": "Basic " + p.auth, "Content-Type": "application/json;charset=UTF-8", "Accept": "application/json, text/plain, */*", "User-Agent": "Mozilla/5.0", "Origin": "https://yun.139.com", "Referer": "https://yun.139.com/", "CMS-DEVICE": "default", "x-m4c-caller": "PC", "X-Yun-Api-Version": "v1", "x-DeviceInfo": "||9|12.27.0|chrome|136.0.0.0|||windows 10||zh-CN|||"}
}
func trackingMobileData(result map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	var success bool
	_ = json.Unmarshal(result["success"], &success)
	code := trackingString(result, "resultCode")
	if code == "" {
		code = trackingString(result, "code")
	}
	if code != "" && code != "0" && code != "0000" {
		if len(code) <= 32 && trackingShareIDPattern.MatchString(code) {
			return nil, fmt.Errorf("移动盘接口拒绝请求（状态码 %s），请检查登录状态、提取码、分享权限与网盘空间", code)
		}
		return nil, errors.New("移动盘接口拒绝请求，请检查登录状态、提取码、分享权限与网盘空间")
	}
	if code == "" && !success {
		return nil, errors.New("移动盘接口未确认请求成功")
	}
	data, err := trackingObject(result["data"])
	if err != nil {
		return nil, errors.New("移动盘接口未返回有效数据，请稍后重试")
	}
	return data, nil
}
func (p *trackingMobileShare) signedHeaders(body []byte) (map[string]string, error) {
	nonceBytes := make([]byte, 8)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	zone := time.FixedZone("CST", 8*60*60)
	timestamp := time.Now().In(zone).Format("2006-01-02 15:04:05")
	h := p.headers()
	for key, value := range map[string]string{"Caller": "web", "Mcloud-Channel": "1000101", "Mcloud-Client": "10701", "Mcloud-Route": "001", "Mcloud-Version": "12.27.0", "Mcloud-Sign": timestamp + "," + nonce + "," + trackingMobileSign(string(body), timestamp, nonce), "x-huawei-channelSrc": "10000034", "x-inner-ntwk": "2", "x-m4c-src": "10002", "x-SvcType": "1", "X-Yun-App-Channel": "10000034", "X-Yun-Channel-Source": "10000034", "X-Yun-Client-Info": "||9|12.27.0|chrome|136.0.0.0|||windows 10||zh-CN|||Y2hyb21l||", "X-Yun-Module-Type": "100", "X-Yun-Svc-Type": "1", "Inner-Hcy-Router-Https": "1"} {
		h[key] = value
	}
	return h, nil
}
func (p *trackingMobileShare) personalRequest(ctx context.Context, endpoint string, payload any) (map[string]json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	h, err := p.signedHeaders(body)
	if err != nil {
		return nil, err
	}
	result, err := trackingJSONRequest(ctx, "POST", endpoint, body, h)
	if err != nil {
		return nil, err
	}
	return trackingMobileData(result)
}
func (p *trackingMobileShare) shareRequest(ctx context.Context, endpoint string, payload M) (map[string]json.RawMessage, error) {
	accountInfo := M{"account": p.account, "accountType": 1}
	if query, ok := payload["queryBatchOprTaskDetailReq"].(M); ok {
		query["commonAccountInfo"] = accountInfo
	} else {
		payload["commonAccountInfo"] = accountInfo
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	headers, err := p.signedHeaders(body)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher([]byte("PVGDwmcvfs1uV3d1"))
	if err != nil {
		return nil, err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err = rand.Read(iv); err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(body)%aes.BlockSize
	body = append(body, bytes.Repeat([]byte{byte(pad)}, pad)...)
	ciphertext := make([]byte, len(body))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, body)
	content := base64.StdEncoding.EncodeToString(append(iv, ciphertext...))
	req, err := http.NewRequestWithContext(ctx, "POST", "https://share-kd-njs.yun.139.com/yun-share"+endpoint, strings.NewReader(content))
	if err != nil {
		return nil, errors.New("移动盘分享请求无效")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("hcy-cool-flag", "1")
	resp, err := trackingProviderHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("移动盘分享接口无法连接")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("移动盘分享接口返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return nil, errors.New("移动盘分享响应读取失败")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("移动盘分享响应为空")
	}
	if raw[0] != '{' {
		var quoted string
		if json.Unmarshal(raw, &quoted) == nil {
			raw = []byte(quoted)
		}
		decoded, e := base64.StdEncoding.DecodeString(string(raw))
		if e != nil || len(decoded) < 32 || (len(decoded)-16)%16 != 0 {
			return nil, errors.New("移动盘分享响应无法解密")
		}
		plain := make([]byte, len(decoded)-16)
		cipher.NewCBCDecrypter(block, decoded[:16]).CryptBlocks(plain, decoded[16:])
		pad := int(plain[len(plain)-1])
		if pad < 1 || pad > 16 || pad > len(plain) || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
			return nil, errors.New("移动盘分享响应格式错误")
		}
		raw = plain[:len(plain)-pad]
	}
	result, err := trackingObject(raw)
	if err != nil {
		return nil, err
	}
	return trackingMobileData(result)
}
func (p *trackingMobileShare) List(ctx context.Context, parent string, share bool) ([]trackingShareFile, error) {
	all := []trackingShareFile{}
	cursor := ""
	for page := 0; page < 1000; page++ {
		if share {
			data, err := p.shareRequest(ctx, "/richlifeApp/devapp/IOutLink/getOutLinkInfoV6", M{"getOutLinkInfoReq": M{"account": p.account, "linkID": p.share, "passwd": p.password, "pCaID": parent, "bNum": page*100 + 1, "eNum": (page + 1) * 100, "caSrt": 0, "coSrt": 0, "srtDr": 0}})
			if err != nil {
				return nil, err
			}
			var dirs, files []map[string]json.RawMessage
			if len(data["caLst"]) == 0 {
				data["caLst"] = json.RawMessage("[]")
			}
			if len(data["coLst"]) == 0 {
				data["coLst"] = json.RawMessage("[]")
			}
			if json.Unmarshal(data["caLst"], &dirs) != nil || json.Unmarshal(data["coLst"], &files) != nil {
				return nil, errors.New("移动盘分享目录数据无效")
			}
			for _, f := range dirs {
				all = append(all, trackingShareFile{ID: trackingString(f, "caID"), Name: trackingString(f, "caName"), Dir: true, Token: trackingString(f, "path"), Parent: parent})
			}
			for _, f := range files {
				token := trackingString(f, "path")
				if token == "" {
					token = trackingString(f, "coPath")
				}
				all = append(all, trackingShareFile{ID: trackingString(f, "coID"), Name: trackingString(f, "coName"), Size: trackingNumber(f, "coSize"), Token: token, Parent: parent})
			}
			if trackingString(data, "nextPageCursor") == "" {
				return all, nil
			}
		} else {
			data, err := p.personalRequest(ctx, p.host+"/file/list", M{"parentFileId": parent, "pageInfo": M{"pageCursor": cursor, "pageSize": 100}, "orderBy": "updated_at", "orderDirection": "DESC", "imageThumbnailStyleList": []string{"Small"}})
			if err != nil {
				return nil, err
			}
			var files []map[string]json.RawMessage
			if json.Unmarshal(data["items"], &files) != nil {
				return nil, errors.New("移动盘个人云目录数据无效")
			}
			for _, f := range files {
				all = append(all, trackingShareFile{ID: trackingString(f, "fileId"), Name: trackingString(f, "name"), Size: trackingNumber(f, "size"), Dir: trackingString(f, "type") == "folder", Parent: parent})
			}
			next := trackingString(data, "nextPageCursor")
			if next == "" {
				return all, nil
			}
			if next == cursor {
				return nil, errors.New("移动盘目录分页异常")
			}
			cursor = next
		}
	}
	return nil, errors.New("移动盘目录过大，请缩小范围")
}
func (p *trackingMobileShare) Mkdir(ctx context.Context, parent, name string) (string, error) {
	data, err := p.personalRequest(ctx, p.host+"/file/create", M{"parentFileId": parent, "name": name, "description": "", "type": "folder", "fileRenameMode": "force_rename"})
	if err != nil {
		return "", err
	}
	if fid := trackingString(data, "fileId"); fid != "" {
		return fid, nil
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
	return "", errors.New("移动盘新目录尚不可见")
}
func (p *trackingMobileShare) Save(ctx context.Context, parent string, files []trackingShareFile) (string, error) {
	paths := []string{}
	for _, f := range files {
		if f.Token == "" {
			return "", errors.New("移动盘分享未返回文件路径，无法转存此分享")
		}
		paths = append(paths, f.Token)
	}
	info := M{"linkID": p.share, "contentInfoList": paths, "catalogInfoList": []string{}, "newCatalogID": parent, "needPassword": p.protected}
	data, err := p.shareRequest(ctx, "/richlifeApp/devapp/IBatchOprTask/createOuterLinkBatchOprTask", M{"createOuterLinkBatchOprTaskReq": M{"msisdn": p.account, "ownerAccount": p.owner, "taskType": 1, "taskInfo": info, "linkID": p.share, "needPassword": p.protected}})
	if err != nil {
		return "", err
	}
	task := trackingString(data, "taskID")
	if task == "" {
		task = trackingString(data, "taskId")
	}
	if task == "" {
		return "", errors.New("移动盘未返回转存任务 ID，请稍后重新核对目录")
	}
	return task, nil
}
func (p *trackingMobileShare) Wait(ctx context.Context, task string) error {
	for {
		data, err := p.shareRequest(ctx, "/richlifeApp/devapp/IBatchOprTask/queryBatchOprTaskDetail", M{"queryBatchOprTaskDetailReq": M{"taskID": task, "msisdn": p.account}})
		if err != nil {
			return err
		}
		detail, err := trackingObject(data["queryBatchOprTaskDetailRes"])
		if err != nil {
			detail = data
		}
		batch, err := trackingObject(detail["batchOprTask"])
		if err != nil {
			return errors.New("移动盘未返回转存任务状态")
		}
		if trackingNumber(batch, "taskStatus") == 2 {
			if trackingNumber(batch, "taskResultCode") != 1 {
				return errTrackingTransferFailed
			}
			return nil
		}
		if status := trackingNumber(batch, "taskStatus"); status == 3 || status == 4 {
			return errTrackingTransferFailed
		}
		if err = trackingPause(ctx, time.Second); err != nil {
			return err
		}
	}
}
