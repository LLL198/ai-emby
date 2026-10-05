package main
import("net/http";"strings";"mime";"path" )
func(a *App) filesRoute(w http.ResponseWriter,r *http.Request) bool {
 if r.URL.Path=="/api/files"||r.URL.Path=="/api/files/rename"{a.filesAPI(w,r);return true}
 if r.URL.Path=="/files" {http.Redirect(w,r,"/files/",302);return true}
 if !strings.HasPrefix(r.URL.Path,"/files/"){return false}
 if r.Method!="GET"&&r.Method!="HEAD"{fail(w,400,"不支持的请求");return true}
 name:=strings.TrimPrefix(r.URL.Path,"/files/");if name==""{name="index.html"}
 if strings.Contains(name,"..")||strings.Contains(name,"\\"){fail(w,400,"无效路径");return true}
 b,e:=assets.ReadFile("web/files/"+name);if e!=nil{fail(w,404,"文件不存在");return true}
 w.Header().Set("Content-Type",mime.TypeByExtension(path.Ext(name)))
 w.Header().Set("Content-Security-Policy","default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'self'")
 w.Write(b);return true
}
