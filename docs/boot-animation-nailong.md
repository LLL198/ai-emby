# AI EMBY 半脸奶龙开屏动画

以用户提供的奶龙表情图为形象参考，使用内置 **imagegen** 重绘半脸特写：单只绿色圆眼、黑瞳孔、平平的闭口笑嘴和光滑黄色头部。按用户反馈缩小角色并向右调整取景，增加留白。另半张脸位于画面右边界之外；重点保留单眼缓缓睁开的效果，调整构图让嘴巴只露出一半。脸和嘴保持稳定，只让一只眼睛由闭到睁。

- 成片：`frontend/web/assets/ai-emby-boot-nailong-half-v5-4k.mp4`
- 封面：`frontend/web/assets/ai-emby-boot-nailong-half-poster-v5-4k.jpg`
- 睁眼素材：`frontend/web/assets/ai-emby-boot-nailong-half-open-v3.png`
- 闭眼素材：`frontend/web/assets/ai-emby-boot-nailong-half-closed-v3.png`
- 预览：`/web/boot-preview-nailong.html`

3840 × 2160（4K），30 fps，7.2 秒，静音 H.264 MP4，索引前置。约 2.65–4.4 秒，单眼缓缓睁开，显露绿色眼睛与黑色圆瞳；标题 AI EMBY 从左向右逐字勾勒轮廓，再依次亮成金属字。镜头保持稳定以突出眼睛与半张嘴的画面边缘构图；哑光眼睛保持参考表情的简单质感。

网页默认开屏继续使用金发女主，奶龙是额外成片，预览中可切换。

## 素材与提示词

模式：内置 imagegen。用户参考图副本：`E:/123/goemby/diagnostics/splash-reference/nailong-meme-reference.png`。

缩小后的睁眼素材（先前 v2 半脸素材为编辑输入）：

```text
Use case: precise composition edit. Edit this existing 16:9 Nailoong / 奶龙 half-face opening background. The character is too large. REDUCE THE CHARACTER'S SIZE by approximately 15 percent, and reframe farther to the RIGHT, keeping the right half-face composition and only ONE eye visible. Preserve the identical simple yellow meme character, flat pale green oval eye, solid round black pupil, small closed deadpan smile and cinematic cyan rim lighting. More black/navy breathing room around the character; the left 55% remains mostly clean near-black title space. Eye and mouth should both be visibly smaller than the input. Keep the visible eye near x=81%, y=38%, and the WHOLE tiny mouth with both corners visible near x=91%, y=69%. The other eye stays completely OUTSIDE the right border, no part of it visible. Smoothly extend the original navy background to fill any revealed area. The upper head may still be cropped by the top border so this stays a half-face closeup, not a full portrait. No hard rectangle boundaries. Preserve the mouth shape, vacant expression, yellow color and simple eye exactly; no additional eyes, limbs, tongue, eyelashes, human nose, text, lettering, logo or HUD. Produce a full landscape 16:9 OPEN-EYE plate.
```

闭眼素材（缩小后的睁眼素材为局部编辑目标）：

```text
Use case: precise-object-edit. Produce an exactly aligned CLOSED-EYE animation keyframe from this smaller half-face Nailoong meme portrait. Change ONLY the ONE visible eye: lower a smooth yellow eyelid over the whole green oval and black pupil so the eye is completely closed. The closed eye should be a simple gently curved horizontal dark crease near the lower middle of the original eye, no eyelashes or eyebrows, and absolutely no green or black eyeball visible. Keep the flat tiny closed smile EXACTLY unchanged, and preserve all pixels outside the single eye socket: same yellow head silhouette, same neck, same smaller size, same closeup crop, same one-eye half-face composition, same lighting, same background and bokeh, same black title space, same 16:9 dimensions. Do not shift, zoom, tilt, resize, add a second eye, change mouth, add any limbs, or change expression. Full landscape canvas, not a crop.
```

## 重制

```text
node scripts/render-boot-animation.cjs [Canvas 模块路径] [FFmpeg 路径] [可选检查帧目录] --nailong
node scripts/preview-boot-animation.cjs
```

附加 `--stills-only` 可仅导出检查帧。默认不带 `--nailong` 时重制金发版本。眼部坐标依当前 1672 × 941 半脸素材标定；奶龙镜头向右平移约 64 个基准画面像素（4K 成片中为 128 像素），使嘴巴只露出一半。
