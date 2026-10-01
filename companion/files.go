package main

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "io/fs"
 "net"
 "net/http"
 "os"
 "path/filepath"
 "sort"
 "strings"
 "sync"
 "syscall"
 "time"
 "golang.org/x/sys/unix"
)

var filesMu sync.RWMutex
var errFilePath = errors.New("路径不合法，禁止目录遍历和非法字符")
var errFileLink = errors.New("禁止操作符号链接")
type fileEntry struct {
 Name string `json:"name"`
 Path string `json:"path"`
 IsDir bool `json:"isDir"`
 Size int64 `json:"size"`
 Modified time.Time `json:"modified"`
}
func fileRoot() string { if p:=os.Getenv("FILE_MANAGER_ROOT"); p!="" {return p}; return "/media" }
func fileName(p string) (string,error) {
 if strings.ContainsAny(p, `\:*?"<>|`) || strings.ContainsAny(p,"\x00\r\n") {return "",errFilePath}
 p=strings.TrimPrefix(p,"/")
 if p=="" {return ".",nil}
 for _,s:=range strings.Split(p,"/") {if s==".." || s=="." || s=="" {return "",errFilePath}}
 if !filepath.IsLocal(p) {return "",errFilePath};return p,nil
}
// Reject links for predictable UI behavior. os.Root also enforces containment
// during the actual operation, including concurrent symlink replacement.
func fileCheck(root *os.Root,p string) (fs.FileInfo,error) {
 var fi fs.FileInfo
 var e error
 if p=="." {return root.Stat(".")}
 parts:=strings.Split(p,"/")
 for i:=range parts {
  fi,e=root.Lstat(strings.Join(parts[:i+1],"/"));if e!=nil{return nil,e}
  if fi.Mode()&os.ModeSymlink!=0{return nil,errFileLink}
 }
 return fi,nil
}
func fileFail(w http.ResponseWriter,e error) {
 code:=500; message:="文件操作失败"
 switch {
 case errors.Is(e,errFilePath):code=400;message=e.Error()
 case errors.Is(e,errFileLink):code=403;message=e.Error()
 case errors.Is(e,fs.ErrNotExist):code=404;message="文件或目录不存在"
 case errors.Is(e,fs.ErrPermission),errors.Is(e,syscall.EROFS):code=403;message="权限不足或目录只读"
 case errors.Is(e,fs.ErrExist),errors.Is(e,syscall.ENOTEMPTY):code=400;message="同级目录已存在该名称"
 }
 fail(w,code,message)
}
func (a *App) filesAPI(w http.ResponseWriter,r *http.Request) {
 u,e:=a.auth(r);if e!=nil || !u.Admin || u.API {fail(w,403,"需要管理员登录");return}
 if r.Method=="GET" {filesMu.RLock();defer filesMu.RUnlock()} else {filesMu.Lock();defer filesMu.Unlock()}
 root,e:=os.OpenRoot(fileRoot());if e!=nil {fileFail(w,e);return};defer root.Close()
 if r.Method=="GET" && r.URL.Path=="/api/files" {a.listFiles(w,r,root);return}
 if r.Method=="POST" && r.URL.Path=="/api/files/rename" {
  var b struct{OldPath,NewPath string};if !body(w,r,&b){return}
  old,e:=fileName(b.OldPath);if e!=nil {fileFail(w,e);return};next,e:=fileName(b.NewPath);if e!=nil {fileFail(w,e);return}
  if old=="." || next=="." || filepath.Dir(old)!=filepath.Dir(next) || strings.TrimSpace(filepath.Base(next))=="" {fail(w,400,"只能重命名同级文件夹，不能操作根目录");return}
  fi,e:=fileCheck(root,old);if e!=nil {fileFail(w,e);return};if !fi.IsDir(){fail(w,400,"只支持重命名文件夹");return}
  if _,e=fileCheck(root,next);e==nil {fail(w,400,"同级目录已存在该名称");return} else if !errors.Is(e,fs.ErrNotExist){fileFail(w,e);return}
  if e=a.filesAvailable(r.Context(),[]string{old});e!=nil{fail(w,403,e.Error());return}
  // Hold the parent directory descriptor and atomically refuse overwrite.
  dir,e:=root.Open(filepath.Dir(old));if e!=nil{fileFail(w,e);return};defer dir.Close()
  e=unix.Renameat2(int(dir.Fd()),filepath.Base(old),int(dir.Fd()),filepath.Base(next),unix.RENAME_NOREPLACE)
  if e!=nil{fileFail(w,e);return}
  a.filesChanged([]string{old,next});respond(w,M{"ok":true});return
 }
 if r.Method=="DELETE" && r.URL.Path=="/api/files" {
  var b struct{Paths []string};if !body(w,r,&b){return}
  if len(b.Paths)==0 || len(b.Paths)>100 {fail(w,400,"请选择1至100个文件");return}
  paths:=[]string{}
  for _,s:=range b.Paths {p,e:=fileName(s);if e!=nil{fileFail(w,e);return};if p=="."{fail(w,400,"不能删除根目录");return};if _,e=fileCheck(root,p);e!=nil{fileFail(w,e);return};for _,prev:=range paths{if pathsOverlap(prev,p){fail(w,400,"删除路径不能重复或互相包含");return}};paths=append(paths,p)}
  if e=a.filesAvailable(r.Context(),paths);e!=nil{fail(w,403,e.Error());return}
  deleted:=[]string{}
  for _,p:=range paths {if e=root.RemoveAll(p);e!=nil{a.filesChanged(deleted);w.Header().Set("Content-Type","application/json");w.WriteHeader(500);json.NewEncoder(w).Encode(M{"error":"删除失败，请刷新目录确认已完成的操作", "deleted":deleted});return};deleted=append(deleted,p)}
  a.filesChanged(paths);respond(w,M{"ok":true,"deleted":deleted});return
 }
 fail(w,400,"不支持的文件操作")
}
func (a *App) listFiles(w http.ResponseWriter,r *http.Request,root *os.Root) {
 p,e:=fileName(q(r,"path"));if e!=nil{fileFail(w,e);return}
 fi,e:=fileCheck(root,p);if e!=nil{fileFail(w,e);return};if !fi.IsDir(){fail(w,400,"路径不是目录");return}
 search:=strings.ToLower(strings.TrimSpace(q(r,"search")));if len(search)>512{fail(w,400,"搜索词过长");return}
 scope:=q(r,"scope");if scope!=""&&scope!="current"&&scope!="global"{fail(w,400,"无效搜索范围");return}
 order:=q(r,"sort");if order=="" {order="name"};desc:=q(r,"order")=="desc"
 if parts:=strings.Split(order,":");len(parts)==2{order=parts[0];desc=parts[1]=="desc"}
 if order!="name"&&order!="time"&&order!="size"{fail(w,400,"无效排序字段");return}
 if v:=q(r,"order");v!=""&&v!="asc"&&v!="desc"{fail(w,400,"无效排序方向");return}
 start:=p;if scope=="global"&&search!=""{start="."}
 entries:=[]fileEntry{};visited:=0
 var walk func(string) error
 walk=func(dir string)error{
  if e:=r.Context().Err();e!=nil{return e}
  f,e:=root.Open(dir);if e!=nil{return e};defer f.Close()
  for {
   ds,readErr:=f.ReadDir(512)
   for _,d:=range ds {
    visited++;if visited>500000{return fmt.Errorf("目录过大，请缩小搜索范围")}
    if d.Type()&os.ModeSymlink!=0{continue}
    child:=filepath.Join(dir,d.Name())
    if search=="" || strings.Contains(strings.ToLower(d.Name()),search) {
     info,e:=root.Lstat(child);if errors.Is(e,fs.ErrNotExist){continue};if e!=nil{return e};if info.Mode()&os.ModeSymlink!=0{continue}
     if !info.IsDir()&&!info.Mode().IsRegular(){continue}
     size:=info.Size();if info.IsDir(){size=0}
     entries=append(entries,fileEntry{d.Name(),"/"+child,info.IsDir(),size,info.ModTime()})
    }
    if scope=="global"&&search!=""&&d.IsDir(){if e:=walk(child);e!=nil{return e}}
   }
   if readErr!=nil {if readErr.Error()=="EOF"{return nil};return readErr}
  }
 }
 if e=walk(start);e!=nil{fileFail(w,e);return}
 sort.Slice(entries,func(i,j int)bool {x,y:=entries[i],entries[j];c:=0;switch order{case "time":if x.Modified.Before(y.Modified){c=-1}else if x.Modified.After(y.Modified){c=1};case "size":if x.Size<y.Size{c=-1}else if x.Size>y.Size{c=1};default:c=strings.Compare(strings.ToLower(x.Name),strings.ToLower(y.Name))};if c==0{c=strings.Compare(x.Path,y.Path)};if desc{return c>0};return c<0})
 respond(w,M{"path":q(r,"path"),"entries":entries,"total":len(entries)})
}
func (a *App) filesAvailable(ctx context.Context,paths []string) error {
 if a.db!=nil {
  for _,lib:=range a.libraries(){for _,loc:=range lib["Locations"].([]string){for _,p:=range paths{full:=filepath.Join(fileRoot(),p);if full==loc || strings.HasPrefix(loc,full+"/"){return errors.New("该目录包含媒体库根目录，请先在媒体库设置移除路径")}}}}
  var scanning int
  if e:=a.db.QueryRow("SELECT count(*) FROM libraries WHERE status='scanning'").Scan(&scanning);e!=nil{return errors.New("无法检查扫描状态")};if scanning>0{return errors.New("媒体库正在扫描，请完成后再操作")}
  rows,e:=a.db.Query("SELECT i.path FROM plays p JOIN items i ON i.id=p.item WHERE p.updated>?",time.Now().Add(-2*time.Minute).Unix());if e!=nil{return errors.New("无法检查播放状态")};defer rows.Close()
  for rows.Next(){var active string;if e=rows.Scan(&active);e!=nil{return errors.New("无法检查播放状态")};for _,p:=range paths{if pathsOverlap(filepath.Join(fileRoot(),p),active){return errors.New("文件被占用：正在播放，请停止播放后重试")}}};if rows.Err()!=nil{return errors.New("无法检查播放状态")}
 }
 sock:=os.Getenv("FILE_BUSY_SOCKET")
 if sock=="" {
  // The built-in database checks above are sufficient for the standalone
  // public deployment. An external busy-check service is optional.
  return nil
 }
 conn,e:=(&net.Dialer{Timeout:3*time.Second}).DialContext(ctx,"unix",sock);if e!=nil{return errors.New("占用检查服务不可用，已阻止操作")};defer conn.Close();conn.SetDeadline(time.Now().Add(10*time.Second))
 if e=json.NewEncoder(conn).Encode(M{"paths":paths});e!=nil{return errors.New("占用检查失败")}
 var b struct{OK bool `json:"ok"`;Error string `json:"error"`}
 if e=json.NewDecoder(conn).Decode(&b);e!=nil{return errors.New("占用检查失败")};if !b.OK {if b.Error==""{b.Error="占用检查失败"};return errors.New(b.Error)};return nil
}
func (a *App) filesChanged(paths []string) {
 if a.db==nil{return}
 // Use the existing targeted scanner, even if filesystem watching is disabled.
 for _,lib:=range a.libraries(){for _,loc:=range lib["Locations"].([]string){for _,p:=range paths{full:=filepath.Join(fileRoot(),p);if pathsOverlap(loc,full){key:=lib["Id"].(string);go a.scanLibrary(key);break}}}}
}
