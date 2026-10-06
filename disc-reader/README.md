# 原生蓝光按需读盘组件

`ai-emby-disc-reader` 是 Go 播放服务私有使用的 libbluray 桥接进程。通过 `bd_open_stream` 的扇区读取回调，从服务的临时回环 HTTP 视图读取 ISO，不需要挂载、下载完整源镜像或提取中间正片文件。服务负责源地址、Range 校验、缓存上限、暂停与取消；组件负责最长主片选择、默认角度、片段导航与精确字节跳转。

## 构建

源码 Dockerfile 与发布包的 Dockerfile.runtime 均自动编译并安装组件。Go 服务继续使用 `CGO_ENABLED=0`，组件单独动态链接 libbluray 与 libcurl。

自行运行 Linux 二进制时，以 Debian/Ubuntu 为例：

```sh
sudo apt-get install gcc pkg-config libbluray-dev libcurl4-openssl-dev
cc -std=c11 -O2 -Wall -Wextra -Werror -D_FORTIFY_SOURCE=2 -fstack-protector-strong \
  -Wl,-z,relro,-z,now -o ai-emby-disc-reader reader.c \
  $(pkg-config --cflags --libs libbluray libcurl)
sudo install -m 755 ai-emby-disc-reader /usr/local/bin/
```

运行环境需要匹配的 libbluray/libcurl 共享库；项目默认 Debian bookworm 镜像安装 `libbluray2` 与 `libcurl4`。组件需能在播放服务的 `PATH` 中找到。实时输出和 HLS 分段模式不保存完整播放输出，旧客户端的文件缓存路径继续保留。

## 私有协议

参数为一个 `http://127.0.0.1:端口/随机路径/movie` 地址。组件不接受外网地址、禁用代理和重定向，ISO 读取必须返回完整的 HTTP 206 分段。标准错误不进入客户端错误消息。

所有数值为大端编码：

| 方向 | 格式 |
| --- | --- |
| 启动后的 stdout | `BDR2`（4 字节）+ 主片字节数（uint64）+ 时长（uint64，90 kHz）+ 播放列表编号（uint32） |
| stdin 读取请求 | `READ`（4 字节）+ 长度（uint32）+ 主片偏移（uint64） |
| stdout 回复 | 状态（uint32，0 表示成功）+ 返回长度（uint32）+ 数据 |
| stdin 时间定位 | `SEEK`（4 字节）+ 0（uint32）+ 目标时间（uint64，90 kHz） |
| 时间定位成功回复 | 0（uint32）+ 16（uint32）+ 实际字节位置（uint64）+ 实际时间（uint64，90 kHz） |
| 时间定位失败回复 | 非零状态（uint32）+ 0（uint32） |

Go 端兼容旧版 `BDR1` 的字节读取，但只对 `BDR2` 使用时间定位。`bd_seek_time` 返回邻近访问点；实际时间必须参与分段时间轴，不能当成精确请求时间。时间索引缺失、时长不一致或访问点稀疏时，HLS 保留准确解码定位并关闭原编码复制。时间定位失败不终止组件，后续字节读取仍可继续。

单次读取不超过 1 MiB。Go 端串行访问同一组件，较大的 `ReadAt` 自动拆分请求。libbluray 定位可能回到较早的可读位置，因此组件会补读到精确偏移后再返回数据。EOF、损坏请求或读取失败会结束进程。

组件与临时监听绑定播放任务上下文，取消会终止组件并取消上游请求。目录探测计时独立，成功后切换到整个播放任务的上下文。组件不设置整次读取的固定超时，以便任务暂停；上游的读取期限由 Go 服务管理。

当前选择最长播放列表的第一个角度，不执行菜单，不解密 AACS/BD+ 光盘。验证范围见 [ISO 排查记录](../docs/iso-playback-investigation.md)。
