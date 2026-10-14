# 动态照片

状态：实现 iPhone Live Photo 与安卓 Motion Photo 的导入、预览、播放及完整原件下载；实际设备导出的格式矩阵仍需持续补样。

## 导入和资产

- 一张动态照片只有一个照片资产 ID。相册成员、标签、权限、复制、改名、回收站和彻底删除都作用于这个资产。
- iPhone：在同一次选择中提交同目录、同名的 JPEG/HEIC 与 MOV，扩展名及文件名大小写不敏感。前端拒绝缺失照片或同名候选不唯一的组合。服务端读取 Apple MakerNote 的 ContentIdentifier 与 MOV 的 `com.apple.quicktime.content.identifier`；只要一端有标识，就要求两端存在且相同。两端均无标识的导出件允许以用户明确选择的唯一同名组合导入，不跨批次或后台猜测配对。
- 安卓：识别 Google Camera XMP 中的 MotionPhoto + Container Directory，以及旧式 MicroVideo + MicroVideoOffset。按命名空间读取属性和元素，不依赖前缀；视频位置由文件尾和声明长度确定，支持视频前有 GainMap 的组合。MotionPhoto=0 明确表示普通照片。已识别的声明若缺视频、长度越界或视频无法探测，整个导入失败，不默默降级成静态照片。
- JPEG 从扫描数据前的 APP1 读取 XMP。HEIC 当前从前 4 MiB 读取完整 XMP packet；非标准厂商封装、扩展 XMP、超出窗口的元数据及 AVIF 暂不保证动态识别。浏览器或分享软件已经移除的视频不能恢复，上传界面提示选择未修改原件。
- 每个原文件最多 256 MiB；iPhone 两个文件分别计限。动态视频须有有效视频轨且时长大于 0、不超过 30 秒。独立视频不作为照片导入。
- 配对上传按照片、MOV 的顺序流式写入受管暂存目录，不缓冲完整原件或写入系统临时盘。上传完成不等待 AI。视频探测和静态头部校验先完成，两份对象引用和资产在一个事务内提交；失败回收暂存文件及本次新发布的无引用对象。

## 保存和生命周期

照片原件保持字节不变。iPhone MOV 是第二个不可变内容对象；安卓保留包含视频的整份照片原件，并提取片段为单独内容对象，方便通过现有的只读文件描述符解码接口播放。提取会额外占用该片段的空间；按 SHA-256 去重，复制不重复存储。

Catalog 的资产保存主图和视频引用、视频名称、类型与时长。复制保留二者，恢复不拆分，彻底删除和崩溃对账只回收没有任何主图或视频引用的对象。同一主图配不同视频不归为同一图库的重复照片；跨成员“也保存过”提示仍按主图字节匹配。

下载普通照片或安卓动态照片返回原件；下载 Live Photo 返回 ZIP，内含保持原始字节、使用同一文件名主体的照片与 MOV。单独的原图及视频接口支持 Range，且每次请求重新做资产授权。ZIP 只是无损传递两份原件，不保证任意手机相册应用直接识别 ZIP，重新导入时解压并同时选择两份文件。

## 预览和资源

网格只显示静态缩略图与“动态照片”标识，不加载视频。查看器提供明确的动态播放按钮，默认静音，可用播放器控件开声；播放结束、切换照片或关闭查看器停止播放。失败时保留静态图和原件下载入口。

HEIC 主图的尺寸和容器旋转从 primary item 关联属性读取；通过隔离 Media Worker 的 libheif 生成兼容图片。动态片段按需通过同一 Worker 转为最高 1080 高度的 H.264/AAC MP4，不写转码缓存。相册同时最多两个解码或播放任务，图片任务 30 秒、视频播放请求 45 秒超时；请求断开取消解码。元数据探测有 15 秒超时。Worker 本身继续受 systemd 的连接数、CPU、内存和无网络约束。

`anas-media.socket` 使用 `root:a-nas-photos 0660`，已有该附加组的产品服务与相册服务均可访问。相册服务不加入产品服务组，Worker 不获得受管图库路径权限；所有输入仍是只读 FD。

## AI 与模型边界

- 默认只分析主照片：缩略图用于图像向量，转正的 1600 px 主图用于物体检测；HEIC 先生成同样的兼容图片。视频对象从不排入缩略图、embedding 或检测任务。
- 标签、搜索与 AI 聚合仍按一个资产呈现；同一主照片的多份资产复用内容对象的派生结果，不随复制或视频播放重复推理。
- 沿用现有空闲调度、低优先级与取消规则。新功能无需增加模型、视觉 token、逐帧任务或上传云端的步骤。
- 该版本的搜索只能描述主图，不宣称理解动作、音频或短片其他帧。未来若做动作搜索，应另设用户可控的后台任务，限制采样帧数、总像素和并发，记录模型版本、片段时间点与证据；不能把单帧结果无来源地合并成整张照片的事实。

## 关联

- 调查：[Apple 配对标识解析](../investigations/2026-10-14-live-photo-identifiers.md)

- 规格：[相册](photo-library.md)、[相册界面](photo-gallery-ui.md)
- 架构：[相册技术设计](../architecture/photo-library.md)、[AI 调度](../architecture/photo-ai.md)
- 实现：[资产与视频生命周期](../../internal/photos/motion.go)、[安卓格式识别](../../internal/photos/motionformat.go)、[HEIF 主图属性](../../internal/photos/heifmetadata.go)、[API 与导出](../../internal/photosapi/motion.go)、[上传配对](../../web/src/photos/uploadGroups.ts)、[查看器](../../web/src/photos/Viewer.tsx)
- 验证：[生命周期、Apple 标识与 AI 队列](../../internal/photos/motion_test.go)、[真实编解码](../../internal/photos/motioncodecs_test.go)、[格式边界](../../internal/photos/motionformat_test.go)、[接口权限、Range 与原件导出](../../internal/photosapi/motion_test.go)、[前端配对](../../web/src/photos/uploadGroups.test.ts)
- 格式来源：[Android Motion Photo 官方格式](https://developer.android.com/media/platform/motion-photo-format)、[Apple Live Photo movie metadata](https://developer.apple.com/documentation/avfoundation/avcapturephotosettings/livephotomoviemetadata)
