package main

import (
 "encoding/json"
 "net/http/httptest"
 "net/url"
 "strings"
 "testing"
)

func TestLoginBodyFormats(t *testing.T) {
 for _, tc := range []struct{name, ct, data, user, pw string; ok bool}{
  {"ios", "application/x-www-form-urlencoded; charset=UTF-8", "Username=admin&Pw=test-password-12345", "admin", "test-password-12345", true},
  {"escaped", "application/x-www-form-urlencoded", url.Values{"username":{"用户"}, "pw":{"a+b&%= 空格"}}.Encode(), "用户", "a+b&%= 空格", true},
  {"json", "application/json", `{"Username":"admin","Pw":"secret"}`, "admin", "secret", true},
  {"bad-form", "application/x-www-form-urlencoded", "Username=admin&Pw=%zz", "", "", false},
  {"bad-json", "application/json", "Username=admin", "", "", false},
  {"oversize", "application/x-www-form-urlencoded", "Pw="+strings.Repeat("x", 1<<20), "", "", false},
  {"no-query-credentials", "application/x-www-form-urlencoded", "", "", "", true},
 } {
  t.Run(tc.name, func(t *testing.T) {
   r:=httptest.NewRequest("POST", "/emby/Users/authenticatebyname?Username=query&Pw=query", strings.NewReader(tc.data))
   r.Header.Set("Content-Type",tc.ct)
   w:=httptest.NewRecorder()
   var b loginCredentials
   ok:=loginBody(w,r,&b)
   if ok!=tc.ok {t.Fatalf("ok=%v status=%d",ok,w.Code)}
   if ok && (b.Username!=tc.user || b.Pw!=tc.pw) {t.Fatal("credentials decoded incorrectly")}
   if !ok && w.Code!=400 {t.Fatalf("status=%d",w.Code)}
  })
 }
}

func TestIOSFormLogin(t *testing.T) {
 a:=testApp(t)
 for _, tc:=range []struct{pw string; status int}{{"test-password-12345",200},{"wrong-password",401}} {
  r:=httptest.NewRequest("POST","/emby/Users/authenticatebyname?X-Emby-Client=Emby+for+iOS&X-Emby-Device-Name=iPhone&X-Emby-Device-Id=ios-form-test&X-Emby-Client-Version=2.2.58",strings.NewReader(url.Values{"Username":{"admin"},"Pw":{tc.pw}}.Encode()))
  r.Header.Set("Content-Type","application/x-www-form-urlencoded; charset=UTF-8")
  r.Header.Set("Origin","emby-local://app")
  w:=httptest.NewRecorder()
  a.serve(w,r)
  if w.Code!=tc.status {t.Fatalf("status=%d want=%d",w.Code,tc.status)}
  if tc.status==200 {
   var result struct{AccessToken string; SessionInfo struct{Client,DeviceId string}}
   if err:=json.Unmarshal(w.Body.Bytes(),&result);err!=nil {t.Fatal(err)}
   if result.AccessToken=="" || result.SessionInfo.Client!="Emby for iOS" || result.SessionInfo.DeviceId!="ios-form-test" {t.Fatal("incomplete login response")}
  }
 }
}
