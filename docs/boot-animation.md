# AI Emby 开屏动画

参考 [NativeDog1/dsh-boot-animation](https://github.com/NativeDog1/dsh-boot-animation) 的「DeepSeek 赛博朋克片头」，重制半脸特写、眼睛由闭到睁、蓝色反光与金属标题的节奏。

当前为原创成年女角色：金发、蓝眼、清冷气质。参考《鸣潮》卡提希娅、菲比的美术质感，重新设计面容、发饰和服装。单眼特写保留鼻尖、完整嘴唇与下巴。没有直接使用原片或游戏角色图作为产品素材。标题为全大写 `AI EMBY`。

## 成片与预览

新增 [4K 成片与动态登录页](login-background.md)：完整片头为 `ai-emby-boot-v4-4k.mp4`；预览页与已登录用户开屏使用该版本，未登录用户的 4K 背景恢复同样的黑屏、线条扫描显影和睁眼时间点；大字保持原字号比例，放在左上方，登录框置于其下。以下 v4 信息保留为原始 1080p 版本记录。

另有[奶龙搞怪版](boot-animation-nailong.md)，两种版本可在预览页切换。

- 成片：`frontend/web/assets/ai-emby-boot-v4.mp4`
- 封面：`frontend/web/assets/ai-emby-boot-poster-v4.jpg`
- 睁眼原图：`frontend/web/assets/ai-emby-boot-gold-open-v3.png`
- 闭眼原图：`frontend/web/assets/ai-emby-boot-gold-closed-v3.png`
- 预览：`/web/boot-preview.html`，可播放、直接看标题或睁眼段落、下载 MP4。

1920 × 1080，30 fps，7.2 秒，静音 H.264 / yuv420p MP4，索引前置。约 0–2.5 秒扫描显影；2.65–4.4 秒眼睑缓缓抬起，逐步露出虹膜，最后淡出。

标题从 2.35 秒起，按 A → I → E → M → B → Y 的顺序依次勾勒轮廓，每个字母间隔 0.18 秒；3.25 秒起又按相同顺序逐个亮成金属字，4.15 秒完成。已出现的字母位置固定，后续字母独立进入，空格保持最终排版。

动作由匹配的睁眼、闭眼原图驱动。仅在眼部局部区域移动眼睑与睫毛，虹膜保持原形逐步显露；面部、嘴唇和头发保持稳定。数字反光限制在当前眼睛开口和虹膜范围内。镜头轻微推移，嘴唇始终完整可见。

前端入口 `frontend/index.html` 已接入。每个标签页首次访问播放一次；刷新不重复，重新打开标签页会播放。支持跳过、Escape、减少动态效果偏好、媒体失败及自动播放拒绝回退；加载超过 4 秒或整个开屏超过 12 秒时解除覆盖。竖屏完整显示视频。源码已更新，其它运行中的部署需更新对应前端文件。

## 素材生成记录

模式：内置 **imagegen**。先生成原创角色，再编辑取景和闭眼关键帧；动画与标题由 Canvas 制作，FFmpeg 编码。图像编辑均使用 imagegen，保留原始输出。

初始设计方向：原创成年女性，优质 3D 二次元游戏过场质感，金色长发、湛蓝虹膜、清冷表情，非对称蓝晶发夹、珍珠耳饰、白与深蓝衣领配浅金饰边。右侧单眼半脸，左侧近黑留白，蓝色轮廓光，无文字、标志或 HUD。参考图仅用于镜头、光线和美术方向。

最终取景编辑提示词（输出为睁眼原图）：

```text
Use case: precise-object-edit / camera reframing. Edit this original blonde blue-eyed adult anime game heroine startup-film keyframe. Preserve EXACT character identity, aloof calm expression, gold-blonde hair, crystal clip, pearl earring, white/navy collar, cinematic 3D game rendering, blue rim lighting and dark background. Change ONLY framing: pull camera back about 10 percent and translate her head slightly LEFT so the ENTIRE mouth is visible including BOTH lip corners with 70 pixels of breathing room to the right of the right lip corner. The chin and nose tip must also fully fit. Her visible single blue eye remains a large prominent half-face close-up on the right around x=79%, y=42%; keep other eye hidden by hair or beyond right frame. The full lips should center around x=90%, y=72% and be visibly enclosed inside the frame, not cut by its right edge. Keep left 45% mostly near-black for later typography. Preserve 16:9 landscape composition. No text, no logo, no UI, no second character, no open mouth, no smile. This must remain the same original female character, just framed correctly with complete mouth.
```

闭眼编辑提示词（以最终睁眼原图作为输入）：

```text
Use case: precise-object-edit. Produce the perfectly matching CLOSED-EYE keyframe for this original blonde blue-eyed adult female animation character. Make ONE strictly local change: naturally close the SINGLE visible eye, upper eyelid lowered, eyelashes resting together along a gently curved closed eye line. No iris, no pupil, no blue light visible through lid. The eyelid should rest near the lower boundary of the original open eye, with subtle realistic cool skin shading. CRITICAL preserve identical canvas dimensions, camera framing, exact pixel alignment, head size, face outline, nose, FULL mouth and both lip corners, hair, crystal clip, earring, shoulder, costume, blue rim light, skin texture, black left half and background bokeh. Do NOT move, zoom, reframe, restyle, or redraw character. All pixels OUTSIDE the single eyelid region must stay identical to input. Preserve calm aloof facial expression with lips closed, no smile. No text or UI. Return full landscape 16:9 canvas, not a crop.
```

## 重制

动画源文件：`scripts/render-boot-animation.cjs`。需 Node.js、`@napi-rs/canvas`、支持 libx264 的 FFmpeg。Windows 自动注册微软雅黑、Consolas 与 Segoe UI；其它平台需支持中文的字体。

```text
node scripts/render-boot-animation.cjs [Canvas 模块路径] [FFmpeg 路径] [可选检查帧目录]
```

不传前两个参数时使用本机模块与 PATH。重制覆盖 v4 成片与封面，保留原图。传入检查帧目录后可再附加 `--stills-only`，仅检查关键画面，不编码视频。眼部坐标依据当前 1672 × 941 素材标定；换角色时需重新标定。

本机预览（支持视频跳转）：

```text
node scripts/preview-boot-animation.cjs
```

打开 `http://127.0.0.1:8768/web/boot-preview.html`。服务仅监听本机；可通过第一个参数换端口。
